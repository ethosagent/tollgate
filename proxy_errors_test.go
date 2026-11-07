package tollgate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

type statusErr struct{ status int }

func (e statusErr) Error() string   { return "denied" }
func (e statusErr) HTTPStatus() int { return e.status }

type retryErr struct{ after time.Duration }

func (e retryErr) Error() string             { return "busy" }
func (e retryErr) RetryAfter() time.Duration { return e.after }

type failAuth struct{ err error }

func (a failAuth) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	return tollgate.Tenant{}, a.err
}

// countingAdmission counts Acquire and release calls; err rejects.
type countingAdmission struct {
	acquired, released atomic.Int32
	err                error
}

func (a *countingAdmission) Acquire(context.Context, tollgate.Tenant, *tollgate.Call) (func(), error) {
	a.acquired.Add(1)
	if a.err != nil {
		return nil, a.err
	}
	return func() { a.released.Add(1) }, nil
}

func errorBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readAll(t, resp), &m); err != nil {
		t.Fatalf("error body is not JSON: %v", err)
	}
	return m
}

func TestProxy_UnknownProviderIsProviderShaped404(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	// A resolver that knows no provider: the 404 shape follows the path prefix.
	base := newProxyServer(t, tollgate.Options{Providers: noProviders{}})
	tests := []struct {
		path, wantTyp string
		anthropic     bool
	}{
		{"/openai/v1/chat/completions", "invalid_request_error", false},
		{"/anthropic/v1/messages", "not_found_error", true},
		{"/mystery/v1/x", "invalid_request_error", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp := post(t, base, tt.path, []byte(`{}`), nil)
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			body := errorBody(t, resp)
			e := body["error"].(map[string]any)
			if e["type"] != tt.wantTyp {
				t.Fatalf("body = %v", body)
			}
			if _, ok := body["type"]; ok != tt.anthropic {
				t.Fatalf("anthropic envelope present = %v, want %v (%v)", ok, tt.anthropic, body)
			}
		})
	}
	if n := len(up.Requests()); n != 0 {
		t.Fatalf("upstream contacted %d times", n)
	}
}

type noProviders struct{}

func (noProviders) Resolve(context.Context, tollgate.Tenant, *http.Request) (tollgate.Provider, string, error) {
	return tollgate.Provider{}, "", errors.New("no such provider")
}

func TestProxy_AuthErrors(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		want int
	}{
		"plain error is 401":       {errors.New("bad key"), 401},
		"forbidden error is 403":   {statusErr{403}, 403},
		"non-error status is 401":  {statusErr{200}, 401},
		"wrapped forbidden is 403": {errors.Join(errors.New("ctx"), statusErr{403}), 403},
	} {
		t.Run(name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{})
			adm := &countingAdmission{}
			base := newProxyServer(t, tollgate.Options{Auth: failAuth{tt.err}, Admission: adm})
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), via(up))
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if body := errorBody(t, resp); body["error"] == nil {
				t.Fatalf("not provider-shaped: %v", body)
			}
			if len(up.Requests()) != 0 || adm.acquired.Load() != 0 {
				t.Fatalf("auth failure must stop before admission and upstream")
			}
		})
	}
}

func TestProxy_AdmissionRejectsWith429AndRetryAfter(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		want string
	}{"default is 1s": {errors.New("full"), "1"}, "from error rounds up": {retryErr{2500 * time.Millisecond}, "3"}} {
		t.Run(name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{})
			adm := &countingAdmission{err: tt.err}
			base := newProxyServer(t, tollgate.Options{Admission: adm})
			resp := post(t, base, "/anthropic/v1/messages", []byte(`{}`), via(up))
			body := errorBody(t, resp)
			if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != tt.want {
				t.Fatalf("status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
			}
			if body["type"] != "error" {
				t.Fatalf("want Anthropic shape, got %v", body)
			}
			if len(up.Requests()) != 0 {
				t.Fatal("upstream contacted after admission reject")
			}
		})
	}
}

func TestProxy_ReleaseCalledExactlyOnce(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		fake testkit.Options
		opts tollgate.Options
		want int
	}{
		{"success", "/openai/v1/chat/completions", `{}`, testkit.Options{}, tollgate.Options{}, 200},
		{"upstream 500", "/openai/v1/chat/completions", `{}`, testkit.Options{Status: 500}, tollgate.Options{}, 500},
		{"body too large", "/openai/v1/chat/completions", strings.Repeat("x", 64), testkit.Options{}, tollgate.Options{Limits: tollgate.Limits{MaxRequestBody: 8}}, 413},
		{"unknown provider", "/nope/v1/x", `{}`, testkit.Options{}, tollgate.Options{}, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, tt.fake)
			adm := &countingAdmission{}
			tt.opts.Admission = adm
			base := newProxyServer(t, tt.opts)
			resp := post(t, base, tt.path, []byte(tt.body), via(up))
			_ = readAll(t, resp)
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if adm.acquired.Load() != 1 || adm.released.Load() != 1 {
				t.Fatalf("acquired=%d released=%d, want 1/1", adm.acquired.Load(), adm.released.Load())
			}
		})
	}
}

func TestProxy_BodyOverLimitIs413BeforeUpstream(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	base := newProxyServer(t, tollgate.Options{Limits: tollgate.Limits{MaxRequestBody: 1024}})
	big := []byte(strings.Repeat("a", 2048))

	t.Run("declared length", func(t *testing.T) {
		resp := post(t, base, "/openai/v1/chat/completions", big, via(up))
		_ = readAll(t, resp)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("chunked, no length", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, base+"/openai/v1/chat/completions", io.NopCloser(strings.NewReader(string(big))))
		req.Header.Set("x-upstream-url", up.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = readAll(t, resp)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("exactly at the limit passes", func(t *testing.T) {
		resp := post(t, base, "/openai/v1/chat/completions", big[:1024], via(up))
		_ = readAll(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	if n := len(up.Requests()); n != 1 {
		t.Fatalf("upstream saw %d requests, want only the at-limit one", n)
	}
}

func TestProxy_UpstreamOverrideAllowlist(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	tenant := func(allow ...string) tollgate.Tenant {
		return tollgate.Tenant{ID: "t", Policy: &tollgate.Policy{AllowedUpstreams: allow}}
	}
	tests := []struct {
		name     string
		tenant   tollgate.Tenant
		override string
		wantUp   bool // the fake upstream receives the call
		wantCode int
	}{
		{"cidr match", tenant("127.0.0.0/8"), up.URL, true, 200},
		{"exact host match", tenant("127.0.0.1"), up.URL, true, 200},
		{"host mismatch", tenant("10.0.0.0/8", "other.example"), up.URL, false, 403},
		{"invalid override", tenant("127.0.0.1"), "ftp://127.0.0.1", false, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(up.Requests())
			base := newProxyServer(t, tollgate.Options{Auth: policyAuth{tt.tenant}})
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), map[string]string{"x-upstream-url": tt.override})
			_ = readAll(t, resp)
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantCode)
			}
			if got := len(up.Requests()) - before; (got == 1) != tt.wantUp {
				t.Fatalf("upstream received %d calls, wantUp=%v", got, tt.wantUp)
			}
		})
	}
}

// TestProxy_UpstreamOverrideIgnoredWithoutPolicy points the default provider
// at an unreachable port: with no policy the header is ignored, so the call
// goes to the provider's own base URL and never reaches the fake.
func TestProxy_UpstreamOverrideIgnoredWithoutPolicy(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	var dialed atomic.Bool
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		dialed.Store(r.URL.Host == "api.openai.com")
		return nil, errors.New("blocked in test")
	})
	for name, auth := range map[string]tollgate.Authenticator{
		"no policy":       policyAuth{tollgate.Tenant{ID: "t"}},
		"empty allowlist": policyAuth{tollgate.Tenant{ID: "t", Policy: &tollgate.Policy{}}},
	} {
		t.Run(name, func(t *testing.T) {
			dialed.Store(false)
			base := newProxyServer(t, tollgate.Options{Auth: auth, Transport: rt})
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), via(up))
			_ = readAll(t, resp)
			if !dialed.Load() || len(up.Requests()) != 0 {
				t.Fatalf("header was honoured without a policy (dialed default=%v, fake saw %d)", dialed.Load(), len(up.Requests()))
			}
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502 from the blocked transport", resp.StatusCode)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
