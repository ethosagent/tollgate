package core_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

// eventually polls cond until it holds or a 5 s deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestProxy_PreservesEscapedPath(t *testing.T) {
	var got string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.URL.EscapedPath() + "?" + r.URL.RawQuery
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
	})
	base := newProxyServer(t, core.Options{Transport: rt})
	resp := post(t, base, "/openrouter/v1/files/a%2Fb%20c?q=a%2Fb", []byte(`{}`), nil)
	_ = readAll(t, resp)
	if want := "/api/v1/files/a%2Fb%20c?q=a%2Fb"; got != want {
		t.Fatalf("upstream path = %q, want %q", got, want)
	}
}

// ctxHandler is a slog handler that, like many trace/export wrappers, drops
// records whose context is already done.
type ctxHandler struct {
	mu    sync.Mutex
	lines []string
}

func (h *ctxHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *ctxHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *ctxHandler) WithGroup(string) slog.Handler            { return h }
func (h *ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx.Err() != nil {
		return nil
	}
	var finish string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "finish" {
			finish = a.Value.String()
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, finish)
	return nil
}

func (h *ctxHandler) has(finish string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, l := range h.lines {
		if l == finish {
			return true
		}
	}
	return false
}

// streamThenCancel opens a slow stream through base, reads the first chunk and
// disconnects.
func streamThenCancel(t *testing.T, base string, up *fakeUpstream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("x-upstream-url", up.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, len(fixtures.Chunks(streamFixture)[0]))); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
}

func TestProxy_LogsClientCancelDespiteCanceledContext(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{ChunkDelay: 30 * time.Second})
	h := &ctxHandler{}
	base := newProxyServer(t, core.Options{Logger: slog.New(h)})
	streamThenCancel(t, base, up)
	eventually(t, "a client_cancel log line", func() bool { return h.has(string(model.FinishClientCancel)) })
}

func TestProxy_ReleaseCalledOnceOnAbortExits(t *testing.T) {
	t.Run("client disconnect", func(t *testing.T) {
		up := newFakeUpstream(t, upstreamOptions{ChunkDelay: 30 * time.Second})
		adm := &countingAdmission{}
		base := newProxyServer(t, core.Options{Admission: adm})
		streamThenCancel(t, base, up)
		eventually(t, "release", func() bool { return adm.released.Load() == 1 })
		if adm.acquired.Load() != 1 || adm.released.Load() != 1 {
			t.Fatalf("acquired=%d released=%d", adm.acquired.Load(), adm.released.Load())
		}
	})
	t.Run("upstream drop mid-stream ends with an error event", func(t *testing.T) {
		up := newFakeUpstream(t, upstreamOptions{FailAfterChunks: 2})
		adm := &countingAdmission{}
		base := newProxyServer(t, core.Options{Admission: adm})
		resp := post(t, base, "/openai/v1/chat/completions", []byte(`{"stream":true}`), via(up))
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || !strings.Contains(string(body), `"error"`) {
			t.Fatalf("want a clean end with an error event, got %q %v", body, err)
		}
		eventually(t, "release", func() bool { return adm.released.Load() == 1 })
	})
	t.Run("upstream drop mid JSON body cuts the client", func(t *testing.T) {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"a":`))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}))
		t.Cleanup(up.Close)
		adm := &countingAdmission{}
		base := newProxyServer(t, core.Options{Admission: adm})
		resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), map[string]string{"x-upstream-url": up.URL})
		if _, err := io.ReadAll(resp.Body); err == nil {
			t.Fatal("want a cut body")
		}
		resp.Body.Close()
		eventually(t, "release", func() bool { return adm.released.Load() == 1 })
	})
}

// pathAdmission records the Call.Path it is given.
type pathAdmission struct{ path chan string }

func (a pathAdmission) Acquire(_ context.Context, _ model.Tenant, c *model.Call) (func(), error) {
	a.path <- c.Path
	return nil, nil
}

func TestProxy_AdmissionSeesInboundPath(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	adm := pathAdmission{make(chan string, 1)}
	base := newProxyServer(t, core.Options{Admission: adm})
	_ = readAll(t, post(t, base, "/openai/v1/chat/completions", []byte(`{}`), via(up)))
	if got := <-adm.path; got != "/openai/v1/chat/completions" {
		t.Fatalf("Call.Path = %q, want the inbound path documented on Call", got)
	}
}

func TestProxy_ExtensionStatusErrors(t *testing.T) {
	tests := []struct {
		name  string
		opts  core.Options
		want  int
		retry string
	}{
		{"auth 503 is honoured", core.Options{Auth: failAuth{statusErr{503}}}, 503, ""},
		{"auth 429 is honoured", core.Options{Auth: failAuth{statusErr{429}}}, 429, ""},
		{"admission 503 keeps Retry-After", core.Options{Admission: &countingAdmission{err: statusErr{503}}}, 503, "1"},
		{"resolver status is honoured", core.Options{Providers: errResolver{statusErr{451}}}, 451, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newProxyServer(t, tt.opts)
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{}`), nil)
			_ = readAll(t, resp)
			if resp.StatusCode != tt.want || resp.Header.Get("Retry-After") != tt.retry {
				t.Fatalf("status=%d retry-after=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
			}
		})
	}
}

type errResolver struct{ err error }

func (r errResolver) Resolve(context.Context, model.Tenant, *http.Request) (model.Provider, string, error) {
	return model.Provider{}, "", r.err
}

// fixedResolver is a host-supplied resolver that always returns base.
type fixedResolver struct{ base string }

func (r fixedResolver) Resolve(context.Context, model.Tenant, *http.Request) (model.Provider, string, error) {
	u, err := url.Parse(r.base)
	return model.Provider{Name: "custom", Wire: model.WireOpenAIChat, BaseURL: u}, "/v1/chat/completions", err
}

// TestProxy_TargetValidatedForEveryResolver proves the core, not the
// resolver, guards the upstream target (ADR 0007).
func TestProxy_TargetValidatedForEveryResolver(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	tests := []struct {
		name     string
		resolver model.ProviderResolver
		auth     model.Authenticator
		override string
		want     int
		wantUp   bool
	}{
		{"custom resolver base is used", fixedResolver{up.URL}, nil, "", 200, true},
		{"bad scheme from resolver is 502", fixedResolver{"file:///etc/passwd"}, nil, "", 502, false},
		{"userinfo from resolver is 502", fixedResolver{strings.Replace(up.URL, "//", "//u:p@", 1)}, nil, "", 502, false},
		{"nil base from resolver is 502", nilBaseResolver{}, nil, "", 502, false},
		{"override checked against policy", fixedResolver{"http://127.0.0.1:1"}, policyAuth{model.Tenant{ID: "t", Policy: &model.Policy{AllowedUpstreams: []string{"10.0.0.0/8"}}}}, up.URL, 403, false},
		{"override honoured when allowed", fixedResolver{"http://127.0.0.1:1"}, nil, up.URL, 200, true},
		{"override ignored without policy", fixedResolver{up.URL}, policyAuth{model.Tenant{ID: "t"}}, "http://127.0.0.1:1", 200, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(up.Requests())
			base := newProxyServer(t, core.Options{Providers: tt.resolver, Auth: tt.auth})
			hdr := map[string]string{}
			if tt.override != "" {
				hdr["x-upstream-url"] = tt.override
			}
			resp := post(t, base, "/x/v1/chat/completions", []byte(`{}`), hdr)
			_ = readAll(t, resp)
			if resp.StatusCode != tt.want || (len(up.Requests()) > before) != tt.wantUp {
				t.Fatalf("status=%d (want %d) upstream calls=%d", resp.StatusCode, tt.want, len(up.Requests())-before)
			}
		})
	}
}

type nilBaseResolver struct{}

func (nilBaseResolver) Resolve(context.Context, model.Tenant, *http.Request) (model.Provider, string, error) {
	return model.Provider{Name: "nil"}, "/", nil
}
