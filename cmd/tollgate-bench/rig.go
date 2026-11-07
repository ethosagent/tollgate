package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/metrics/stdout"
	"github.com/ethosagent/tollgate/testkit"
)

// rig is the system under measurement: a fake upstream, a tollgate in front
// of it with a real metric sink, and an admin listener for /statusz.
type rig struct {
	cfg      config
	fake     *testkit.FakeUpstream
	proxy    *tollgate.Proxy
	proxyURL string
	stops    []func()
}

// benchAuth admits every call as one tenant that may use the upstream
// override header to reach loopback, which is where the fake listens.
type benchAuth struct{}

func (benchAuth) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	return tollgate.Tenant{ID: "bench", Policy: &tollgate.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}, nil
}

func newRig(ctx context.Context, cfg config) (*rig, error) {
	r := &rig{cfg: cfg}
	fake, stopFake := testkit.NewServer(testkit.Options{ChunkDelay: cfg.chunkDelay})
	r.fake = fake
	r.stops = append(r.stops, stopFake)
	// The default MaxConnsPerHost (100) would queue calls and measure the cap,
	// not the proxy: give every session its own upstream connection.
	p, err := tollgate.New(tollgate.Options{
		Auth:    benchAuth{},
		Metrics: stdout.New(io.Discard),
		Limits:  tollgate.Limits{MaxConnsPerHost: 2 * cfg.sessions, MaxIdleConnsPerHost: cfg.sessions},
	})
	if err != nil {
		r.stop()
		return nil, fmt.Errorf("tollgate.New: %w", err)
	}
	r.proxy = p
	if err := p.Start(ctx); err != nil {
		r.stop()
		return nil, fmt.Errorf("start: %w", err)
	}
	if r.proxyURL, err = r.listen(ctx, p.Handler()); err != nil {
		r.stop()
		return nil, err
	}
	return r, nil
}

// listen serves h on a loopback port and returns its base URL.
func (r *rig) listen(ctx context.Context, h http.Handler) (string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }() // ends with srv.Close in stop
	r.stops = append(r.stops, func() { _ = srv.Close() })
	return "http://" + ln.Addr().String(), nil
}

// stop shuts everything down, last started first. It is safe to call twice.
func (r *rig) stop() {
	for i := len(r.stops) - 1; i >= 0; i-- {
		r.stops[i]()
	}
	r.stops = nil
}

// target is where one phase sends its calls.
type target struct {
	url     string // full URL of the call
	headers map[string]string
}

func (r *rig) directTarget() target {
	return target{url: r.fake.URL + testkit.PathFor(r.cfg.fixture), headers: map[string]string{testkit.FixtureHeader: r.cfg.fixture}}
}

func (r *rig) proxyTarget() target {
	provider := "openai"
	if strings.HasPrefix(r.cfg.fixture, "anthropic/") {
		provider = "anthropic"
	}
	return target{url: r.proxyURL + "/" + provider + testkit.PathFor(r.cfg.fixture), headers: map[string]string{
		testkit.FixtureHeader: r.cfg.fixture,
		"x-upstream-url":      r.fake.URL,
	}}
}

// drops closes the proxy (flushing the sink) and reads /statusz from its
// admin handler, in process.
func (r *rig) drops(ctx context.Context) (dropCounters, error) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.proxy.Close(cctx); err != nil {
		return dropCounters{}, fmt.Errorf("close proxy: %w", err)
	}
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "/statusz", nil)
	if err != nil {
		return dropCounters{}, err
	}
	w := httptest.NewRecorder()
	r.proxy.AdminHandler().ServeHTTP(w, req)
	return decodeStatusz(w.Body)
}

func decodeStatusz(body io.Reader) (dropCounters, error) {
	var doc struct {
		Calls   struct{ Total int64 } `json:"calls"`
		Metrics struct {
			Dropped    int64 `json:"dropped"`
			SinkErrors int64 `json:"sink_errors"`
			Written    int64 `json:"written"`
		} `json:"metrics"`
		Hooks struct {
			DroppedEvents int64 `json:"dropped_events"`
		} `json:"hooks"`
		Sessions *struct {
			ClosedDropped int64 `json:"closed_dropped"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		return dropCounters{}, errors.Join(errors.New("statusz: bad body"), err)
	}
	d := dropCounters{Calls: doc.Calls.Total, MetricsDropped: doc.Metrics.Dropped, SinkErrors: doc.Metrics.SinkErrors,
		MetricsWritten: doc.Metrics.Written, EventsDropped: doc.Hooks.DroppedEvents}
	if doc.Sessions != nil {
		d.SessionsClosedDropped = doc.Sessions.ClosedDropped
	}
	return d, nil
}
