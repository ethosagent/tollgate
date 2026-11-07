package tollgate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNew_ZeroOptionsGetDefaults(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.opts.Auth == nil || p.opts.Admission == nil || p.opts.Metrics == nil || p.opts.Logger == nil || p.opts.Transport == nil {
		t.Fatalf("defaults not applied: %+v", p.opts)
	}
	if p.opts.Limits != DefaultLimits() {
		t.Fatalf("limits = %+v, want defaults", p.opts.Limits)
	}
	if p.opts.Headers != DefaultHeaderNames() {
		t.Fatalf("headers = %+v, want defaults", p.opts.Headers)
	}
}

func TestNew_KeepsCallerTransport(t *testing.T) {
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	p, err := New(Options{Transport: rt})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := p.opts.Transport.(roundTripFunc); !ok {
		t.Fatalf("transport replaced: %T", p.opts.Transport)
	}
}

func TestNew_RejectsInvalidOptions(t *testing.T) {
	_, err := New(Options{Limits: Limits{MaxRequestBody: -1}})
	if err == nil || !strings.Contains(err.Error(), "MaxRequestBody") {
		t.Fatalf("err = %v, want MaxRequestBody error", err)
	}
}

func TestUse_AppendsInOrderAndSkipsNil(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	p.Use(namedHook{name: "a"}, nil)
	p.Use(namedHook{name: "b"})
	got := p.snapshotHooks()
	if len(got) != 2 || got[0].Name() != "a" || got[1].Name() != "b" {
		t.Fatalf("hooks = %v", got)
	}
}

func TestHandler_Health(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
}

func TestHandler_ProxyPathReturnsProviderShaped501(t *testing.T) {
	p, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`))
	p.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Error struct{ Type, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Error.Type != "not_implemented" {
		t.Fatalf("error type = %q", body.Error.Type)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

type namedHook struct {
	NoopHook
	name string
}

func (h namedHook) Name() string { return h.name }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
