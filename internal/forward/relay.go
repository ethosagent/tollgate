package forward

import (
	"errors"
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

// Relay read buffers. A stream starts on a small one: SSE events are far
// smaller, and an open stream pins its buffer for the stream's life, so the
// small size is most of what a stream costs here. A read that fills it means
// the upstream is sending bulk (a large JSON body); the relay then moves to a
// big buffer, for fewer write+flush cycles, for the rest of the response.
const (
	smallRelayBufSize = 4 << 10
	relayBufSize      = 32 << 10
)

var (
	smallRelayBufs = sync.Pool{New: func() any { b := make([]byte, smallRelayBufSize); return &b }}
	relayBufs      = sync.Pool{New: func() any { b := make([]byte, relayBufSize); return &b }}
)

// relayBuf is one response's pooled read buffer. Keep it on the stack; call
// release when the response ends.
type relayBuf struct {
	p   *[]byte
	big bool
}

func newRelayBuf() relayBuf { return relayBuf{p: smallRelayBufs.Get().(*[]byte)} }

// bytes is the buffer to read into next.
func (b *relayBuf) bytes() []byte { return *b.p }

// read records a read of n bytes into bytes(); one that filled the small
// buffer moves the response to a big one. Call it only once those n bytes
// are used: the small buffer goes back to the pool.
func (b *relayBuf) read(n int) {
	if b.big || n < len(*b.p) {
		return
	}
	smallRelayBufs.Put(b.p)
	b.p, b.big = relayBufs.Get().(*[]byte), true
}

func (b *relayBuf) release() {
	if b.big {
		relayBufs.Put(b.p)
	} else {
		smallRelayBufs.Put(b.p)
	}
}

// Relay copies src to dst and flushes dst after every non-empty read, so a
// streamed chunk reaches the client as soon as it arrives. A dst that cannot
// flush is written without flushing. It reuses pooled buffers and returns the
// bytes written. A nil error means src hit EOF; a failure wraps
// ErrClientWrite or ErrUpstreamRead.
func Relay(dst http.ResponseWriter, src io.Reader) (int64, error) {
	return Tee(dst, src, nil, nil)
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
