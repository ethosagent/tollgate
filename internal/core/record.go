package core

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// outcome is the single per-call record: everything the access log line, the
// Metric and After know about one call. It holds metadata only: never header
// values, bodies or credentials (PRIV-1, PRIV-2). The handler goroutine writes
// it; once finishCall runs it is read-only, except tap, which guards itself.
type outcome struct {
	start      time.Time
	callID     string
	shape      normalise.ErrorShape // error body shape; refined once the provider is known
	tenant     string
	provider   string
	status     int
	finish     model.Finish
	dispatched bool                 // relay ran: the upstream request was attempted
	chn        *chain.Chain         // the hooks of this call: later Use calls do not join it
	call       *model.Call          // never nil: ID and Path at once, the rest as the pipeline learns it
	w          *forward.TimedWriter // the client writer; its first write is the TTFB
	usage      model.Usage          // stream:false usage, from the held body
	tap        *chain.Tap           // stream:true usage (and OnEvent), nil when nobody needs it
	final      *forward.Finalized   // what the Finalizer barrier did; its usage covers every upstream call
}

func (p *Proxy) newOutcome(w http.ResponseWriter, r *http.Request) *outcome {
	id := p.nextCallID()
	return &outcome{start: time.Now(), callID: id, finish: model.FinishOK,
		shape: normalise.ShapeForWire(resolve.WireForPath(r.URL.Path)),
		chn:   p.chain.Load(), call: &model.Call{ID: id, Path: r.URL.Path}, w: forward.NewTimedWriter(w)}
}

// finishCall ends every call, whatever path it took (rejected, short-circuit,
// relayed, cut): it writes the access line and builds the call's one Metric
// for After and metricOut. With no consumer and no After hook that is done
// inline; otherwise a goroutine waits (bounded) for the consumer's usage and
// runs After, so neither can hold the response.
func (p *Proxy) finishCall(ctx context.Context, o *outcome) {
	d := time.Since(o.start)
	p.logCall(ctx, o, d)
	if o.tap == nil && !o.chn.Has(chain.StageAfter) {
		p.emit(o.metric(d))
		return
	}
	go p.settle(context.WithoutCancel(ctx), o, d) // hooks outlive the handler
}

// settle waits for the stream consumer (at most 2×HookChainDeadline), then
// hands the Metric to After and to metricOut. It ends with the call's hooks.
func (p *Proxy) settle(ctx context.Context, o *outcome, d time.Duration) {
	if o.tap != nil {
		o.tap.Wait(p.opts.Limits.HookChainDeadline)
		p.stats.droppedFrames.Add(o.tap.Dropped())
		p.stats.unparsed.Add(o.tap.Unparsed())
	}
	m := o.metric(d)
	o.chn.After(ctx, o.call, m)
	p.emit(m)
}

func (p *Proxy) emit(m model.Metric) {
	if out := p.metricOut.Load(); out != nil {
		(*out)(m)
	}
}

// wantsUsage reports whether anyone reads Metric.Usage for a call on chn: an
// After hook, or the metrics output (a sink or the admin endpoints, admin.go).
func (p *Proxy) wantsUsage(chn *chain.Chain) bool {
	return p.metricOut.Load() != nil || chn.Has(chain.StageAfter)
}

// metric builds the call's Metric; d is the handler's duration.
func (o *outcome) metric(d time.Duration) model.Metric {
	c := o.call
	m := model.Metric{CallID: o.callID, TenantID: o.tenant, SessionID: c.Session.ID, Provider: o.provider,
		Model: c.Model, Stream: c.Stream, Status: o.status, Finish: o.finish, Dispatched: o.dispatched, Usage: o.usage,
		Start: o.start, Duration: d}
	switch {
	case o.final != nil: // the barrier saw every upstream response, discarded terminals included
		m.Usage = o.final.Usage
	case o.tap != nil:
		m.Usage = o.tap.Usage()
	}
	if t := o.w.FirstWrite(); !t.IsZero() {
		m.TTFB = t.Sub(o.start)
	}
	return m
}

// logCall writes the access line. The request context may already be
// cancelled (client gone), so it is detached: handlers that consult ctx must
// still record exactly those calls.
func (p *Proxy) logCall(ctx context.Context, o *outcome, d time.Duration) {
	p.opts.Logger.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "proxy call",
		slog.String("call_id", o.callID), slog.String("tenant", o.tenant),
		slog.String("provider", o.provider), slog.Int("status", o.status),
		slog.String("finish", string(o.finish)),
		slog.Int64("duration_ms", d.Milliseconds()))
}
