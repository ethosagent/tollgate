package tollgate

import (
	"context"
	"errors"
	"net/http"

	"github.com/ethosagent/tollgate/internal/metrics"
)

// initMetrics prepares the metric queue when the host configured a sink. With
// the default DiscardSink nothing is prepared and calls do no metric work
// until AdminHandler is used. The queue's goroutine starts with the first
// metric (ensureQueue), so New needs no context and a proxy that never serves
// starts nothing.
func (p *Proxy) initMetrics() {
	switch p.opts.Metrics.(type) {
	case DiscardSink, *DiscardSink:
		return
	}
	l := p.opts.Limits
	p.qcfg = &metrics.Config{Sink: p.opts.Metrics, Queue: l.MetricQueue, Batch: l.MetricBatch, Interval: l.MetricFlushInterval}
	p.enableRegistry()
}

// ensureQueue returns the metric queue, starting it on first use. It returns
// nil without a sink or after Close.
func (p *Proxy) ensureQueue(ctx context.Context) *metrics.Queue {
	if q := p.queue.Load(); q != nil || p.qcfg == nil {
		return q
	}
	p.qmu.Lock()
	defer p.qmu.Unlock()
	if q := p.queue.Load(); q != nil || p.qclosed {
		return q
	}
	q := metrics.New(context.WithoutCancel(ctx), *p.qcfg)
	p.queue.Store(q)
	return q
}

// enableRegistry starts counting calls for the admin endpoints and connects
// metricOut. It is idempotent and safe while serving: calls already running
// keep the choice they made at their start.
func (p *Proxy) enableRegistry() {
	if !p.reg.CompareAndSwap(nil, metrics.NewRegistry()) {
		return
	}
	out := p.offer
	p.metricOut.Store(&out)
}

// offer is metricOut: queue the call for the sink, then count it. Neither
// blocks. The order means a count includes only calls already queued.
func (p *Proxy) offer(ctx context.Context, m Metric) {
	if q := p.ensureQueue(ctx); q != nil {
		q.Offer(m)
	} else if p.qcfg != nil {
		p.lateDrops.Add(1) // after Close
	}
	if r := p.reg.Load(); r != nil {
		r.Observe(m)
	}
}

// Close stops the session sweeper and the OnSessionClosed goroutine, flushes
// the metric queue to the sink and stops its goroutine. Call it
// after the HTTP server has shut down so every call's Metric is queued. If ctx
// ends first, Close returns its error and whatever the sink has not accepted
// is lost (and counted as dropped). Without a sink it does nothing. It is
// safe to call more than once; calls handled afterwards drop their metrics.
func (p *Proxy) Close(ctx context.Context) error {
	sessErr := p.closeSessions(ctx)
	p.qmu.Lock()
	p.qclosed = true
	q := p.queue.Load()
	p.qmu.Unlock()
	if q == nil {
		return sessErr
	}
	return errors.Join(sessErr, q.Close(ctx))
}

// serveReadyz answers 503 while the metric sink is failing (ARCHITECTURE.md
// section 7), so an orchestrator stops sending new traffic to a node that
// cannot record it; /healthz stays 200.
func (p *Proxy) serveReadyz(w http.ResponseWriter, _ *http.Request) {
	if q := p.queue.Load(); q != nil && !q.Stats().Ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "metric sink unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// AdminHandler returns the operational endpoints:
//
//	GET /statusz   JSON: in-flight calls, queue depth, drops, sink errors,
//	               per-provider call counts by finish, hook skip counts
//	GET /metrics   Prometheus text exposition (version 0.0.4)
//
// They are not on Handler: bind this handler to a private listener, never the
// one clients use. Using it turns on per-call counting (a sink does too); a
// proxy with neither does no metric work. Label values are bounded (provider,
// finish, status class). It returns the same handler on every call and is
// safe for concurrent use.
func (p *Proxy) AdminHandler() http.Handler {
	p.admin.Do(func() {
		p.enableRegistry()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /statusz", p.serveStatusz)
		mux.HandleFunc("GET /metrics", p.serveMetrics)
		p.adminH = mux
	})
	return p.adminH
}

// report gathers everything the admin endpoints expose.
func (p *Proxy) report() metrics.Report {
	r := metrics.Report{InFlight: p.inflight.Load(),
		Hooks: metrics.HookCounts{UnbufferedResponses: p.stats.unbuffered.Load(), DroppedEvents: p.stats.droppedFrames.Load()}}
	if reg := p.reg.Load(); reg != nil {
		r.Snapshot = reg.Snapshot()
	}
	if p.qcfg != nil {
		r.Queue, r.HasQueue = metrics.Stats{Ready: true}, true
		if q := p.queue.Load(); q != nil {
			r.Queue = q.Stats()
		}
		r.Queue.Dropped += p.lateDrops.Load()
	}
	return r
}

func (p *Proxy) serveMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_ = metrics.WriteProm(w, p.report()) // a failed write means the scraper left
}

func (p *Proxy) serveStatusz(w http.ResponseWriter, _ *http.Request) {
	d := statuszOf(p.report())
	d.Sessions = p.sessionStatus()
	writeJSON(w, http.StatusOK, d)
}

type statuszProvider struct {
	Calls  int64            `json:"calls"`
	Finish map[string]int64 `json:"finish"`
}

type statuszDoc struct {
	InFlight int64 `json:"in_flight"`
	Calls    struct {
		Total      int64                      `json:"total"`
		ByProvider map[string]statuszProvider `json:"by_provider"`
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
	} `json:"hooks"`
	// Sessions is absent when the host supplied its own SessionStrategy.
	Sessions *statuszSessions `json:"sessions,omitempty"`
}

func statuszOf(r metrics.Report) statuszDoc {
	var d statuszDoc
	d.InFlight = r.InFlight
	d.Calls.ByProvider = map[string]statuszProvider{}
	for _, c := range r.Snapshot.Calls {
		pr, ok := d.Calls.ByProvider[c.Provider]
		if !ok {
			pr.Finish = map[string]int64{}
		}
		pr.Calls += c.N
		pr.Finish[string(c.Finish)] += c.N
		d.Calls.ByProvider[c.Provider] = pr
		d.Calls.Total += c.N
	}
	d.Metrics.Enabled = r.HasQueue
	d.Metrics.SinkReady = true
	if r.HasQueue {
		q := r.Queue
		d.Metrics.QueueDepth, d.Metrics.Dropped, d.Metrics.SinkErrors, d.Metrics.Written, d.Metrics.SinkReady = q.Depth, q.Dropped, q.SinkErrors, q.Written, q.Ready
	}
	d.Hooks.UnbufferedResponses, d.Hooks.DroppedEvents = r.Hooks.UnbufferedResponses, r.Hooks.DroppedEvents
	return d
}
