package chain

import (
	"context"
	"errors"
	"log/slog"
	"maps"
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

// Chain is the ordered hook chain of one call: a snapshot of the hooks
// registered when the call started. It holds no per-call state and is safe for
// concurrent use; hooks added later belong to later calls.
type Chain struct {
	hooks []model.Hook
	log   *slog.Logger
	lim   Limits
}

// New returns a Chain over hooks, which the caller must not modify.
func New(hooks []model.Hook, log *slog.Logger, lim Limits) *Chain {
	return &Chain{hooks: hooks, log: log, lim: lim}
}

// Empty reports whether the chain has no hooks, so callers can skip the work.
func (c *Chain) Empty() bool { return c == nil || len(c.hooks) == 0 }

// Before runs Hook.Before in order. A hook may rewrite call (Body, Header,
// Meta); it gets a private copy that is adopted only if the hook returns in
// time without error or panic, so a cancelled hook that returns late cannot
// change the call. A hook that returns a Response ends the chain and that
// Response is returned: the upstream is never called. Each hook gets
// Limits.Hook, all of them Limits.Total. A failing hook is logged and skipped.
func (c *Chain) Before(ctx context.Context, call *model.Call) *model.Response {
	ctx, cancel := context.WithTimeout(ctx, c.lim.Total)
	defer cancel()
	type out struct {
		call model.Call
		resp *model.Response
	}
	for i, h := range c.hooks {
		if ctx.Err() != nil {
			c.expired(ctx, call, "before", c.hooks[i:])
			return nil
		}
		cp := cloneCall(call) // before the goroutine starts: it must never read call
		res, err := Within(ctx, c.lim.Hook, func(ctx context.Context) (out, error) {
			r, err := h.Before(ctx, &cp)
			return out{cp, r}, err
		})
		switch {
		case err != nil:
			c.fail(ctx, call, h, "before", err)
		case res.resp != nil:
			return res.resp
		default:
			*call = res.call
		}
	}
	return nil
}

// OnResponse runs Hook.OnResponse in order over a buffered stream:false
// response and returns the final one. A hook gets a private copy of the
// response; a returned non-nil Response replaces it for the next hook. Bounds
// and failure handling are as for Before.
func (c *Chain) OnResponse(ctx context.Context, call *model.Call, resp *model.Response) *model.Response {
	ctx, cancel := context.WithTimeout(ctx, c.lim.Total)
	defer cancel()
	for i, h := range c.hooks {
		if ctx.Err() != nil {
			c.expired(ctx, call, "on_response", c.hooks[i:])
			break
		}
		cp := cloneResponse(resp)
		r, err := Within(ctx, c.lim.Hook, func(ctx context.Context) (*model.Response, error) {
			return h.OnResponse(ctx, call, cp)
		})
		if err != nil {
			c.fail(ctx, call, h, "on_response", err)
		} else if r != nil {
			resp = r
		}
	}
	return resp
}

// After runs Hook.After in order, each under Limits.Total. The caller runs it
// off the request path; a failing hook is logged and skipped.
func (c *Chain) After(ctx context.Context, call *model.Call, m model.Metric) {
	for _, h := range c.hooks {
		_, err := Within(ctx, c.lim.Total, func(ctx context.Context) (struct{}, error) {
			h.After(ctx, call, m)
			return struct{}{}, nil
		})
		if err != nil {
			c.fail(ctx, call, h, "after", err)
		}
	}
}

// SessionClosed runs Hook.OnSessionClosed in order, each under Limits.Total.
// There is no call, so a failure is logged with the session's tenant only.
func (c *Chain) SessionClosed(ctx context.Context, s model.Session) {
	call := &model.Call{Tenant: model.Tenant{ID: s.TenantID}}
	for _, h := range c.hooks {
		_, err := Within(ctx, c.lim.Total, func(ctx context.Context) (struct{}, error) {
			h.OnSessionClosed(ctx, s)
			return struct{}{}, nil
		})
		if err != nil {
			c.fail(ctx, call, h, "session_closed", err)
		}
	}
}

// fail logs a skipped hook: metadata only, never the panic value or the error
// text, which may carry request data (PRIV-1).
func (c *Chain) fail(ctx context.Context, call *model.Call, h model.Hook, method string, err error) {
	reason, msg := "error", "hook skipped"
	switch {
	case IsPanic(err):
		reason, msg = "panic", "hook panicked"
	case errors.Is(err, ErrDeadline):
		reason = "timeout"
	}
	c.log.LogAttrs(ctx, slog.LevelError, msg,
		slog.String("hook", nameOf(h)), slog.String("method", method),
		slog.String("call_id", call.ID), slog.String("tenant", call.Tenant.ID),
		slog.String("reason", reason))
}

// expired logs the hooks a stage never reached because its time ran out or
// the client left.
func (c *Chain) expired(ctx context.Context, call *model.Call, method string, skipped []model.Hook) {
	for _, h := range skipped {
		c.log.LogAttrs(ctx, slog.LevelError, "hook skipped",
			slog.String("hook", nameOf(h)), slog.String("method", method),
			slog.String("call_id", call.ID), slog.String("tenant", call.Tenant.ID),
			slog.String("reason", "chain_deadline"))
	}
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

// cloneCall copies what a Before hook may write: Body bytes, Header and Meta
// (shallow). The rest is a value copy.
func cloneCall(c *model.Call) model.Call {
	cp := *c
	cp.Body = slices.Clone(c.Body)
	cp.Header = c.Header.Clone()
	cp.Meta = maps.Clone(c.Meta)
	cp.Messages = slices.Clone(c.Messages)
	return cp
}

func cloneResponse(r *model.Response) *model.Response {
	return &model.Response{Status: r.Status, Header: r.Header.Clone(), Body: slices.Clone(r.Body)}
}
