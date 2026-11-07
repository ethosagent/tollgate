package forward

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type recWriter struct {
	buf     bytes.Buffer
	flushes int
	failAt  int // fail the Nth write (1-based); 0 never
	writes  int
	short   bool
}

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
	r.chunks = r.chunks[1:]
	return n, nil
}

func TestRelay_FlushesAfterEveryRead(t *testing.T) {
	w := &recWriter{}
	src := &chunkReader{chunks: []string{"data: 1\n\n", "data: 2\n\n", "data: 3\n\n"}, err: io.EOF}
	n, err := Relay(w, func() error { w.flushes++; return nil }, src)
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
		flush   func() error
		src     io.Reader
		want    error
		wantOut string
	}{
		{"upstream drop keeps partial bytes", &recWriter{}, nil, &chunkReader{chunks: []string{"abc"}, err: boom}, ErrUpstreamRead, "abc"},
		{"client write error", &recWriter{failAt: 1}, nil, strings.NewReader("abc"), ErrClientWrite, ""},
		{"client short write", &recWriter{short: true}, nil, strings.NewReader("abc"), ErrClientWrite, "ab"},
		{"client flush error", &recWriter{}, func() error { return boom }, strings.NewReader("abc"), ErrClientWrite, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Relay(tt.w, tt.flush, tt.src)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.w.buf.String() != tt.wantOut {
				t.Fatalf("out = %q, want %q", tt.w.buf.String(), tt.wantOut)
			}
		})
	}
}

func BenchmarkRelay(b *testing.B) {
	data := bytes.Repeat([]byte("data: x\n\n"), 1000)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		if _, err := Relay(io.Discard, func() error { return nil }, bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}
