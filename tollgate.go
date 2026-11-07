package tollgate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/ethosagent/tollgate/internal/forward"
)

// Proxy is an LLM proxy. Create it with New, register hooks with Use and
// serve Handler. It is safe for concurrent use.
type Proxy struct {
	opts    Options
	handler http.Handler // built once in New

	mu    sync.RWMutex
	hooks []Hook
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
	p := &Proxy{opts: opts}
	p.handler = p.buildHandler()
	return p, nil
}

// Use appends hooks to the ordered hook chain. A nil hook is ignored. Hooks
// added after serving starts apply to calls that start afterwards.
func (p *Proxy) Use(hooks ...Hook) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range hooks {
		if h != nil {
			p.hooks = append(p.hooks, h)
		}
	}
}

// snapshotHooks returns the current chain; callers must not modify it. The
// full slice expression caps capacity, so a concurrent Use appends to a new
// array instead of writing into the one a caller is reading.
func (p *Proxy) snapshotHooks() []Hook {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.hooks[:len(p.hooks):len(p.hooks)]
}

// Handler returns the HTTP handler: health endpoints plus the proxy.
//
//	GET /healthz   200 while the process is alive
//	GET /readyz    200 when ready to take traffic
//	ANY /{provider}/...  the proxy pipeline
//
// It returns the same handler on every call.
func (p *Proxy) Handler() http.Handler { return p.handler }

func (p *Proxy) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("/", p.serveProxy)
	return mux
}

// serveProxy is the proxy pipeline entry point. The pipeline (Auth →
// Admission → ProviderResolver → normalise → Session → Hook.Before →
// Transport → tee → Finalizer → After → MetricSink) is not built yet; until
// it is, every proxied request gets a provider-shaped 501.
func (p *Proxy) serveProxy(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "not_implemented", "the tollgate proxy pipeline is not implemented yet")
}

// writeError writes an OpenAI-shaped error body, which OpenAI-compatible
// clients parse. Provider-specific shapes arrive with internal/normalise.
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"type": typ, "message": msg},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the status line is already sent; nothing to recover
}
