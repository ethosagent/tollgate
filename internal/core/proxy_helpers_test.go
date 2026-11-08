package core_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

// policyAuth authenticates every request as one tenant that may target the
// fake upstream through the upstream-override header.
type policyAuth struct{ tenant model.Tenant }

func (a policyAuth) Authenticate(context.Context, *http.Request) (model.Tenant, error) {
	return a.tenant, nil
}

func allowLocal(id string) policyAuth {
	return policyAuth{model.Tenant{ID: id, Policy: &model.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}}
}

// newProxyServer starts a tollgate proxy whose tenant may use the
// upstream-override header. It returns the proxy's base URL.
func newProxyServer(t testing.TB, opts core.Options) string {
	t.Helper()
	if opts.Auth == nil {
		opts.Auth = allowLocal("acme")
	}
	p, err := core.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return serve(t, p)
}

// serve starts p's handler on a test server and returns its base URL.
func serve(t testing.TB, p *core.Proxy) string {
	t.Helper()
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// via returns request headers that aim the default upstream-override header
// at up, plus any extra key/value pairs.
func via(up *fakeUpstream, kv ...string) map[string]string {
	h := map[string]string{"x-upstream-url": up.URL}
	for i := 0; i+1 < len(kv); i += 2 {
		h[kv[i]] = kv[i+1]
	}
	return h
}

// post sends body to base+path with the given headers.
func post(t testing.TB, base, path string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	return postCtx(context.Background(), t, base, path, body, hdr)
}

// postCtx is post under ctx, so a test can cancel the client.
func postCtx(ctx context.Context, t testing.TB, base, path string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func readAll(t testing.TB, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// pathFor maps a fixture name to the proxied path that serves its provider.
func pathFor(fixture string) string {
	provider, _, _ := strings.Cut(fixture, "/")
	return "/" + provider + fixtures.PathFor(fixture)
}

// syncBuf is a goroutine-safe buffer for capturing logs.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
