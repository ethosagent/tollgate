package tollgate_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// get issues a GET with a context, as the lint rules require.
func get(t testing.TB, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getStatus(t testing.TB, url string) int {
	t.Helper()
	resp := get(t, url)
	_ = readAll(t, resp)
	return resp.StatusCode
}

// adminServer serves p.AdminHandler on its own listener, as a host would.
func adminServer(t *testing.T, p *tollgate.Proxy) string {
	t.Helper()
	srv := httptest.NewServer(p.AdminHandler())
	t.Cleanup(srv.Close)
	return srv.URL
}

type statusz struct {
	InFlight int64 `json:"in_flight"`
	Calls    struct {
		Total      int64 `json:"total"`
		ByProvider map[string]struct {
			Calls  int64            `json:"calls"`
			Finish map[string]int64 `json:"finish"`
		} `json:"by_provider"`
	} `json:"calls"`
	Metrics struct {
		Enabled    bool  `json:"enabled"`
		QueueDepth int   `json:"queue_depth"`
		Dropped    int64 `json:"dropped"`
		SinkErrors int64 `json:"sink_errors"`
		Written    int64 `json:"written"`
		SinkReady  bool  `json:"sink_ready"`
	} `json:"metrics"`
	Hooks struct {
		UnbufferedResponses int64 `json:"unbuffered_responses"`
		DroppedEvents       int64 `json:"dropped_events"`
		UsageNotRead        int64 `json:"usage_not_read"`
		UnheldTerminals     int64 `json:"unheld_terminals"`
	} `json:"hooks"`
}

func getStatusz(t *testing.T, admin string) statusz {
	t.Helper()
	resp := get(t, admin+"/statusz")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("statusz = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var s statusz
	if err := json.Unmarshal(readAll(t, resp), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdmin_StatuszCountsCallsPerProviderAndFinish(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("acme")})
	if err != nil {
		t.Fatal(err)
	}
	admin, base := adminServer(t, p), serve(t, p)
	for range 3 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	_ = readAll(t, post(t, base, "/nope/x", []byte(`{}`), via(up)))
	eventually(t, "4 calls counted", func() bool { return getStatusz(t, admin).Calls.Total == 4 })
	s := getStatusz(t, admin)
	if o := s.Calls.ByProvider["openai"]; o.Calls != 3 || o.Finish["ok"] != 3 {
		t.Fatalf("openai = %+v, want 3 ok", o)
	}
	if n := s.Calls.ByProvider["none"]; n.Calls != 1 || n.Finish["rejected"] != 1 {
		t.Fatalf("none = %+v, want 1 rejected (unknown provider)", n)
	}
	if s.InFlight != 0 || s.Metrics.Enabled {
		t.Fatalf("statusz = %+v, want nothing in flight and no sink", s)
	}
}

func TestAdmin_StatuszShowsInFlightCalls(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 30 * time.Second})
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("acme")})
	if err != nil {
		t.Fatal(err)
	}
	admin, base := adminServer(t, p), serve(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+chatPath, strings.NewReader(`{"stream":true}`))
		req.Header.Set("x-upstream-url", up.URL)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	eventually(t, "one call in flight", func() bool { return getStatusz(t, admin).InFlight == 1 })
	cancel()
	<-done
	eventually(t, "no call in flight", func() bool { return getStatusz(t, admin).InFlight == 0 })
}

func TestAdmin_StatuszReportsQueueAndDrops(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	sink := &collectSink{}
	p, base := sinkProxy(t, sink, tollgate.Limits{})
	admin := adminServer(t, p)
	for range 2 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	eventually(t, "both written", func() bool { return getStatusz(t, admin).Metrics.Written == 2 })
	if m := getStatusz(t, admin).Metrics; !m.Enabled || !m.SinkReady || m.Dropped != 0 || m.SinkErrors != 0 {
		t.Fatalf("metrics = %+v", m)
	}
}

// promSample matches `name{labels} value`; labels are optional.
var promSample = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})? (\S+)$`)

// scrape returns value by "name{labels}" key after checking every line is
// either a comment or a well-formed sample.
func scrape(t *testing.T, admin string) map[string]float64 {
	t.Helper()
	resp := get(t, admin+"/metrics")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Fatalf("Content-Type = %q", ct)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(strings.TrimSpace(string(readAll(t, resp))), "\n") {
		if strings.HasPrefix(line, "# HELP ") || strings.HasPrefix(line, "# TYPE ") {
			continue
		}
		m := promSample.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("malformed line %q", line)
		}
		v, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			t.Fatalf("bad value in %q", line)
		}
		key := m[1]
		if m[2] != "" {
			key += "{" + m[2] + "}"
		}
		out[key] = v
	}
	return out
}

func TestAdmin_MetricsExposesCountersAndHistogramWithBoundedLabels(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	sink := &collectSink{}
	p, base := sinkProxy(t, sink, tollgate.Limits{})
	admin := adminServer(t, p)
	for range 2 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}
	_ = readAll(t, post(t, base, "/nope/x", []byte(`{}`), via(up)))
	eventually(t, "3 calls observed", func() bool {
		return scrape(t, admin)[`tollgate_call_duration_seconds_count`] == 3
	})
	got := scrape(t, admin)
	for k, want := range map[string]float64{
		`tollgate_calls_total{provider="openai",finish="ok",status_class="2xx"}`:     2,
		`tollgate_calls_total{provider="none",finish="rejected",status_class="4xx"}`: 1,
		`tollgate_call_duration_seconds_bucket{le="+Inf"}`:                           3,
		`tollgate_metrics_sink_ready`:                                                1,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	allowed := regexp.MustCompile(`^(provider="[a-z0-9_-]+"|finish="(ok|upstream_error|client_cancel|rejected|timeout|unknown)"|status_class="([1-5]xx|none|other)"|kind="[a-z_]+"|reason="[a-z_]+"|le="[0-9.e+Inf-]+")(,|$)`)
	for k := range got {
		if _, labels, ok := strings.Cut(strings.TrimSuffix(k, "}"), "{"); ok {
			for rest := labels; rest != ""; {
				m := allowed.FindString(rest)
				if m == "" {
					t.Fatalf("series %q has an unbounded or unknown label near %q", k, rest)
				}
				rest = rest[len(m):]
			}
		}
	}
}

func TestAdmin_IsNotServedOnTheProxyMux(t *testing.T) {
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("acme")})
	if err != nil {
		t.Fatal(err)
	}
	_ = p.AdminHandler()
	base := serve(t, p)
	for _, path := range []string{"/statusz", "/metrics"} {
		if got := getStatus(t, base+path); got != http.StatusNotFound {
			t.Errorf("proxy mux GET %s = %d, want 404 (admin endpoints belong on the private listener)", path, got)
		}
	}
}

func TestAdmin_UnknownPathAndMethod(t *testing.T) {
	p, err := tollgate.New(tollgate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := p.AdminHandler()
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/nope", 404}, {"POST", "/statusz", 405}, {"POST", "/metrics", 405}, {"GET", "/statusz", 200}, {"GET", "/metrics", 200},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}
