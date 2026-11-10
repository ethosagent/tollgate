package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/model"
)

// metrics collects what the proxy emits at the metric sink seam.
type metrics struct {
	mu sync.Mutex
	ms []model.Metric
}

func (r *metrics) add(m model.Metric) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ms = append(r.ms, m)
}

func (r *metrics) all() []model.Metric {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.Metric(nil), r.ms...)
}

func metricProxy(t *testing.T, opts core.Options, hooks ...model.Hook) (string, *metrics, *core.Proxy) {
	t.Helper()
	if opts.Auth == nil {
		opts.Auth = allowLocal("acme") // lets the override reach the fake upstream
	}
	p, err := core.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	rec := &metrics{}
	p.SetMetricOut(rec.add)
	p.Use(hooks...)
	return serve(t, p), rec, p
}

var shortCircuit = &tHook{name: "gate", before: func(context.Context, *model.Call) (*model.Response, error) {
	return &model.Response{Status: 418, Body: []byte("no")}, nil
}}

// TestProxy_OneMetricPerCall is the seam milestone 006 builds on: every call,
// whatever path it takes, produces exactly one Metric with its outcome.
func TestProxy_OneMetricPerCall(t *testing.T) {
	tests := []struct {
		name    string
		opts    core.Options
		up      upstreamOptions
		hooks   []model.Hook
		body    string // "" is the streamed fixture request
		cancel  bool
		status  int
		finish  model.Finish
		tenant  string
		parsed  bool // provider, model and usage are known
		touched bool // the upstream saw the request
	}{
		{name: "ok stream", status: 200, finish: model.FinishOK, tenant: "acme", parsed: true, touched: true},
		{name: "ok stream:false", body: plainRequest, status: 200, finish: model.FinishOK, tenant: "acme", parsed: true, touched: true},
		{name: "auth rejected", opts: core.Options{Auth: failAuth{errors.New("no")}}, status: 401, finish: model.FinishRejected},
		{name: "admission rejected", opts: core.Options{Admission: &countingAdmission{err: errors.New("full")}}, status: 429, finish: model.FinishRejected, tenant: "acme"},
		{name: "upstream error", up: upstreamOptions{Status: 500}, body: plainRequest, status: 500, finish: model.FinishUpstreamError, tenant: "acme", touched: true},
		{name: "timeout", opts: core.Options{Limits: core.Limits{FirstByteTimeout: 50 * time.Millisecond}}, up: upstreamOptions{FirstByteDelay: 300 * time.Millisecond},
			body: plainRequest, status: 504, finish: model.FinishTimeout, tenant: "acme", touched: true},
		{name: "client cancel", up: upstreamOptions{ChunkDelay: 30 * time.Second}, cancel: true, status: 200, finish: model.FinishClientCancel, tenant: "acme", touched: true},
		{name: "short-circuit", hooks: []model.Hook{shortCircuit}, body: plainRequest, status: 418, finish: model.FinishRejected, tenant: "acme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newFakeUpstream(t, tt.up)
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
			if m.Dispatched != tt.touched {
				t.Fatalf("Dispatched = %v, want %v (refusals before the upstream are false)", m.Dispatched, tt.touched)
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
	got := make(chan model.Metric, 1)
	h := &tHook{name: "a", after: func(_ context.Context, c *model.Call, m model.Metric) {
		if c.ID == m.CallID {
			got <- m
		}
	}}
	up := newFakeUpstream(t, upstreamOptions{})
	base, rec, _ := metricProxy(t, core.Options{Auth: failAuth{errors.New("no")}}, h)
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	m := <-got
	eventually(t, "the sink Metric", func() bool { return len(rec.all()) == 1 })
	if m.Status != 401 || m.Finish != model.FinishRejected || rec.all()[0] != m {
		t.Fatalf("After got %+v, sink got %+v; want the same rejected Metric", m, rec.all())
	}
}

// TestProxy_MetricDispatchedSignalsAnUpstreamAttempt: Dispatched separates a
// call refused before the upstream (resolver, here) from one that reached it.
func TestProxy_MetricDispatchedSignalsAnUpstreamAttempt(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base, rec, _ := metricProxy(t, core.Options{})
	_ = readAll(t, post(t, base, "/nope/x", []byte(`{}`), via(up)))
	eventually(t, "the refused call's Metric", func() bool { return len(rec.all()) == 1 })
	if m := rec.all()[0]; m.Dispatched || m.Finish != model.FinishRejected || m.Status != 404 {
		t.Fatalf("resolver refusal metric = %+v, want rejected 404, not dispatched", m)
	}
}

// TestProxy_ShortCircuitReachesAfterWithMetaAndMetric: Meta a short-circuiting
// Before hook wrote reaches After, and the Metric says which hook answered.
// Other calls leave the fields zero.
func TestProxy_ShortCircuitReachesAfterWithMetaAndMetric(t *testing.T) {
	type seen struct {
		meta any
		m    model.Metric
	}
	got := make(chan seen, 2)
	gate := &tHook{name: "loop-monitor", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		if c.Meta == nil {
			c.Meta = map[string]any{}
		}
		c.Meta["monitor.rule"] = "loop"
		return &model.Response{Status: 429, Body: []byte("no")}, nil
	}}
	obs := &tHook{name: "obs", after: func(_ context.Context, c *model.Call, m model.Metric) { got <- seen{c.Meta["monitor.rule"], m} }}
	up := newFakeUpstream(t, upstreamOptions{})
	base, rec, _ := metricProxy(t, core.Options{}, gate, obs)
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	s := <-got
	if s.meta != "loop" || !s.m.ShortCircuited || s.m.ShortCircuitedBy != "loop-monitor" || s.m.Dispatched ||
		s.m.Status != 429 || s.m.Finish != model.FinishRejected {
		t.Fatalf("after saw meta=%v metric=%+v", s.meta, s.m)
	}
	eventually(t, "the sink Metric", func() bool { return len(rec.all()) == 1 })
	if m := rec.all()[0]; m != s.m {
		t.Fatalf("sink %+v != After %+v", m, s.m)
	}
}
