package forward

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// nullWriter is a ResponseWriter that discards everything and flushes for free.
type nullWriter struct{ h http.Header }

func (w *nullWriter) Header() http.Header         { return w.h }
func (w *nullWriter) WriteHeader(int)             {}
func (w *nullWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *nullWriter) FlushError() error           { return nil }

// BenchmarkTee measures one chunk (one typical SSE event per read) through the
// tee: allocs/op is allocations per chunk. "plain" has no consumer, "tapped"
// scans each chunk into a queue that is drained.
func BenchmarkTee(b *testing.B) {
	chunk := []byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n")
	for _, tc := range []struct {
		name   string
		tapped bool
	}{{"plain", false}, {"tapped", true}} {
		b.Run(tc.name, func(b *testing.B) {
			src := bytes.NewReader(bytes.Repeat(chunk, b.N))
			w := &nullWriter{h: http.Header{}}
			var q *FrameQueue
			if tc.tapped {
				q = NewFrameQueue(b.N + 1)
			}
			b.SetBytes(int64(len(chunk)))
			b.ReportAllocs()
			b.ResetTimer()
			// One Tee call for b.N chunks would read 32 KiB per read; feed one
			// chunk per read like a live stream instead.
			if _, err := Tee(w, oneChunkPerRead{src: src, size: len(chunk)}, q, nil); err != nil {
				b.Fatal(err)
			}
		})
	}
}

// oneChunkPerRead returns at most size bytes per Read.
type oneChunkPerRead struct {
	src  io.Reader
	size int
}

func (r oneChunkPerRead) Read(p []byte) (int, error) { return r.src.Read(p[:min(len(p), r.size)]) }
