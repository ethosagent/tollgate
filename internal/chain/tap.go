package chain

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// Tap consumes one streamed call's frame queue on its own goroutine: it maps
// frames to Events, keeps the call's usage and delivers each event to the
// OnEvent hooks (none is fine: then it only collects usage). It ends when
// Forward closes the queue. Usage, Dropped and Unparsed are safe to read at
// any time.
type Tap struct {
	done     chan struct{}
	stop     context.CancelFunc // ends OnEvent's context
	q        *forward.FrameQueue
	unparsed atomic.Int64 // frames the mapper rejected: no events, no usage
	mu       sync.Mutex
	usage    model.Usage
}

// StartTap starts the consumer for q. OnEvent hooks get a context derived
// from ctx (which must not end with the request) that Wait cancels.
func (c *Chain) StartTap(ctx context.Context, call *model.Call, q *forward.FrameQueue, m normalise.EventMapper) *Tap {
	ctx, stop := context.WithCancel(ctx)
	t := &Tap{done: make(chan struct{}), stop: stop, q: q}
	go t.consume(ctx, c.Events(call), m)
	return t
}

// consume maps the queue's frames to Events and delivers them. Frames the tee
// dropped are reported to the mapper as a gap; the call's ending is the
// queue's End, recorded by the caller from Forward's Result.
func (t *Tap) consume(ctx context.Context, d *Events, m normalise.EventMapper) {
	defer close(t.done)
	for f, gap, ok := t.q.Next(); ok; f, gap, ok = t.q.Next() {
		if gap > 0 {
			m.Gap(gap)
		}
		name, data := normalise.SplitFrame(f)
		evs, err := m.Frame(name, data)
		if err != nil {
			t.unparsed.Add(1) // counted, never silent (STR-8)
			continue
		}
		t.note(evs)
		t.deliver(ctx, d, evs)
	}
	m.Gap(t.q.End().Gap)
	t.deliver(ctx, d, m.Finish())
}

func (t *Tap) note(evs []model.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	normalise.TrackUsage(&t.usage, evs)
}

func (t *Tap) deliver(ctx context.Context, d *Events, evs []model.Event) {
	for _, e := range evs {
		d.Deliver(ctx, e)
	}
}

// Wait waits for the consumer once the call has ended: up to drain for the
// hooks to see what is left, then it cancels OnEvent's context and waits up
// to drain again for the queue to empty. A hook that ignores its context can
// keep the consumer goroutine, but never more than 2×drain of the caller's
// time: the caller (After, the Metric) goes on with the usage known so far.
// It reports whether the consumer finished.
func (t *Tap) Wait(drain time.Duration) bool {
	timer := time.NewTimer(drain)
	defer timer.Stop()
	defer t.stop()
	for range 2 {
		select {
		case <-t.done:
			return true
		case <-timer.C:
			t.stop()
			timer.Reset(drain)
		}
	}
	return false
}

// Usage is the usage seen so far: final once Wait reported true.
func (t *Tap) Usage() model.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

// Dropped is how many frames the tee dropped for this call so far.
func (t *Tap) Dropped() int64 { return t.q.Dropped() }

// Unparsed is how many frames the mapper could not parse so far. Their
// events and usage are lost; the client still got their bytes.
func (t *Tap) Unparsed() int64 { return t.unparsed.Load() }
