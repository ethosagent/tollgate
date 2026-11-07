package tollgate

// Test-only views of the proxy's hook counters, until a metrics milestone
// exposes them.

// UnbufferedResponses is how many stream:false responses bypassed the
// response hooks (over the buffer cap, or encoded).
func (p *Proxy) UnbufferedResponses() int64 { return p.stats.unbuffered.Load() }

// DroppedEvents is how many SSE frames never reached the OnEvent hooks.
func (p *Proxy) DroppedEvents() int64 { return p.stats.droppedFrames.Load() }

// SetMetricOut connects f where milestone 006 will connect the metrics
// queue. Call it before serving.
func (p *Proxy) SetMetricOut(f func(Metric)) { p.metricOut = f }
