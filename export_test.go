package tollgate

import "context"

// Test-only views of the proxy's hook counters, until a metrics milestone
// exposes them.

// UnbufferedResponses is how many stream:false responses bypassed the
// response hooks (over the buffer cap, or encoded).
func (p *Proxy) UnbufferedResponses() int64 { return p.stats.unbuffered.Load() }

// DroppedEvents is how many SSE frames never reached the OnEvent hooks.
func (p *Proxy) DroppedEvents() int64 { return p.stats.droppedFrames.Load() }

// SetMetricOut connects f where milestone 006 will connect the metrics
// queue. Call it before serving.
func (p *Proxy) SetMetricOut(f func(Metric)) {
	out := func(_ context.Context, m Metric) { f(m) }
	p.metricOut.Store(&out)
}

// QueuedMetrics is how many metrics wait for the sink.
func (p *Proxy) QueuedMetrics() int {
	if q := p.queue.Load(); q != nil {
		return q.Stats().Depth
	}
	return 0
}

// DroppedMetrics is how many metrics the queue dropped.
func (p *Proxy) DroppedMetrics() int64 {
	if q := p.queue.Load(); q != nil {
		return q.Stats().Dropped + p.lateDrops.Load()
	}
	return p.lateDrops.Load()
}

// MetricsWired reports whether calls emit Metrics at all.
func (p *Proxy) MetricsWired() bool { return p.metricOut.Load() != nil }
