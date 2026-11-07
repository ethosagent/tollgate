package forward

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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

// Relay copies src to dst and flushes dst after every non-empty read, so a
// streamed chunk reaches the client as soon as it arrives. A dst that cannot
// flush is written without flushing. It reuses pooled buffers and returns the
// bytes written. A nil error means src hit EOF; a failure wraps
// ErrClientWrite or ErrUpstreamRead.
func Relay(dst http.ResponseWriter, src io.Reader) (int64, error) {
	rc := http.NewResponseController(dst)
	bp := relayBufs.Get().(*[]byte)
	defer relayBufs.Put(bp)
	buf := *bp
	var total int64
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			w, werr := writeFlush(dst, rc, buf[:n])
			total += int64(w)
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

// writeFlush writes p in full and flushes; a writer that cannot flush is
// not an error.
func writeFlush(dst io.Writer, rc *http.ResponseController, p []byte) (int, error) {
	w, err := dst.Write(p)
	if err == nil && w < len(p) {
		err = io.ErrShortWrite
	}
	if err == nil {
		if ferr := rc.Flush(); !errors.Is(ferr, http.ErrNotSupported) {
			err = ferr
		}
	}
	return w, err
}
