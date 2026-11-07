package forward

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// Errors that say which side of the relay failed.
var (
	// ErrClientWrite means writing or flushing to the client failed; the
	// client is gone and the upstream request should be cancelled.
	ErrClientWrite = errors.New("forward: client write failed")
	// ErrUpstreamRead means reading the upstream body failed before EOF.
	ErrUpstreamRead = errors.New("forward: upstream read failed")
)

const relayBufSize = 32 << 10

var relayBufs = sync.Pool{New: func() any { b := make([]byte, relayBufSize); return &b }}

// Relay copies src to dst and calls flush after every non-empty read, so a
// streamed chunk reaches the client as soon as it arrives. It reuses pooled
// buffers and returns the bytes written. A nil error means src hit EOF; a
// failure wraps ErrClientWrite or ErrUpstreamRead.
func Relay(dst io.Writer, flush func() error, src io.Reader) (int64, error) {
	bp := relayBufs.Get().(*[]byte)
	defer relayBufs.Put(bp)
	buf := *bp
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := dst.Write(buf[:n])
			total += int64(w)
			if werr == nil && w < n {
				werr = io.ErrShortWrite
			}
			if werr == nil && flush != nil {
				werr = flush()
			}
			if werr != nil {
				return total, fmt.Errorf("%w: %w", ErrClientWrite, werr)
			}
		}
		if errors.Is(rerr, io.EOF) {
			return total, nil
		}
		if rerr != nil {
			return total, fmt.Errorf("%w: %w", ErrUpstreamRead, rerr)
		}
	}
}
