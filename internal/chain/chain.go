package chain

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// Limits are the hook deadlines (Options.Limits.HookBeforeDeadline and
// HookChainDeadline).
type Limits struct {
	Hook  time.Duration // one hook call
	Total time.Duration // all hooks of one stage together (After: one hook)
}

// Stage is one Hook method the chain runs.
type Stage int

// Stages, in call order.
const (
	StageBefore Stage = iota
	StageEvent
	StageResponse
	StageAfter
	StageSessionClosed
	numStages
)

// stageMethod is a stage's Go method name and its name in logs.
var stageMethod = [numStages]struct{ goName, log string }{
	{"Before", "before"}, {"OnEvent", "on_event"}, {"OnResponse", "on_response"},
	{"After", "after"}, {"OnSessionClosed", "session_closed"},
}

// Chain is an immutable, ordered hook chain. Which methods each hook
// overrides is detected once, when it joins (detect.go), so a stage no hook
// implements costs nothing. Use builds a new Chain with With and a call keeps
// the Chain it started with. It holds no per-call state and is safe for
// concurrent use.
type Chain struct {
	all []model.Hook
	by  [numStages][]model.Hook // the hooks that override each stage, in order
	log *slog.Logger
	lim Limits
}

// New returns a Chain over hooks (nil hooks are ignored).
func New(hooks []model.Hook, log *slog.Logger, lim Limits) *Chain {
	return (&Chain{log: log, lim: lim}).With(hooks...)
}

// With returns a new Chain with hooks appended; c is unchanged. Without a
// non-nil hook it returns c.
func (c *Chain) With(hooks ...model.Hook) *Chain {
	hooks = slices.DeleteFunc(slices.Clone(hooks), func(h model.Hook) bool { return h == nil })
	if len(hooks) == 0 {
		return c
	}
	n := &Chain{all: slices.Concat(c.all, hooks), by: c.by, log: c.log, lim: c.lim}
	for s := range numStages {
		n.by[s] = slices.Clip(n.by[s]) // appends below never write into c's arrays
		for _, h := range hooks {
			if overrides(h, stageMethod[s].goName) {
				n.by[s] = append(n.by[s], h)
			}
		}
	}
	return n
}

// Hooks returns the hooks in order; callers must not modify the slice.
func (c *Chain) Hooks() []model.Hook { return slices.Clip(c.all) }

// Empty reports whether the chain has no hooks.
func (c *Chain) Empty() bool { return len(c.all) == 0 }

// Has reports whether any hook overrides stage s.
func (c *Chain) Has(s Stage) bool { return len(c.by[s]) > 0 }

// Before runs Hook.Before in order. A hook may rewrite call; it gets a deep
// private copy (cloneCall) that is adopted only if the hook returns in time
// without error or panic, so a failed hook, or a cancelled one that returns
// late, cannot change the call. A hook that returns a Response ends the chain
// and that Response is returned: the upstream is never called. Each hook gets
// Limits.Hook, all of them Limits.Total. A failing hook is logged and skipped.
func (c *Chain) Before(ctx context.Context, call *model.Call) *model.Response {
	type out struct {
		call model.Call
		resp *model.Response
	}
	var resp *model.Response
	run(ctx, c, StageBefore, idsOf(call), c.lim.Hook, c.lim.Total,
		func(h model.Hook) func(context.Context) (out, error) {
			cp := cloneCall(call) // here, on the caller: the hook's goroutine never reads call
			return func(ctx context.Context) (out, error) {
				r, err := h.Before(ctx, &cp)
				return out{cp, r}, err
			}
		},
		func(o out) bool {
			if o.resp != nil {
				resp = o.resp
				return true
			}
			*call = o.call
			return false
		})
	return resp
}

// OnResponse runs Hook.OnResponse in order over a buffered stream:false
// response and returns the final one. A hook gets a private copy of the
// response; a returned non-nil Response replaces it for the next hook. The
// call is shared and read-only here. Bounds and failures are as for Before.
func (c *Chain) OnResponse(ctx context.Context, call *model.Call, resp *model.Response) *model.Response {
	run(ctx, c, StageResponse, idsOf(call), c.lim.Hook, c.lim.Total,
		func(h model.Hook) func(context.Context) (*model.Response, error) {
			cp := cloneResponse(resp)
			return func(ctx context.Context) (*model.Response, error) { return h.OnResponse(ctx, call, cp) }
		},
		func(r *model.Response) bool {
			if r != nil {
				resp = r
			}
			return false
		})
	return resp
}

// After runs Hook.After in order, each under Limits.Total. The caller runs it
// off the request path; call and m are shared and read-only. A failing hook
// is logged and skipped.
func (c *Chain) After(ctx context.Context, call *model.Call, m model.Metric) {
	each(ctx, c, StageAfter, idsOf(call), func(ctx context.Context, h model.Hook) { h.After(ctx, call, m) })
}

// SessionClosed runs Hook.OnSessionClosed in order, each under Limits.Total.
// There is no call, so a failure is logged with the session's tenant only.
func (c *Chain) SessionClosed(ctx context.Context, s model.Session) {
	each(ctx, c, StageSessionClosed, ids{tenant: s.TenantID}, func(ctx context.Context, h model.Hook) { h.OnSessionClosed(ctx, s) })
}
