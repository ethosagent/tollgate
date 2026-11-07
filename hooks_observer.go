package tollgate

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// observer is the per-call hook state: the OnEvent consumer, the buffered
// response path and the After goroutine. It is created and finished by the
// handler goroutine; the goroutines it starts end with the call.
type observer struct {
	p     *Proxy
	chn   *chain.Chain
	call  *Call
	o     *outcome
	wire  normalise.Wire // nil for a pass-through wire
	timed *forward.TimedWriter

	base       context.Context // the request's values without its cancellation
	events     context.Context // OnEvent's ctx: cancelled shortly after the call ends
	stopEvents context.CancelFunc

	consumed chan struct{} // closed when the OnEvent consumer has ended
	// usage, end and ended are written by the consumer (or, for stream:false,
	// by the handler) before After's goroutine reads them.
	usage Usage
	end   Finish
	ended bool
}

func newObserver(p *Proxy, chn *chain.Chain, call *Call, o *outcome, r *http.Request, w http.ResponseWriter, wire normalise.Wire) *observer {
	base := context.WithoutCancel(r.Context()) // hooks outlive the handler
	events, stop := context.WithCancel(base)
	done := make(chan struct{})
	close(done) // no consumer until startEvents
	return &observer{p: p, chn: chn, call: call, o: o, wire: wire, timed: forward.NewTimedWriter(w),
		base: base, events: events, stopEvents: stop, consumed: done}
}

// shortCircuit answers with a Before hook's Response; the upstream is not called.
func (obs *observer) shortCircuit(resp *Response) {
	obs.o.status = forward.ResponseStatus(resp.Status)
	if obs.o.status >= http.StatusBadRequest {
		obs.o.finish = FinishRejected
	}
	forward.WriteResponse(obs.timed, resp)
}

// startEvents starts the call's single OnEvent consumer and returns the queue
// the tee feeds. Forward closes the queue when the call ends, which ends the
// consumer.
func (obs *observer) startEvents() *forward.FrameQueue {
	q := forward.NewFrameQueue(obs.p.opts.Limits.EventBuffer)
	obs.consumed = make(chan struct{})
	go obs.consume(q, obs.wire.NewEventMapper())
	return q
}

// consume maps the queue's frames to Events and delivers them to every hook.
// Frames the tee dropped are reported to the mapper as a gap, and the real
// ending comes from the queue, not from the mapper (normalise.EventMapper).
func (obs *observer) consume(q *forward.FrameQueue, m normalise.EventMapper) {
	defer close(obs.consumed)
	d := obs.chn.Events(obs.call)
	for f, gap, ok := q.Next(); ok; f, gap, ok = q.Next() {
		if gap > 0 {
			m.Gap(gap)
		}
		name, data := normalise.SplitFrame(f)
		if evs, err := m.Frame(name, data); err == nil {
			obs.deliver(d, evs)
		}
	}
	end := q.End()
	m.Gap(end.Gap)
	obs.deliver(d, m.Finish())
	obs.end, obs.ended = end.Finish, true
	obs.p.stats.droppedFrames.Add(q.Dropped())
}

func (obs *observer) deliver(d *chain.Events, evs []Event) {
	for _, e := range evs {
		if e.Kind == EventUsage && e.Usage != nil {
			obs.usage = *e.Usage // the mapper reports a running total
		}
		d.Deliver(obs.events, e)
	}
}

// finishHeld ends a stream:false call: a complete response goes through the
// OnResponse chain, a broken one is released as it is (the caller then aborts
// the connection), and one that was passed through needs nothing.
func (obs *observer) finishHeld(ctx context.Context, held *forward.Capture, res forward.Result) {
	if !held.Started() || held.Passed() {
		return
	}
	if res.Cut || res.Finish == FinishClientCancel {
		// The client left or must see the cut; a failed write changes nothing.
		// The flush matters: the abort that follows discards unsent bytes.
		_ = held.Release()
		_ = http.NewResponseController(obs.timed).Flush()
		return
	}
	resp := held.Response()
	obs.usageOf(resp)
	out := obs.chn.OnResponse(ctx, obs.call, resp)
	obs.o.status = forward.ResponseStatus(out.Status)
	forward.WriteResponse(obs.timed, out)
}

// usageOf takes the token usage from a complete successful response body.
func (obs *observer) usageOf(resp *Response) {
	if resp.Status >= http.StatusBadRequest {
		return
	}
	evs, err := obs.wire.MapBody(resp.Body)
	if err != nil {
		return
	}
	for _, e := range evs {
		if e.Kind == EventUsage && e.Usage != nil {
			obs.usage = *e.Usage
		}
	}
}

// skipped records a stream:false response that went to the client without
// OnResponse (over the buffer cap, or encoded).
func (obs *observer) skipped(reason string) {
	obs.p.stats.unbuffered.Add(1)
	obs.p.opts.Logger.LogAttrs(context.Background(), slog.LevelWarn, "response not held for hooks",
		slog.String("reason", reason), slog.Int64("cap", obs.p.opts.Limits.MaxRequestBody),
		slog.String("call_id", obs.call.ID), slog.String("tenant", obs.call.Tenant.ID))
}

// complete hands the finished call to a goroutine that waits for the OnEvent
// consumer and then runs After. The handler does not wait, so After cannot
// hold the response. The consumer gets Limits.HookChainDeadline to drain what
// is left once the call has ended.
func (obs *observer) complete() {
	m := obs.metric()
	drain := time.AfterFunc(obs.p.opts.Limits.HookChainDeadline, obs.stopEvents)
	go func() {
		<-obs.consumed
		drain.Stop()
		obs.stopEvents()
		m.Usage = obs.usage
		if obs.ended {
			m.Finish = obs.end
		}
		obs.chn.After(obs.base, obs.call, m)
	}()
}

// metric is the Metric as far as the handler knows it; complete adds usage.
func (obs *observer) metric() Metric {
	o, c := obs.o, obs.call
	m := Metric{CallID: o.callID, TenantID: o.tenant, SessionID: c.Session.ID, Provider: o.provider,
		Model: c.Model, Stream: c.Stream, Status: o.status, Finish: o.finish, Start: o.start,
		Duration: time.Since(o.start)}
	if t := obs.timed.FirstWrite(); !t.IsZero() {
		m.TTFB = t.Sub(o.start)
	}
	return m
}
