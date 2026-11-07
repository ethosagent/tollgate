package tollgate_test

import (
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
	up := testkit.NewFakeUpstream(b, testkit.Options{})
	srv := httptest.NewServer(p.Handler())
	b.Cleanup(srv.Close)
	body := string(requestBody(b, streamFixture))
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
