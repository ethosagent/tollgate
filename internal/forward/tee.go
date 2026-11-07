package forward

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/ethosagent/tollgate/internal/model"
)

// frameBudget is the average frame size a FrameQueue budgets for: its byte
// cap is size * frameBudget, and never below MaxFrameBytes so one maximal
// frame always fits an empty queue.
const frameBudget = 4 << 10

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
type FrameQueue struct {
	ch       chan queued
	maxBytes int64
	bytes    atomic.Int64 // bytes in ch: producer adds, consumer subtracts
	dropped  atomic.Int64
	gap      int64 // producer only: drops since the last queued frame
	end      End   // written by the producer before close(ch)
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
	return &FrameQueue{ch: make(chan queued, size), maxBytes: max(int64(size)*frameBudget, MaxFrameBytes)}
}

// Next blocks for the next frame. frame is the consumer's to keep; gap is the
// number of frames dropped since the previous one (0 when none). ok is
// false once the call has ended and every frame was read; then call End.
func (q *FrameQueue) Next() (frame []byte, gap int64, ok bool) {
	it, ok := <-q.ch
	if !ok {
		return nil, 0, false
	}
	q.bytes.Add(-int64(len(it.frame)))
	return it.frame, it.gap, true
}

// End reports how the call ended. It is valid only after Next returned
// ok=false.
func (q *FrameQueue) End() End { return q.end }

// Dropped is the number of frames dropped so far: queue full (frames or
// bytes), or frame over MaxFrameBytes. Safe to call at any time.
func (q *FrameQueue) Dropped() int64 { return q.dropped.Load() }

// offer hands a copy of frame to the consumer without ever blocking. Only
// the relay goroutine calls it, and only before finish. The copy is the one
// allocation per delivered frame: frame aliases the relay buffer.
func (q *FrameQueue) offer(frame []byte) {
	n := int64(len(frame))
	if len(q.ch) == cap(q.ch) || q.bytes.Load()+n > q.maxBytes {
		q.lose(1)
		return
	}
	q.bytes.Add(n)
	q.ch <- queued{frame: append([]byte(nil), frame...), gap: q.gap} // cannot block: one producer, room checked
	q.gap = 0
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
	q.end = End{Finish: f, Gap: q.gap}
	close(q.ch)
}

// Tee copies src to dst like Relay and, when q is not nil, scans the same
// bytes into frames for q. Each read is written and flushed to the client
// first; only then is a copy scanned, so the consumer can never delay the
// client (ADR 0004). A nil q means no scanning and no extra work. begin, if
// not nil, runs once just before the first byte is written, so a caller can
// hold the response headers until there is a body to send. Tee does not
// close q. It returns the bytes written; errors are as for Relay.
func Tee(dst http.ResponseWriter, src io.Reader, q *FrameQueue, begin func()) (int64, error) {
	rc := http.NewResponseController(dst)
	bp := relayBufs.Get().(*[]byte)
	defer relayBufs.Put(bp)
	buf := *bp
	var sc Scanner
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if begin != nil {
				begin()
				begin = nil
			}
			w, werr := writeFlush(dst, rc, buf[:n])
			total += int64(w)
			if werr != nil {
				return total, fmt.Errorf("%w: %w", ErrClientWrite, werr)
			}
			if q != nil {
				sc.scan(buf[:n], q)
				q.lose(sc.takeOversized())
			}
		}
		if errors.Is(rerr, io.EOF) {
			if q != nil {
				sc.finish(q)
				q.lose(sc.takeOversized())
			}
			return total, nil
		}
		if rerr != nil {
			return total, fmt.Errorf("%w: %w", ErrUpstreamRead, rerr)
		}
	}
}
