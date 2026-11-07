package chain

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// FinalizeLimits bound the Finalizer seam (Options.Limits).
type FinalizeLimits struct {
	Decide  time.Duration // one Decide call (HookChainDeadline)
	Budget  int           // recoveries one finalizer may start per call (FinalizerBudget)
	Ceiling int           // recoveries per call, all finalizers (RecoveryCeiling)
}

// Finalizers is the ordered, immutable list of Finalizers a Proxy was built
// with. It holds no per-call state and is safe for concurrent use.
type Finalizers struct {
	list []model.Finalizer
	log  *slog.Logger
	lim  FinalizeLimits
}

// NewFinalizers returns the Finalizers over list (nil entries are ignored).
// A nil log discards.
func NewFinalizers(list []model.Finalizer, log *slog.Logger, lim FinalizeLimits) *Finalizers {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Finalizers{list: slices.DeleteFunc(slices.Clone(list), func(f model.Finalizer) bool { return f == nil }), log: log, lim: lim}
}

// Empty reports whether there is no finalizer: then no terminal event is held.
func (f *Finalizers) Empty() bool { return f == nil || len(f.list) == 0 }

// Barrier is one call's Finalizer state: how many recoveries each finalizer
// and the call have used. A call is served by one goroutine at a time, but
// Decide is not safe for concurrent use.
type Barrier struct {
	f     *Finalizers
	call  *model.Call
	used  []int // recoveries per finalizer
	total int
}

// Begin starts the barrier of one call. call is read-only from here on.
func (f *Finalizers) Begin(call *model.Call) *Barrier {
	return &Barrier{f: f, call: call, used: make([]int, len(f.list))}
}

// Decide asks the finalizers in order what to do with the held terminal event
// of a response and returns the first answer that acts: a Recover with a body
// that the budgets allow, or a Replace with a reply. A finalizer that panics,
// overruns Limits.Decide, asks for more recoveries than its budget or the
// call's ceiling allows, or answers with a malformed decision counts as
// having released, and the next one is asked. If none acts the result is a
// Release. Each finalizer gets private copies of the call and the summary.
func (b *Barrier) Decide(ctx context.Context, s model.StreamSummary) model.Decision {
	for i, fz := range b.f.list {
		d, err := Within(ctx, b.f.lim.Decide, func(ctx context.Context) (model.Decision, error) {
			cp := cloneCall(b.call)
			return fz.Decide(ctx, &cp, cloneSummary(s)), nil
		})
		if err != nil {
			b.f.skip(ctx, b.call, fz, failReason(ctx, err))
			continue
		}
		if b.admit(ctx, i, &d) {
			return d
		}
	}
	return model.Decision{Action: model.DecisionRelease}
}

// admit reports whether d acts and may be acted on: well-formed and, for a
// recovery, within the budgets (counted here, when it is granted). It
// replaces d's slices with private copies.
func (b *Barrier) admit(ctx context.Context, i int, d *model.Decision) bool {
	switch d.Action {
	case model.DecisionRecover:
		if len(d.Body) == 0 {
			return false
		}
		if b.used[i] >= b.f.lim.Budget || b.total >= b.f.lim.Ceiling {
			b.f.skip(ctx, b.call, b.f.list[i], "over budget")
			return false
		}
		b.used[i]++
		b.total++
		d.Body = slices.Clone(d.Body)
		return true
	case model.DecisionReplace:
		if d.Response == nil || len(d.Response.Body) == 0 {
			return false
		}
		d.Response = cloneResponse(d.Response)
		return true
	}
	return false
}

func failReason(ctx context.Context, err error) string {
	switch {
	case IsPanic(err):
		return reasonPanic
	case ctx.Err() != nil:
		return stageEndReason(ctx)
	}
	return reasonTimeout
}

// skip logs a finalizer that was passed over: metadata only (PRIV-1).
func (f *Finalizers) skip(ctx context.Context, call *model.Call, fz model.Finalizer, reason string) {
	level := slog.LevelError
	if reason == reasonClientGone {
		level = slog.LevelInfo
	}
	f.log.LogAttrs(context.WithoutCancel(ctx), level, "finalizer skipped",
		slog.String("finalizer", nameOfFinalizer(fz)), slog.String("call_id", call.ID),
		slog.String("tenant", call.Tenant.ID), slog.String("reason", reason))
}

// nameOfFinalizer is fz.Name() that cannot panic.
func nameOfFinalizer(fz model.Finalizer) (name string) {
	defer func() {
		if recover() != nil {
			name = "unknown"
		}
	}()
	return fz.Name()
}

// cloneSummary is the private copy a Finalizer gets.
func cloneSummary(s model.StreamSummary) model.StreamSummary {
	s.ToolCalls = slices.Clone(s.ToolCalls)
	for i := range s.ToolCalls {
		s.ToolCalls[i].Arguments = slices.Clone(s.ToolCalls[i].Arguments)
	}
	return s
}
