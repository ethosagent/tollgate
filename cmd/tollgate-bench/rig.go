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
	"sync"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/metrics/stdout"
	"github.com/ethosagent/tollgate/testkit"
)

// callHeader carries the id that ties a client call to its upstream record.
// tollgate forwards it untouched, like any header it does not own.
const callHeader = "x-bench-call"

// rig is the system under measurement: a fake upstream, a tollgate in front
// of it with a real metric sink (its work is part of the cost), and the time
// the proxy's listener handed each call to tollgate.
type rig struct {
	fake     upstream
	proxy    *tollgate.Proxy
	proxyURL string
	arrived  sync.Map // call id -> time.Time the proxy's handler was entered
	stops    []func()
}

// benchAuth admits every call as one tenant that may use the upstream
// override header to reach loopback, which is where the fake listens.
type benchAuth struct{}

func (benchAuth) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	return tollgate.Tenant{ID: "bench", Policy: &tollgate.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}, nil
}

// newRig starts a fake upstream with the given chunk delay (or uses the
// -upstream one) and a proxy for cfg.sessions concurrent streams.
func newRig(ctx context.Context, cfg config, chunkDelay time.Duration) (*rig, error) {
	r := &rig{}
	sessions := cfg.sessions
	if cfg.upstream != "" { // its chunk delay is the one it was started with
		fake, err := dialRemoteUpstream(cfg.upstream)
		if err != nil {
			return nil, err
		}
		r.fake = fake
	} else {
		r.fake = newLocalUpstream(chunkDelay)
	}
	r.stops = append(r.stops, r.fake.stop)
	// The default MaxConnsPerHost (100) would queue calls and measure the cap,
	// not the proxy: give every session its own upstream connection.
	p, err := tollgate.New(tollgate.Options{
		Auth:    benchAuth{},
		Metrics: stdout.New(io.Discard),
		Limits:  tollgate.Limits{MaxConnsPerHost: 2 * sessions, MaxIdleConnsPerHost: sessions},
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
	r.stops = append(r.stops, func() { _ = p.Close(context.WithoutCancel(ctx)) }) // after drops, a second Close is a no-op
	h := p.Handler()
	if cfg.nullProxy {
		np := newNullProxy(sessions, r.fake.url()+testkit.PathFor(cfg.fixture))
		r.stops = append(r.stops, np.rt.CloseIdleConnections)
		h = np
	}
	if r.proxyURL, err = r.listen(ctx, r.stamp(h)); err != nil {
		r.stop()
		return nil, err
	}
	return r, nil
}

// stamp records when each call reached tollgate's handler.
func (r *rig) stamp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if id := req.Header.Get(callHeader); id != "" {
			r.arrived.Store(id, time.Now())
		}
		next.ServeHTTP(w, req)
	})
}

// listen serves h on a loopback port and returns its base URL. A plain
// http.Server rather than httptest, for its ReadHeaderTimeout.
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

// target is where a call is sent.
type target struct {
	url     string // full URL of the call
	headers map[string]string
}

func (r *rig) directTarget(fixture string) target {
	return target{url: r.fake.url() + testkit.PathFor(fixture), headers: map[string]string{testkit.FixtureHeader: fixture}}
}

func (r *rig) proxyTarget(fixture string) target {
	provider, _ := testkit.RequestFor(fixture)
	return target{url: r.proxyURL + "/" + provider + testkit.PathFor(fixture), headers: map[string]string{
		testkit.FixtureHeader: fixture,
		"x-upstream-url":      r.fake.url(),
	}}
}

// upstreamSide returns, by call id, what the fake recorded for each call.
func (r *rig) upstreamSide(ctx context.Context) (map[string]testkit.Request, error) {
	return r.fake.records(ctx)
}

// proxyArrival is when the proxy's handler got call id.
func (r *rig) proxyArrival(id string) (time.Time, bool) {
	v, ok := r.arrived.Load(id)
	if !ok {
		return time.Time{}, false
	}
	return v.(time.Time), true
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
