package tollgate_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// BenchmarkPassThrough_Stream measures the proxy's overhead per streamed
// call against an in-process fake upstream (HOT-7).
func BenchmarkPassThrough_Stream(b *testing.B) {
	up := testkit.NewFakeUpstream(b, testkit.Options{})
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("bench")})
	if err != nil {
		b.Fatal(err)
	}
	srv := httptest.NewServer(p.Handler())
	b.Cleanup(srv.Close)
	body := `{"stream":true}`
	b.ReportAllocs()
	for b.Loop() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/openai/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("x-upstream-url", up.URL)
		resp, err := srv.Client().Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// benchCall runs streamed calls through p and reports per-call cost; it is
// how the zero-hook fast path and the hooked path are compared (HOT-7).
func benchCall(b *testing.B, p *tollgate.Proxy) {
	benchCallBody(b, p, string(requestBody(b, streamFixture)))
}

// benchCallBody runs calls with request body through p.
func benchCallBody(b *testing.B, p *tollgate.Proxy, body string) {
	up := testkit.NewFakeUpstream(b, testkit.Options{})
	srv := httptest.NewServer(p.Handler())
	b.Cleanup(srv.Close)
	b.ReportAllocs()
	for b.Loop() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/openai/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("x-upstream-url", up.URL)
		resp, err := srv.Client().Do(req)
		if err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// BenchmarkCall_NoHooks is the zero-hook fast path: no parse, no consumer,
// no After goroutine.
func BenchmarkCall_NoHooks(b *testing.B) {
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("bench")})
	if err != nil {
		b.Fatal(err)
	}
	benchCall(b, p)
}

// BenchmarkCall_OneHook has one hook implementing every method: parse,
// Before, the OnEvent consumer and After.
func BenchmarkCall_OneHook(b *testing.B) {
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("bench")})
	if err != nil {
		b.Fatal(err)
	}
	p.Use(&tHook{name: "all"})
	benchCall(b, p)
}

// nopSink accepts every batch: the benchmarks measure the per-call metric
// path (Registry, queue offer, usage), not a sink.
type nopSink struct{}

func (nopSink) Write(context.Context, []tollgate.Metric) error { return nil }

// sinkBenchProxy is a started proxy with a sink, closed when b ends.
func sinkBenchProxy(b *testing.B) *tollgate.Proxy {
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("bench"), Metrics: nopSink{}})
	if err != nil {
		b.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

// BenchmarkCall_WithSink is a streamed call with a metric sink: the tap
// collects usage, and the Metric is counted and queued.
func BenchmarkCall_WithSink(b *testing.B) { benchCall(b, sinkBenchProxy(b)) }

// BenchmarkCall_StreamFalseWithSink is a stream:false call with a sink:
// the response is copied (BodyCopy) to read its usage.
func BenchmarkCall_StreamFalseWithSink(b *testing.B) {
	benchCallBody(b, sinkBenchProxy(b), plainRequest)
}

// BenchmarkCall_StreamFalseNoHooks is the same call with nothing reading
// it: the baseline for the copy's cost.
func BenchmarkCall_StreamFalseNoHooks(b *testing.B) {
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("bench")})
	if err != nil {
		b.Fatal(err)
	}
	benchCallBody(b, p, plainRequest)
}
