package forward

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Tee copies src to dst like Relay and, when q is not nil, scans the same
// bytes into frames for q. Each read is written and flushed to the client
// first; only then is a copy scanned, so the consumer can never delay the
// client (ADR 0004). A nil q means no scanning and no extra work. begin, if
// not nil, runs once just before the first byte is written, so a caller can
// hold the response headers until there is a body to send. Tee does not
// close q. It returns the bytes written; errors are as for Relay.
func Tee(dst http.ResponseWriter, src io.Reader, q *FrameQueue, begin func()) (int64, error) {
	rc := http.NewResponseController(dst)
	rb := newRelayBuf()
	defer rb.release()
	var sc Scanner
	var total int64
	for {
		buf := rb.bytes()
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
		rb.read(n) // buf[:n] is written and scanned: it may be returned now
	}
}
