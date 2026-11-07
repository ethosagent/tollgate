package forward

import (
	"context"
	"errors"
	"io"
	"time"
)

// errLimit is the cancel cause when a stream limit fires. finishFor reads it
// to tell a timeout from a client that left.
var errLimit = errors.New("forward: stream limit exceeded")

// StreamLimits bound one forwarded call. A zero field disables that limit.
type StreamLimits struct {
	// FirstByte is the time allowed until the first upstream body byte,
	// counted from the start of the call.
	FirstByte time.Duration
	// Idle is the longest quiet gap allowed between upstream body bytes.
	Idle time.Duration
	// MaxCall is the longest the whole call may take.
	MaxCall time.Duration
}

// withLimits derives the call context: MaxCall is a deadline whose cause is
// errLimit, and cancel ends the call with a cause (errLimit from the idle
// clock, nil on return). The caller must call cancel.
func withLimits(ctx context.Context, l StreamLimits) (context.Context, context.CancelCauseFunc) {
	stop := func() {}
	if l.MaxCall > 0 {
		ctx, stop = context.WithTimeoutCause(ctx, l.MaxCall, errLimit)
	}
	ctx, cancel := context.WithCancelCause(ctx)
	return ctx, func(cause error) { cancel(cause); stop() }
}

// idleClock is the one re-armable timer behind FirstByte and Idle. It is
// armed with FirstByte at the start of the call (or with Idle once headers
// arrive when FirstByte is zero) and re-armed with Idle on every read that
// returns bytes. When it fires it cancels the call with errLimit; closing
// the body is left to the context (see Forward). Only the call's goroutine
// touches it, so it needs no lock; time.Timer is safe against its own
// callback. A nil *idleClock is valid and does nothing.
type idleClock struct {
	t     *time.Timer
	idle  time.Duration
	fire  func()
	first bool // no body byte seen yet
}

// startClock arms the first-byte limit. It returns nil when l sets neither
// FirstByte nor Idle.
func startClock(l StreamLimits, cancel context.CancelCauseFunc) *idleClock {
	if l.FirstByte <= 0 && l.Idle <= 0 {
		return nil
	}
	c := &idleClock{idle: l.Idle, fire: func() { cancel(errLimit) }, first: true}
	if l.FirstByte > 0 {
		c.t = time.AfterFunc(l.FirstByte, c.fire)
	}
	return c
}

// headers starts the idle clock when there is no first-byte limit.
func (c *idleClock) headers() {
	if c != nil && c.t == nil {
		c.t = time.AfterFunc(c.idle, c.fire)
	}
}

// touch records body bytes: the first-byte limit is over and the idle clock
// restarts.
func (c *idleClock) touch() {
	switch {
	case c.idle > 0:
		c.t.Reset(c.idle)
	case c.first:
		c.t.Stop()
	}
	c.first = false
}

func (c *idleClock) stop() {
	if c != nil && c.t != nil {
		c.t.Stop()
	}
}

// touchReader tells the clock about every read that returns bytes.
type touchReader struct {
	r io.Reader
	c *idleClock
}

func (t *touchReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.c.touch()
	}
	return n, err
}
