package chain

import (
	"context"

	"github.com/ethosagent/tollgate/internal/model"
)

// Events delivers the events of one streamed call to every hook's OnEvent, in
// Use order. It is used by one goroutine (the call's consumer) and is not
// safe for concurrent use.
type Events struct {
	c    *Chain
	call *model.Call
	dead []bool // hooks that panicked on this call: skipped, so one log line
}

// Events returns the delivery state for one call. Only hooks that override
// OnEvent are called.
func (c *Chain) Events(call *model.Call) *Events {
	return &Events{c: c, call: call, dead: make([]bool, len(c.by[StageEvent]))}
}

// Deliver gives e to each hook. A hook that panics is logged once and skipped
// for the rest of the call. It does nothing and reports false once ctx has
// ended, so the consumer can drain its queue without waiting on hooks. A hook
// that ignores ctx holds the consumer, never the client (ADR 0004). Events are
// shared by all hooks and must be treated as read-only.
func (d *Events) Deliver(ctx context.Context, e model.Event) bool {
	if ctx.Err() != nil {
		return false
	}
	for i, h := range d.c.by[StageEvent] {
		if !d.dead[i] && !d.one(ctx, h, e) {
			d.dead[i] = true
		}
	}
	return true
}

// one calls a single OnEvent under recover, with no goroutine or deadline per
// event (that would cost a goroutine per event per hook): the consumer's ctx
// carries the bound. A hook that ignores ctx can hold the consumer goroutine,
// never the client; the caller bounds how long After waits for it.
func (d *Events) one(ctx context.Context, h model.Hook, e model.Event) (ok bool) {
	defer func() {
		if recover() != nil {
			d.c.skip(ctx, idsOf(d.call), h, StageEvent, reasonPanic)
			ok = false
		}
	}()
	h.OnEvent(ctx, d.call, e)
	return true
}
