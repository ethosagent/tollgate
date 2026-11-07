package tollgate

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/metrics"
)

// Proxy is an LLM proxy. Create it with New, register hooks with Use and
// serve Handler. It is safe for concurrent use.
type Proxy struct {
	opts    Options
	handler http.Handler // built once in New

	reqHeaders forward.HeaderFilter // strips hop-by-hop, tollgate's own and extension-consumed headers
	idPrefix   string               // random per Proxy; call IDs are idPrefix + counter
	callSeq    atomic.Uint64

	useMu sync.Mutex                  // serialises Use; readers never lock
	chain atomic.Pointer[chain.Chain] // the hooks a new call runs: one atomic load per call

	// fin holds Options.Finalizers under their Limits; empty means no
	// terminal event is ever held (finalize.go).
	fin *chain.Finalizers

	// metricOut receives every call's Metric once After has run (record.go).
	// admin.go sets it (to offer) only when a sink is configured or the admin
	// handler is used; while it is nil and no hook needs it, a call skips
	// parsing and usage.
	metricOut atomic.Pointer[func(context.Context, Metric)]
	qcfg      *metrics.Config // nil without a sink; set in New, never changed
	qmu       sync.Mutex      // guards starting and closing the queue
	qclosed   bool
	queue     atomic.Pointer[metrics.Queue] // started by the first call's metric (admin.go)
	lateDrops atomic.Int64                  // metrics offered after Close
	reg       atomic.Pointer[metrics.Registry]
	admin     sync.Once
	adminH    http.Handler
	inflight  atomic.Int64 // calls inside serveProxy

	stats    hookStats
	sessions sessionState
}

// New validates opts, applies defaults and returns a Proxy.
func New(opts Options) (*Proxy, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("tollgate: invalid options: %w", err)
	}
	opts = opts.withDefaults()
	if opts.Transport == nil {
		opts.Transport = forward.NewTransport(forward.TransportConfig{
			DialTimeout:         opts.Limits.ConnectTimeout,
			FirstByteTimeout:    opts.Limits.FirstByteTimeout,
			MaxConnsPerHost:     opts.Limits.MaxConnsPerHost,
			MaxIdleConnsPerHost: opts.Limits.MaxIdleConnsPerHost,
			IdleConnTimeout:     opts.Limits.IdleConnTimeout,
		})
	}
	h := opts.Headers
	p := &Proxy{
		opts:       opts,
		reqHeaders: forward.NewHeaderFilter(append([]string{h.TenantKey, h.Session, h.UpstreamURL}, opts.consumedHeaders()...)...),
		idPrefix:   rand.Text()[:12] + "-",
	}
	p.fin = newFinalizers(opts)
	p.chain.Store(chain.New(nil, opts.Logger, chain.Limits{Hook: opts.Limits.HookBeforeDeadline, Total: opts.Limits.HookChainDeadline}))
	p.initSessions()
	p.handler = p.buildHandler()
	p.initMetrics()
	return p, nil
}

// Use appends hooks to the ordered hook chain. A nil hook is ignored. Hooks
// added after serving starts apply to calls that start afterwards. Which Hook
// methods each hook overrides (rather than inherits from an embedded
// NoopHook) is detected here, once, so stages no hook implements cost nothing.
func (p *Proxy) Use(hooks ...Hook) {
	p.useMu.Lock()
	defer p.useMu.Unlock()
	p.chain.Store(p.chain.Load().With(hooks...))
	if p.chain.Load().Has(chain.StageSessionClosed) {
		p.startSessionConsumer()
	}
}

// Handler returns the HTTP handler: health endpoints plus the proxy.
//
//	GET /healthz   200 while the process is alive
//	GET /readyz    200 when ready to take traffic; 503 while the metric sink is failing
//	ANY /{provider}/...  the proxy pipeline
//
// It returns the same handler on every call.
func (p *Proxy) Handler() http.Handler { return p.handler }

func (p *Proxy) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", p.serveReadyz)
	mux.HandleFunc("/", p.serveProxy)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status line is already sent; nothing to recover
}
