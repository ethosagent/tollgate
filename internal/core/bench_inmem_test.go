package core_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
)

// memUpstream answers every request with a streamed fixture from memory: no
// socket, no scheduler hand-off, so a benchmark through it is tollgate's own
// CPU per call.
type memUpstream struct{ body []byte }

func (u memUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(bytes.NewReader(u.body)), Request: r}, nil
}

// flushRecorder discards the response; it flushes for free.
type flushRecorder struct{ h http.Header }

func (w *flushRecorder) Header() http.Header         { return w.h }
func (w *flushRecorder) WriteHeader(int)             {}
func (w *flushRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (w *flushRecorder) FlushError() error           { return nil }

// benchInMemory serves streamed calls straight into p's handler with an
// in-memory upstream: the pipeline's own cost per call (HOT-7), without the
// loopback sockets and goroutine hand-offs the BenchmarkCall_* rigs include.
func benchInMemory(b *testing.B, opts core.Options) {
	body := bytes.Join(fixtures.Chunks(streamFixture), nil)
	opts.Transport = memUpstream{body: body}
	p, err := core.New(opts)
	if err != nil {
		b.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = p.Close(context.Background()) })
	h := p.Handler()
	req := requestBody(b, streamFixture)
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader(req))
		if opts.Auth != nil { // the built-in NoAuth tenant may not override; the default target never leaves memory
			r.Header.Set("x-upstream-url", "http://127.0.0.1:9")
		}
		h.ServeHTTP(&flushRecorder{h: http.Header{}}, r)
	}
}

// BenchmarkServe_InMemory: the zero-hook path with a host Authenticator.
func BenchmarkServe_InMemory(b *testing.B) {
	benchInMemory(b, core.Options{Auth: allowLocal("bench")})
}

// BenchmarkServe_InMemoryWithSink adds a started sink: parse, session, tap
// consumer and the Metric.
func BenchmarkServe_InMemoryWithSink(b *testing.B) {
	p := core.Options{Auth: allowLocal("bench"), Metrics: nopSink{}}
	benchInMemory(b, p)
}

// BenchmarkServe_InMemoryBuiltins: every seam is a built-in (NoAuth,
// Unlimited, the prefix resolver), so none needs a deadline goroutine.
func BenchmarkServe_InMemoryBuiltins(b *testing.B) {
	benchInMemory(b, core.Options{})
}
