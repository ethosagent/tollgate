package tollgate_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// metrics collects what the proxy emits at the metric sink seam.
type metrics struct {
	mu sync.Mutex
	ms []tollgate.Metric
}

func (r *metrics) add(m tollgate.Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ms = append(r.ms, m)
}

func (r *metrics) all() []tollgate.Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tollgate.Metric(nil), r.ms...)
}

func metricProxy(t *testing.T, opts tollgate.Options, hooks ...tollgate.Hook) (string, *metrics, *tollgate.Proxy) {
	t.Helper()
	if opts.Auth == nil {
		opts.Auth = allowLocal("acme") // lets the override reach the fake upstream
	}
	p, err := tollgate.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	rec := &metrics{}
	p.SetMetricOut(rec.add)
	p.Use(hooks...)
	return serve(t, p), rec, p
}

var shortCircuit = &tHook{name: "gate", before: func(context.Context, *tollgate.Call) (*tollgate.Response, error) {
	return &tollgate.Response{Status: 418, Body: []byte("no")}, nil
}}

// TestProxy_OneMetricPerCall is the seam milestone 006 builds on: every call,
// whatever path it takes, produces exactly one Metric with its outcome.
func TestProxy_OneMetricPerCall(t *testing.T) {
	tests := []struct {
		name    string
		opts    tollgate.Options
		up      testkit.Options
		hooks   []tollgate.Hook
		body    string // "" is the streamed fixture request
		cancel  bool
		status  int
		finish  tollgate.Finish
		tenant  string
		parsed  bool // provider, model and usage are known
		touched bool // the upstream saw the request
	}{
		{name: "ok stream", status: 200, finish: tollgate.FinishOK, tenant: "acme", parsed: true, touched: true},
		{name: "ok stream:false", body: plainRequest, status: 200, finish: tollgate.FinishOK, tenant: "acme", parsed: true, touched: true},
		{name: "auth rejected", opts: tollgate.Options{Auth: failAuth{errors.New("no")}}, status: 401, finish: tollgate.FinishRejected},
		{name: "admission rejected", opts: tollgate.Options{Admission: &countingAdmission{err: errors.New("full")}}, status: 429, finish: tollgate.FinishRejected, tenant: "acme"},
		{name: "upstream error", up: testkit.Options{Status: 500}, body: plainRequest, status: 500, finish: tollgate.FinishUpstreamError, tenant: "acme", touched: true},
		{name: "timeout", opts: tollgate.Options{Limits: tollgate.Limits{FirstByteTimeout: 50 * time.Millisecond}}, up: testkit.Options{FirstByteDelay: 300 * time.Millisecond},
			body: plainRequest, status: 504, finish: tollgate.FinishTimeout, tenant: "acme", touched: true},
		{name: "client cancel", up: testkit.Options{ChunkDelay: 30 * time.Second}, cancel: true, status: 200, finish: tollgate.FinishClientCancel, tenant: "acme", touched: true},
		{name: "short-circuit", hooks: []tollgate.Hook{shortCircuit}, body: plainRequest, status: 418, finish: tollgate.FinishRejected, tenant: "acme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, tt.up)
			base, rec, _ := metricProxy(t, tt.opts, tt.hooks...)
			switch {
			case tt.cancel:
				streamThenCancel(t, base, up)
			case tt.body == "":
				_ = readAll(t, post(t, base, chatPath, requestBody(t, streamFixture), via(up)))
			default:
				_ = readAll(t, post(t, base, chatPath, []byte(tt.body), via(up)))
			}
			eventually(t, "the call's Metric", func() bool { return len(rec.all()) > 0 })
			m := rec.all()[0]
			if m.Status != tt.status || m.Finish != tt.finish || m.TenantID != tt.tenant || m.CallID == "" || m.Start.IsZero() || m.Duration <= 0 {
				t.Fatalf("metric = %+v, want status %d finish %s tenant %q", m, tt.status, tt.finish, tt.tenant)
			}
			if tt.parsed && (m.Provider != "openai" || m.Model == "" || m.Usage.InputTokens == 0 || m.TTFB <= 0) {
				t.Fatalf("metric = %+v, want provider, model, usage and TTFB", m)
			}
			if tt.touched {
				eventually(t, "the upstream request", func() bool { return len(up.Requests()) > 0 })
			} else if len(up.Requests()) != 0 {
				t.Fatal("upstream contacted")
			}
			// A second call's Metric arrives after the first call ended: still
			// exactly one per call id.
			_ = readAll(t, post(t, base, "/nope/x", []byte(`{}`), via(up)))
			eventually(t, "the second call's Metric", func() bool { return len(rec.all()) >= 2 })
			if ms := rec.all(); len(ms) != 2 || ms[0].CallID == ms[1].CallID {
				t.Fatalf("metrics = %+v, want exactly one per call", ms)
			}
		})
	}
}

func TestProxy_AfterSeesTheMetricOfARejectedCall(t *testing.T) {
	got := make(chan tollgate.Metric, 1)
	h := &tHook{name: "a", after: func(_ context.Context, c *tollgate.Call, m tollgate.Metric) {
		if c.ID == m.CallID {
			got <- m
		}
	}}
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	base, rec, _ := metricProxy(t, tollgate.Options{Auth: failAuth{errors.New("no")}}, h)
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	m := <-got
	eventually(t, "the sink Metric", func() bool { return len(rec.all()) == 1 })
	if m.Status != 401 || m.Finish != tollgate.FinishRejected || rec.all()[0] != m {
		t.Fatalf("After got %+v, sink got %+v; want the same rejected Metric", m, rec.all())
	}
}
