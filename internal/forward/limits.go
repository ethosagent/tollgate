package forward

import (
	"context"
	"errors"
	"io"
	"sync"
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

func (l StreamLimits) none() bool { return l == StreamLimits{} }

// watchdog enforces StreamLimits for one call. When a limit fires it cancels
// the call's context with errLimit and closes the upstream body, which
// unblocks a read stuck on a silent upstream. It starts no goroutine of its
// own: timers run their callback on the runtime's. A nil *watchdog is valid
// and does nothing.
type watchdog struct {
	cancel context.CancelCauseFunc
	idleD  time.Duration

	mu    sync.Mutex
	body  io.Closer
	first *time.Timer
	idle  *time.Timer
	max   *time.Timer
	fired bool
	done  bool
}

// startWatchdog arms the first-byte and max-call timers. It returns nil when
// l sets no limit.
func startWatchdog(l StreamLimits, cancel context.CancelCauseFunc) *watchdog {
	if l.none() {
		return nil
	}
	w := &watchdog{cancel: cancel, idleD: l.Idle}
	w.mu.Lock()
	defer w.mu.Unlock()
	if l.MaxCall > 0 {
		w.max = time.AfterFunc(l.MaxCall, w.fire)
	}
	if l.FirstByte > 0 {
		w.first = time.AfterFunc(l.FirstByte, w.fire)
	}
	return w
}

// attach gives the watchdog the body to close on a timeout. With no
// first-byte limit the idle clock starts here.
func (w *watchdog) attach(body io.Closer) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.body = body
	fired := w.fired
	if w.first == nil && w.idleD > 0 && !w.done {
		w.idle = time.AfterFunc(w.idleD, w.fire)
	}
	w.mu.Unlock()
	if fired {
		_ = body.Close() // a limit fired while the round trip was finishing
	}
}

// touch records body bytes: the first-byte timer stops and the idle timer
// restarts.
func (w *watchdog) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	if w.first != nil {
		w.first.Stop()
		w.first = nil
	}
	switch {
	case w.idleD <= 0:
	case w.idle == nil:
		w.idle = time.AfterFunc(w.idleD, w.fire)
	default:
		w.idle.Reset(w.idleD)
	}
}

func (w *watchdog) fire() {
	w.mu.Lock()
	if w.done || w.fired {
		w.mu.Unlock()
		return
	}
	w.fired = true
	body := w.body
	w.mu.Unlock()
	w.cancel(errLimit)
	if body != nil {
		_ = body.Close()
	}
}

// stop disarms every timer. A callback already running sees done and exits.
func (w *watchdog) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	for _, t := range []*time.Timer{w.first, w.idle, w.max} {
		if t != nil {
			t.Stop()
		}
	}
}

// touchReader tells the watchdog about every read that returns bytes.
type touchReader struct {
	r  io.Reader
	wd *watchdog
}

func (t touchReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.wd.touch()
	}
	return n, err
}
