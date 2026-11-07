package tollgate

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// hookStats counts what the hook machinery had to give up on, so a skip is
// never silent (STR-8). A later milestone exports them as metrics.
type hookStats struct {
	unbuffered    atomic.Int64 // stream:false responses passed without OnResponse
	droppedFrames atomic.Int64 // SSE frames that never reached OnEvent
}

// newChain snapshots the registered hooks into the chain for one call.
func (p *Proxy) newChain() *chain.Chain {
	l := p.opts.Limits
	return chain.New(p.snapshotHooks(), p.opts.Logger, chain.Limits{Hook: l.HookBeforeDeadline, Total: l.HookChainDeadline})
}

// serveHooked forwards one call with hooks around it (ARCHITECTURE.md
// section 4): parse the request once, Before, then either relay a streamed
// response while a consumer feeds OnEvent, or hold a stream:false response for
// OnResponse, and finally After. A request that does not parse skips the
// hooks and passes through untouched.
func (p *Proxy) serveHooked(w http.ResponseWriter, r *http.Request, o *outcome, call *Call, req forward.Request, chn *chain.Chain) {
	wire, ok := p.parseCall(r.Context(), call)
	if !ok {
		p.forward(w, r, o, req)
		return
	}
	obs := newObserver(p, chn, call, o, r, w, wire)
	defer obs.complete()
	if resp := chn.Before(r.Context(), call); resp != nil {
		obs.shortCircuit(resp)
		return
	}
	req.Header, req.Body = call.Header, call.Body
	var held *forward.Capture
	body := http.ResponseWriter(obs.timed)
	switch {
	case wire == nil: // pass-through wire: Before and After only
	case call.Stream:
		req.Frames = obs.startEvents()
	default:
		held = forward.NewCapture(obs.timed, p.opts.Limits.MaxRequestBody, obs.skipped)
		body = held
	}
	res := p.relay(obs.timed, body, r, o, req)
	if held != nil {
		obs.finishHeld(r.Context(), held, res)
	}
	abortIfCut(res)
}

// parseCall fills the Call from the request body, once. wire is nil for a
// pass-through wire (no parsing); ok is false when the body does not parse.
func (p *Proxy) parseCall(ctx context.Context, call *Call) (wire normalise.Wire, ok bool) {
	wire, err := normalise.ForWire(call.Provider.Wire)
	if err != nil {
		return nil, true
	}
	if err := wire.ParseRequest(call.Body, call); err != nil {
		p.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "request not parsed; hooks skipped",
			slog.String("call_id", call.ID), slog.String("tenant", call.Tenant.ID))
		return nil, false
	}
	return wire, true
}
