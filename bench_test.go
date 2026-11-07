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
