package tollgate

import (
	"context"
	"errors"
	"net/http"

	"github.com/ethosagent/tollgate/internal/metrics"
)

// initMetrics creates the metric queue when the host configured a sink.
// With the default DiscardSink nothing is created and calls do no metric
// work until AdminHandler is used. The queue's writer starts in Start, so
// New starts no goroutine.
func (p *Proxy) initMetrics() {
	switch p.opts.Metrics.(type) {
	case DiscardSink, *DiscardSink:
		return
	}
	l := p.opts.Limits
	p.queue = metrics.New(metrics.Config{Sink: p.opts.Metrics, Queue: l.MetricQueue, Batch: l.MetricBatch,
		Interval: l.MetricFlushInterval, WriteTimeout: l.MetricWriteTimeout, FailAfter: l.MetricFailAfter})
	p.enableRegistry()
}

// ErrClosed is returned by Start after Close.
var ErrClosed = errors.New("tollgate: proxy is closed")

// Start starts the Proxy's background work: the metric writer that batches
// every call's Metric to Options.Metrics. Call it once before serving and
// Close when done; with the default DiscardSink it does nothing. Until it
// runs, metrics wait in the bounded queue (overflow drops and counts) and
// /readyz answers 503, so a host that forgets it is told. ctx's values reach
// every MetricSink.Write; its cancellation does not stop the writer (Close
// does, after flushing), so a host may pass its signal context. Further
// calls do nothing; after Close it returns ErrClosed.
func (p *Proxy) Start(ctx context.Context) error {
	if p.closed.Load() {
		return ErrClosed
	}
	if p.queue != nil {
		p.queue.Start(ctx)
	}
	return nil
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
// blocks; after Close the queue drops and counts. The order means a count
// includes only calls already queued.
func (p *Proxy) offer(m Metric) {
	if p.queue != nil {
		p.queue.Offer(m)
	}
	if r := p.reg.Load(); r != nil {
		r.Observe(m)
	}
}

// Close stops the session sweeper and the OnSessionClosed goroutine, flushes
// the metric queue to the sink and stops its writer (a proxy that was never
// started still flushes). Call it after the HTTP server has shut down so
// every call's Metric is queued. If ctx ends first, Close returns its error
// and whatever the sink has not accepted is lost (and counted as dropped).
// It is safe to call more than once; calls handled afterwards drop their
// metrics, and Start returns ErrClosed.
func (p *Proxy) Close(ctx context.Context) error {
	p.closed.Store(true)
	sessErr := p.closeSessions(ctx)
	if p.queue == nil {
		return sessErr
	}
	return errors.Join(sessErr, p.queue.Close(ctx))
}

// serveReadyz answers 503 while a configured metric sink cannot record
// calls: its writer was never started, or the sink has failed
// Limits.MetricFailAfter times in a row (ARCHITECTURE.md section 7). An
// orchestrator then stops sending new traffic; /healthz stays 200.
func (p *Proxy) serveReadyz(w http.ResponseWriter, _ *http.Request) {
	if p.queue != nil {
		switch st := p.queue.Stats(); {
		case !st.Started:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "metric writer not started"})
			return
		case !st.Ready:
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "metric sink unavailable"})
			return
		}
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
	if p.queue != nil {
		st := p.queue.Stats()
		r.Queue = &st
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
	d.Metrics.Enabled = r.Queue != nil
	d.Metrics.SinkReady = true
	if q := r.Queue; q != nil {
		d.Metrics.QueueDepth, d.Metrics.Dropped, d.Metrics.SinkErrors, d.Metrics.Written, d.Metrics.SinkReady = q.Depth, q.Dropped, q.SinkErrors, q.Written, q.Ready
	}
	d.Hooks.UnbufferedResponses, d.Hooks.DroppedEvents = r.Hooks.UnbufferedResponses, r.Hooks.DroppedEvents
	return d
}
