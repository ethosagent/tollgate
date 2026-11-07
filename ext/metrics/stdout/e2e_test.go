package stdout_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/metrics/stdout"
	"github.com/ethosagent/tollgate/testkit"
)

func TestSink_RecordsAProxiedCallAndCloseFlushesIt(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	var out bytes.Buffer
	p, err := tollgate.New(tollgate.Options{
		Auth:    allow{},
		Metrics: stdout.New(&out),
		Limits:  tollgate.Limits{MetricFlushInterval: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-upstream-url", up.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// Close flushes, but only what is queued: wait for the call's metric.
	deadline := time.Now().Add(5 * time.Second)
	for !queued(t, p) {
		if time.Now().After(deadline) {
			t.Fatal("the call's metric was never queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &m); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	if m["provider"] != "openai" || m["finish"] != "ok" || m["status"] != float64(200) {
		t.Fatalf("line = %v", m)
	}
}

type allow struct{}

func (allow) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	return tollgate.Tenant{ID: "acme", Policy: &tollgate.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}, nil
}

// queued reports whether /statusz counts the call (counted after it is queued).
func queued(t *testing.T, p *tollgate.Proxy) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	p.AdminHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statusz", nil))
	var s struct {
		Calls struct {
			Total int64 `json:"total"`
		} `json:"calls"`
	}
	return json.Unmarshal(rec.Body.Bytes(), &s) == nil && s.Calls.Total == 1
}
