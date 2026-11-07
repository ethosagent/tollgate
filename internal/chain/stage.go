package chain

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// ids are what a skipped hook is logged with: never call data (PRIV-1).
type ids struct{ call, tenant string }

func idsOf(c *model.Call) ids { return ids{call: c.ID, tenant: c.Tenant.ID} }

// run is the one stage runner. For each hook of stage s, in order, prepare
// (on the caller's goroutine, so it may copy shared state) returns the call
// to make; it runs under Within with deadline per, and its result goes to
// adopt, which returns true to end the stage. total > 0 bounds the whole
// stage. A failing hook is logged and skipped; once the stage's context has
// ended the hooks not reached are logged and the stage ends.
func run[T any](ctx context.Context, c *Chain, s Stage, who ids, per, total time.Duration,
	prepare func(model.Hook) func(context.Context) (T, error), adopt func(T) (stop bool)) {
	hooks := c.by[s]
	if len(hooks) == 0 {
		return // no hook implements this stage: no context, no timer
	}
	if total > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, total)
		defer cancel()
	}
	for i, h := range hooks {
		if ctx.Err() != nil {
			for _, rest := range hooks[i:] {
				c.skip(ctx, who, rest, s, stageEndReason(ctx))
			}
			return
		}
		v, err := Within(ctx, per, prepare(h))
		if err != nil {
			c.fail(ctx, who, h, s, err)
		} else if adopt(v) {
			return
		}
	}
}

// each runs fn for every hook of stage s, each under Limits.Total.
func each(ctx context.Context, c *Chain, s Stage, who ids, fn func(context.Context, model.Hook)) {
	run(ctx, c, s, who, c.lim.Total, 0,
		func(h model.Hook) func(context.Context) (struct{}, error) {
			return func(ctx context.Context) (struct{}, error) { fn(ctx, h); return struct{}{}, nil }
		},
		func(struct{}) bool { return false })
}

// Reasons a hook was skipped, as logged.
const (
	reasonPanic      = "panic"
	reasonError      = "error"
	reasonTimeout    = "timeout"
	reasonDeadline   = "chain_deadline"
	reasonClientGone = "client_gone"
)

// stageEndReason says why a stage's context ended: the client left
// (Canceled) or the stage ran out of time.
func stageEndReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return reasonClientGone
	}
	return reasonDeadline
}

// fail logs a hook that failed: metadata only, never the panic value or the
// error text, which may carry request data (PRIV-1).
func (c *Chain) fail(ctx context.Context, who ids, h model.Hook, s Stage, err error) {
	switch {
	case IsPanic(err):
		c.skip(ctx, who, h, s, reasonPanic)
	case errors.Is(err, ErrDeadline) && ctx.Err() != nil: // the stage ended, not just this hook's time
		c.skip(ctx, who, h, s, stageEndReason(ctx))
	case errors.Is(err, ErrDeadline):
		c.skip(ctx, who, h, s, reasonTimeout)
	default:
		c.skip(ctx, who, h, s, reasonError)
	}
}

// skip writes the one log line for a skipped hook. A client that left is
// routine (info); everything else is the hook's fault (error).
func (c *Chain) skip(ctx context.Context, who ids, h model.Hook, s Stage, reason string) {
	level, msg := slog.LevelError, "hook skipped"
	switch reason {
	case reasonClientGone:
		level = slog.LevelInfo
	case reasonPanic:
		msg = "hook panicked"
	}
	c.log.LogAttrs(context.WithoutCancel(ctx), level, msg,
		slog.String("hook", nameOf(h)), slog.String("method", stageMethod[s].log),
		slog.String("call_id", who.call), slog.String("tenant", who.tenant),
		slog.String("reason", reason))
}

// nameOf is h.Name() that cannot panic.
func nameOf(h model.Hook) (name string) {
	defer func() {
		if recover() != nil {
			name = "unknown"
		}
	}()
	return h.Name()
}
