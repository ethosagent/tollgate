package forward

import (
	"sync"
	"sync/atomic"

	"github.com/ethosagent/tollgate/internal/model"
)

// frameBudget is the average frame size a FrameQueue budgets for: its byte
// cap is size * frameBudget, and never below MaxFrameBytes so one maximal
// frame always fits an empty queue.
const frameBudget = 4 << 10

// minQueueSlots is the first allocation of a FrameQueue's ring. A consumer
// that keeps up never needs more, so an open stream holds a few hundred
// bytes of queue, not the whole frame cap.
const minQueueSlots = 8

// FrameQueue carries the SSE frames of one response from the tee to a
// consumer in another goroutine (ADR 0004). It is bounded twice: by frame
// count and by the bytes queued. When either is full the tee drops the frame
// and counts it, so a slow or stuck consumer never delays the client and
// never pins more than the byte cap of body memory.
//
// Drops are not silent: the next frame delivered reports how many frames
// were lost before it (its gap), and End reports the drops after the last
// one together with how the call ended, so a stateful consumer (an
// EventMapper) can be told about the gap instead of mis-reading the stream.
// Forward closes the queue when the call ends. The consumer's goroutine
// belongs to whoever attached the queue; the tee starts none.
//
// The frames sit in a ring that grows on demand up to the frame cap, so
// memory follows the backlog rather than the cap. The tee never waits on
// the lock for longer than a consumer's pop or push.
type FrameQueue struct {
	mu     sync.Mutex
	ring   []queued // grows by doubling up to max
	head   int      // index of the oldest frame
	n      int      // frames queued
	max    int      // frame cap
	closed bool
	ready  chan struct{} // capacity 1: a frame or the end is waiting

	maxBytes int64
	bytes    atomic.Int64 // bytes queued: producer adds, consumer subtracts
	dropped  atomic.Int64
	gap      int64 // producer only: drops since the last queued frame
	end      End   // written under mu before closed is set
}

type queued struct {
	frame []byte
	gap   int64
}

// End is how the stream behind a FrameQueue ended.
type End struct {
	// Finish is the call's ending: FinishOK for a clean EOF, else timeout,
	// upstream_error or client_cancel (Result.Finish).
	Finish model.Finish
	// Gap is the number of frames dropped after the last delivered one.
	Gap int64
}

// NewFrameQueue returns a queue of up to size frames and size*4 KiB bytes
// (at least MaxFrameBytes). The proxy sizes it from Limits.EventBuffer.
func NewFrameQueue(size int) *FrameQueue {
	size = max(size, 0)
	return &FrameQueue{max: size, ready: make(chan struct{}, 1), maxBytes: max(int64(size)*frameBudget, MaxFrameBytes)}
}

// Next blocks for the next frame. frame is the consumer's to keep; gap is the
// number of frames dropped since the previous one (0 when none). ok is
// false once the call has ended and every frame was read; then call End.
func (q *FrameQueue) Next() (frame []byte, gap int64, ok bool) {
	for {
		q.mu.Lock()
		if q.n > 0 {
			it := q.ring[q.head]
			q.ring[q.head] = queued{} // the frame is the consumer's now; do not pin it
			q.head = (q.head + 1) % len(q.ring)
			q.n--
			q.mu.Unlock()
			q.bytes.Add(-int64(len(it.frame)))
			return it.frame, it.gap, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return nil, 0, false
		}
		<-q.ready // every push and the close signal after unlocking: no wake-up is lost
	}
}

// End reports how the call ended. It is valid only after Next returned
// ok=false.
func (q *FrameQueue) End() End { return q.end }

// Dropped is the number of frames dropped so far: queue full (frames or
// bytes), or frame over MaxFrameBytes. Safe to call at any time.
func (q *FrameQueue) Dropped() int64 { return q.dropped.Load() }

// offer hands a copy of frame to the consumer without ever blocking on the
// consumer. Only the relay goroutine calls it, and only before finish. The
// copy is the one allocation per delivered frame: frame aliases the relay
// buffer.
func (q *FrameQueue) offer(frame []byte) {
	n := int64(len(frame))
	if q.bytes.Load()+n > q.maxBytes { // only the consumer changes bytes meanwhile, and only down
		q.lose(1)
		return
	}
	q.mu.Lock()
	if q.n == q.max {
		q.mu.Unlock()
		q.lose(1)
		return
	}
	q.push(queued{frame: append([]byte(nil), frame...), gap: q.gap})
	q.mu.Unlock()
	q.bytes.Add(n)
	q.gap = 0
	q.signal()
}

// push appends it to the ring, growing it when full. q.mu is held.
func (q *FrameQueue) push(it queued) {
	if q.n == len(q.ring) {
		grown := make([]queued, min(max(2*len(q.ring), minQueueSlots), q.max))
		for i := range q.n {
			grown[i] = q.ring[(q.head+i)%len(q.ring)]
		}
		q.ring, q.head = grown, 0
	}
	q.ring[(q.head+q.n)%len(q.ring)] = it
	q.n++
}

// signal wakes the consumer if it waits; a pending signal already will.
func (q *FrameQueue) signal() {
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// frame makes the queue the Scanner's sink: every frame that carries a
// field is offered.
func (q *FrameQueue) frame(f []byte, _ int) {
	if f != nil {
		q.offer(f)
	}
}

// lose counts n dropped frames. It is nil-safe.
func (q *FrameQueue) lose(n int64) {
	if q == nil || n == 0 {
		return
	}
	q.gap += n
	q.dropped.Add(n)
}

// finish records the ending and closes the queue. Forward calls it exactly
// once; it is nil-safe.
func (q *FrameQueue) finish(f model.Finish) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.end = End{Finish: f, Gap: q.gap}
	q.closed = true
	q.mu.Unlock()
	q.signal()
}
