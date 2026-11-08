package tollgate_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// These tests hold the public contract of the root package: every Proxy
// method delegates to internal/core, whose own tests cover the pipeline
// (ADR 0016). Here each wrapper is driven the way a host drives it.

// localAuth authenticates every request as a tenant that may aim the
// upstream-override header at a loopback fake upstream.
type localAuth struct{}

func (localAuth) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	return tollgate.Tenant{ID: "acme", Policy: &tollgate.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}, nil
}

// afterHook records the Metric of every call.
type afterHook struct {
	tollgate.NoopHook
	mu  sync.Mutex
	got []tollgate.Metric
}

func (h *afterHook) Name() string { return "after" }

func (h *afterHook) After(_ context.Context, _ *tollgate.Call, m tollgate.Metric) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.got = append(h.got, m)
}

func (h *afterHook) metrics() []tollgate.Metric {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tollgate.Metric(nil), h.got...)
}

type discard struct{}

func (discard) Write(context.Context, []tollgate.Metric) error { return nil }

func newProxy(t *testing.T, opts tollgate.Options) *tollgate.Proxy {
	t.Helper()
	p, err := tollgate.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	return p
}

func status(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

func TestNew_ZeroOptionsServesHealth(t *testing.T) {
	p := newProxy(t, tollgate.Options{})
	if p.Handler() != p.Handler() {
		t.Fatal("Handler returned a different handler on a second call")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		if got := status(t, p.Handler(), path); got != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, got)
		}
	}
}

func TestNew_RejectsInvalidOptions(t *testing.T) {
	p, err := tollgate.New(tollgate.Options{Limits: tollgate.Limits{MaxRequestBody: -1}})
	if p != nil || err == nil {
		t.Fatalf("New = %v, %v; want nil and an error", p, err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "tollgate: invalid options: ") || !strings.Contains(msg, "MaxRequestBody") {
		t.Fatalf("err = %q", msg)
	}
}

func TestProxy_StreamsByteExactAndRunsHooks(t *testing.T) {
	const fixture = "openai/chat-stream-text"
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	p := newProxy(t, tollgate.Options{Auth: localAuth{}})
	h := &afterHook{}
	p.Use(h, nil) // a nil hook is ignored
	srv := httptest.NewServer(p.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/openai"+testkit.PathFor(fixture), strings.NewReader(`{"stream":true}`))
	req.Header.Set("x-upstream-url", up.URL)
	req.Header.Set(testkit.FixtureHeader, fixture)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	want, _ := testkit.Fixture(fixture)
	if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
		t.Fatalf("status %d, err %v; body differs from the fixture: %q", resp.StatusCode, err, got)
	}
	deadline := time.Now().Add(5 * time.Second) // After runs once the handler returns
	for len(h.metrics()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(h.metrics()); n != 1 {
		t.Fatalf("After ran %d times, want 1", n)
	}
	if m := h.metrics()[0]; m.TenantID != "acme" || m.Finish != tollgate.FinishOK || !m.Stream {
		t.Fatalf("metric = %+v", m)
	}
}

func TestProxy_StartCloseLifecycle(t *testing.T) {
	p := newProxy(t, tollgate.Options{Metrics: discard{}})
	if got := status(t, p.Handler(), "/readyz"); got != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before Start = %d, want 503", got)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := status(t, p.Handler(), "/readyz"); got != http.StatusOK {
		t.Fatalf("/readyz after Start = %d, want 200", got)
	}
	for range 2 { // Close is safe to call more than once
		if err := p.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if err := p.Start(context.Background()); !errors.Is(err, tollgate.ErrClosed) || err.Error() != "tollgate: proxy is closed" {
		t.Fatalf("Start after Close = %v, want ErrClosed", err)
	}
}

func TestProxy_AdminHandler(t *testing.T) {
	p := newProxy(t, tollgate.Options{})
	a := p.AdminHandler()
	if a != p.AdminHandler() {
		t.Fatal("AdminHandler returned a different handler on a second call")
	}
	for _, path := range []string{"/statusz", "/metrics"} {
		if got := status(t, a, path); got != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, got)
		}
	}
	if got := status(t, p.Handler(), "/statusz"); got == http.StatusOK {
		t.Fatal("/statusz is served on the client handler")
	}
}
