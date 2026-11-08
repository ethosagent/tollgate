package tollgate

import (
	"context"
	"net/http"

	"github.com/ethosagent/tollgate/internal/core"
)

// The Proxy here is the public face of internal/core's engine: every method
// delegates (ADR 0016). Keep the doc comments here; they are what hosts read.

// Proxy is an LLM proxy. Create it with New, register hooks with Use and
// serve Handler. It is safe for concurrent use.
type Proxy struct {
	core *core.Proxy
}

// ErrClosed is returned by Start after Close.
var ErrClosed = core.ErrClosed

// New validates opts, applies defaults and returns a Proxy.
func New(opts Options) (*Proxy, error) {
	c, err := core.New(opts.core())
	if err != nil {
		return nil, err
	}
	return &Proxy{core: c}, nil
}

// Use appends hooks to the ordered hook chain. A nil hook is ignored. Hooks
// added after serving starts apply to calls that start afterwards. Which Hook
// methods each hook overrides (rather than inherits from an embedded
// NoopHook) is detected here, once, so stages no hook implements cost nothing.
func (p *Proxy) Use(hooks ...Hook) { p.core.Use(hooks...) }

// Handler returns the HTTP handler: health endpoints plus the proxy.
//
//	GET /healthz   200 while the process is alive
//	GET /readyz    200 when ready to take traffic; 503 while a configured
//	               metric sink is not started (Start) or is failing
//	ANY /{provider}/...  the proxy pipeline
//
// It returns the same handler on every call.
func (p *Proxy) Handler() http.Handler { return p.core.Handler() }

// Start starts the Proxy's background work: the metric writer that batches
// every call's Metric to Options.Metrics. Call it once before serving and
// Close when done; with the default DiscardSink it does nothing. Until it
// runs, metrics wait in the bounded queue (overflow drops and counts) and
// /readyz answers 503, so a host that forgets it is told. ctx's values reach
// every MetricSink.Write; its cancellation does not stop the writer (Close
// does, after flushing), so a host may pass its signal context. Further
// calls do nothing; after Close it returns ErrClosed.
func (p *Proxy) Start(ctx context.Context) error { return p.core.Start(ctx) }

// Close stops the session sweeper and the OnSessionClosed goroutine, flushes
// the metric queue to the sink and stops its writer (a proxy that was never
// started still flushes). Call it after the HTTP server has shut down so
// every call's Metric is queued. If ctx ends first, Close returns its error
// and whatever the sink has not accepted is lost (and counted as dropped).
// It is safe to call more than once; calls handled afterwards drop their
// metrics, and Start returns ErrClosed.
func (p *Proxy) Close(ctx context.Context) error { return p.core.Close(ctx) }

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
func (p *Proxy) AdminHandler() http.Handler { return p.core.AdminHandler() }
