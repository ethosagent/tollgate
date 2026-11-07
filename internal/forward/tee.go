package forward

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// FrameQueue carries the SSE frames of one response from the tee to a
// consumer in another goroutine (ADR 0004). It is bounded: when it is full the
// tee drops the frame and counts it, so a slow or stuck consumer never delays
// the client. The tee closes the channel when the call ends. The consumer's
// goroutine belongs to whoever attached the queue; the tee starts none.
type FrameQueue struct {
	ch      chan []byte
	dropped atomic.Int64
	closed  atomic.Bool
}

// NewFrameQueue returns a queue that holds up to size frames.
func NewFrameQueue(size int) *FrameQueue {
	return &FrameQueue{ch: make(chan []byte, max(size, 0))}
}

// Frames is the receive side. Each frame is the consumer's to keep. The
// channel is closed when the call ends.
func (q *FrameQueue) Frames() <-chan []byte { return q.ch }

// Dropped is the number of frames dropped so far: queue full, or frame over
// MaxFrameBytes.
func (q *FrameQueue) Dropped() int64 { return q.dropped.Load() }

// offer hands a copy of frame to the consumer without ever blocking. Only
// the relay goroutine calls it, and only before close.
func (q *FrameQueue) offer(frame []byte) {
	if len(q.ch) == cap(q.ch) { // full: skip the copy
		q.dropped.Add(1)
		return
	}
	select {
	case q.ch <- append([]byte(nil), frame...):
	default:
		q.dropped.Add(1)
	}
}

// close ends the stream for the consumer. It is idempotent and nil-safe.
func (q *FrameQueue) close() {
	if q != nil && q.closed.CompareAndSwap(false, true) {
		close(q.ch)
	}
}

var scanners = sync.Pool{New: func() any { return new(Scanner) }}

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
	var sc *Scanner
	if q != nil {
		sc = scanners.Get().(*Scanner)
		sc.Reset()
		sc.Oversized = 0
		defer func() {
			q.dropped.Add(int64(sc.Oversized))
			scanners.Put(sc)
		}()
	}
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
			if sc != nil {
				sc.Scan(buf[:n], q.offer)
			}
		}
		if errors.Is(rerr, io.EOF) {
			if sc != nil {
				sc.Finish(q.offer)
			}
			return total, nil
		}
		if rerr != nil {
			return total, fmt.Errorf("%w: %w", ErrUpstreamRead, rerr)
		}
	}
}
