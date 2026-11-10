package core

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// hookStats counts what the hook machinery had to give up on, so a skip is
// never silent (STR-8). A later milestone exports them as metrics.
type hookStats struct {
	unbuffered    atomic.Int64 // stream:false responses passed without OnResponse
	droppedFrames atomic.Int64 // SSE frames that never reached the consumer
	unparsed      atomic.Int64 // SSE frames the mapper could not parse
	usageNotRead  atomic.Int64 // stream:false responses whose usage copy was given up
	unheld        atomic.Int64 // streams whose terminal region was too large to hold for Decide
}

// serveCall forwards one admitted call (ARCHITECTURE.md section 4). When
// nothing needs the parsed call (no hook stage, no usage reader) the body is
// relayed without parsing, a consumer or a goroutine. Otherwise the request
// is parsed once, Before runs, and the response is either streamed while a
// consumer (chain.Tap) feeds OnEvent and collects usage, or, stream:false,
// held for OnResponse and usage. A request that does not parse skips the
// hooks and passes through untouched; it still gets its Metric.
func (p *Proxy) serveCall(r *http.Request, o *outcome, req forward.Request) {
	wire, hooked := p.parseCall(r.Context(), o)
	if hooked {
		if resp, by := o.chn.BeforeNamed(r.Context(), o.call); resp != nil {
			o.short, o.shortBy = true, by
			p.shortCircuit(o, resp)
			return
		}
		req.Header, req.Body = o.call.Header, o.call.Body
	}
	var held *forward.Capture
	var cp *forward.BodyCopy
	body := http.ResponseWriter(o.w)
	switch {
	case wire == nil: // not parsed, or a pass-through wire: Before and After only
	case o.call.Stream:
		req.Final = p.finalize(o, wire)
		if o.chn.Has(chain.StageEvent) || p.wantsUsage(o.chn) {
			req.Frames = forward.NewFrameQueue(p.opts.Limits.EventBuffer)
			o.tap = o.chn.StartTap(context.WithoutCancel(r.Context()), o.call, req.Frames, wire.NewEventMapper())
		}
	case o.chn.Has(chain.StageResponse): // OnResponse may replace the body: hold it
		held = forward.NewCapture(o.w, p.opts.Limits.MaxRequestBody, heldSkip{p, o}.skipped)
		body = held
	case p.wantsUsage(o.chn): // usage only: the client is not made to wait for it
		cp = forward.NewBodyCopy(o.w, p.opts.Limits.MaxResponseCopy)
		body = cp
	}
	res := p.relay(body, r, o, req)
	if held != nil {
		p.finishHeld(r.Context(), o, wire, held, res)
	}
	if cp != nil {
		p.copiedUsage(o, wire, cp, res)
	}
	abortIfCut(res)
}

// copiedUsage reads usage from the copy of a stream:false response that
// already went to the client unheld. A broken, failed, encoded or oversized
// response has no usage.
func (p *Proxy) copiedUsage(o *outcome, wire normalise.Wire, cp *forward.BodyCopy, res forward.Result) {
	body, status, ok := cp.Held()
	if cp.Skipped() != "" {
		p.stats.usageNotRead.Add(1)
	}
	if !ok || res.Cut || status >= http.StatusBadRequest {
		return
	}
	if evs, err := wire.MapBody(body); err == nil {
		normalise.TrackUsage(&o.usage, evs)
	}
}

// parseCall fills the Call from the request body, once, when something will
// read it. hooked says the hook stages run; wire is nil when the call is not
// parsed (nobody needs it, a pass-through wire, or a body that does not parse).
func (p *Proxy) parseCall(ctx context.Context, o *outcome) (wire normalise.Wire, hooked bool) {
	c := o.chn
	if !c.Has(chain.StageBefore) && !c.Has(chain.StageEvent) && !c.Has(chain.StageResponse) &&
		!c.Has(chain.StageSessionClosed) && !p.wantsUsage(c) && p.fin.Empty() {
		return nil, false // the zero-hook fast path
	}
	wire, hooked = p.parseBody(ctx, o)
	p.attributeSession(ctx, o)
	return wire, hooked
}

// parseBody parses the request body into the Call.
func (p *Proxy) parseBody(ctx context.Context, o *outcome) (wire normalise.Wire, hooked bool) {
	wire, err := normalise.ForWire(o.call.Provider.Wire)
	if err != nil {
		return nil, true
	}
	if err := wire.ParseRequest(o.call.Body, o.call); err != nil {
		p.opts.Logger.LogAttrs(ctx, slog.LevelWarn, "request not parsed; hooks skipped",
			slog.String("call_id", o.callID), slog.String("tenant", o.tenant))
		return nil, false
	}
	return wire, true
}

// shortCircuit answers with a Before hook's Response; the upstream is not called.
func (p *Proxy) shortCircuit(o *outcome, resp *model.Response) {
	o.status = forward.ResponseStatus(resp.Status)
	if o.status >= http.StatusBadRequest {
		o.finish = model.FinishRejected
	}
	forward.WriteResponse(o.w, resp)
}

// finishHeld ends a stream:false call: a complete response goes through the
// OnResponse chain, a broken one is released as it is (the caller then aborts
// the connection), and one that was passed through needs nothing.
func (p *Proxy) finishHeld(ctx context.Context, o *outcome, wire normalise.Wire, held *forward.Capture, res forward.Result) {
	if !held.Started() || held.Passed() {
		return
	}
	if res.Cut || res.Finish == model.FinishClientCancel {
		// The client left or must see the cut; a failed write changes nothing.
		// The flush matters: the abort that follows discards unsent bytes.
		_ = held.Release()
		_ = http.NewResponseController(o.w).Flush()
		return
	}
	resp := held.Response()
	if resp.Status < http.StatusBadRequest {
		if evs, err := wire.MapBody(resp.Body); err == nil {
			normalise.TrackUsage(&o.usage, evs)
		}
	}
	out := o.chn.OnResponse(ctx, o.call, resp)
	o.status = forward.ResponseStatus(out.Status)
	forward.WriteResponse(o.w, out)
}

// heldSkip is the Capture's skip callback for one call.
type heldSkip struct {
	p *Proxy
	o *outcome
}

// skipped records a stream:false response that went to the client without
// OnResponse (over the buffer cap, or encoded).
func (s heldSkip) skipped(reason string) {
	p, o := s.p, s.o
	p.stats.unbuffered.Add(1)
	p.opts.Logger.LogAttrs(context.Background(), slog.LevelWarn, "response not held for hooks",
		slog.String("reason", reason), slog.Int64("cap", p.opts.Limits.MaxRequestBody),
		slog.String("call_id", o.callID), slog.String("tenant", o.tenant))
}
