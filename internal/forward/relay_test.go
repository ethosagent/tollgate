package forward

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// recWriter is a ResponseWriter that records writes and flushes.
type recWriter struct {
	buf      bytes.Buffer
	flushes  int
	flushErr error
	failAt   int // fail the Nth write (1-based); 0 never
	writes   int
	short    bool
}

func (w *recWriter) Header() http.Header { return http.Header{} }
func (w *recWriter) WriteHeader(int)     {}
func (w *recWriter) FlushError() error   { w.flushes++; return w.flushErr }

func (w *recWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.failAt == w.writes {
		return 0, errors.New("client gone")
	}
	if w.short {
		return w.buf.Write(p[:len(p)-1])
	}
	return w.buf.Write(p)
}

// chunkReader returns one scripted chunk per Read, then err.
type chunkReader struct {
	chunks []string
	err    error
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, r.err
	}
	n := copy(p, r.chunks[0])
	if r.chunks[0] = r.chunks[0][n:]; r.chunks[0] == "" { // a chunk larger than p is read over several calls
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

func TestRelay_FlushesAfterEveryRead(t *testing.T) {
	w := &recWriter{}
	src := &chunkReader{chunks: []string{"data: 1\n\n", "data: 2\n\n", "data: 3\n\n"}, err: io.EOF}
	n, err := Relay(w, src)
	if err != nil || n != 27 || w.buf.String() != "data: 1\n\ndata: 2\n\ndata: 3\n\n" {
		t.Fatalf("n=%d err=%v out=%q", n, err, w.buf.String())
	}
	if w.flushes != 3 {
		t.Fatalf("flushes = %d, want 3 (one per read)", w.flushes)
	}
}

func TestRelay_Failures(t *testing.T) {
	boom := errors.New("reset")
	tests := []struct {
		name    string
		w       *recWriter
		src     io.Reader
		want    error
		wantOut string
	}{
		{"upstream drop keeps partial bytes", &recWriter{}, &chunkReader{chunks: []string{"abc"}, err: boom}, ErrUpstreamRead, "abc"},
		{"client write error", &recWriter{failAt: 1}, strings.NewReader("abc"), ErrClientWrite, ""},
		{"client short write", &recWriter{short: true}, strings.NewReader("abc"), ErrClientWrite, "ab"},
		{"client flush error", &recWriter{flushErr: boom}, strings.NewReader("abc"), ErrClientWrite, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Relay(tt.w, tt.src)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.w.buf.String() != tt.wantOut {
				t.Fatalf("out = %q, want %q", tt.w.buf.String(), tt.wantOut)
			}
		})
	}
}

// discardWriter is a ResponseWriter that cannot flush and drops every byte.
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) WriteHeader(int)             {}
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestRelay_WriterWithoutFlushStillCopies(t *testing.T) {
	if n, err := Relay(discardWriter{}, strings.NewReader("abc")); err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func BenchmarkRelay(b *testing.B) {
	data := bytes.Repeat([]byte("data: x\n\n"), 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := Relay(discardWriter{}, bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestRelayBuf_SmallUntilARead_FillsIt: a stream reads into a small buffer
// until one read fills it; from then on (bulk) it reads into a big one.
func TestRelayBuf_SmallUntilAReadFillsIt(t *testing.T) {
	rb := newRelayBuf()
	defer rb.release()
	if len(rb.bytes()) != smallRelayBufSize {
		t.Fatalf("first buffer = %d bytes, want %d", len(rb.bytes()), smallRelayBufSize)
	}
	rb.read(100)
	if len(rb.bytes()) != smallRelayBufSize {
		t.Fatal("a short read moved to the big buffer")
	}
	rb.read(smallRelayBufSize)
	if len(rb.bytes()) != relayBufSize {
		t.Fatalf("after a full read: %d bytes, want %d", len(rb.bytes()), relayBufSize)
	}
	rb.read(10)
	if len(rb.bytes()) != relayBufSize {
		t.Fatal("a big buffer went back to small mid-response")
	}
}

// TestTee_BulkBodyCrossesTheBufferSwitchByteExact: a body far over the
// small buffer, read whole-buffer at a time, arrives unchanged and its SSE
// frames all reach the queue across the switch.
func TestTee_BulkBodyCrossesTheBufferSwitchByteExact(t *testing.T) {
	var body bytes.Buffer
	for i := range 2000 {
		fmt.Fprintf(&body, "data: {\"n\":%d,\"pad\":%q}\n\n", i, strings.Repeat("x", i%97))
	}
	rec := httptest.NewRecorder()
	q := NewFrameQueue(4096)
	if _, err := Tee(rec, bytes.NewReader(body.Bytes()), q, nil); err != nil {
		t.Fatal(err)
	}
	q.finish(model.FinishOK)
	if !bytes.Equal(rec.Body.Bytes(), body.Bytes()) {
		t.Fatal("relayed body differs")
	}
	if got := readAllFrames(q); len(got) != 2000 || got[1999].frame != fmt.Sprintf("data: {\"n\":1999,\"pad\":%q}", strings.Repeat("x", 1999%97)) {
		t.Fatalf("frames = %d, last = %+v", len(got), got[len(got)-1])
	}
}
