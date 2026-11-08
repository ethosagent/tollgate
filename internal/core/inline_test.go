package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// wrappedAuth embeds the built-in: it inherits InlineSafe by promotion but
// did not declare it, so it is treated as host code and keeps its deadline.
type wrappedAuth struct{ model.NoAuth }

// markedAuth is a host Authenticator that declares InlineSafe itself.
type markedAuth struct{}

func (markedAuth) Authenticate(context.Context, *http.Request) (model.Tenant, error) {
	return model.Tenant{ID: "m"}, nil
}
func (markedAuth) InlineSafe() {}

// markedResolver declares InlineSafe with a pointer receiver.
type markedResolver struct{}

func (*markedResolver) Resolve(context.Context, model.Tenant, *http.Request) (model.Provider, string, error) {
	return model.Provider{}, "", errors.New("none")
}
func (*markedResolver) InlineSafe() {}

// markedWrapper embeds a marked admission and declares nothing.
type markedWrapper struct{ model.Unlimited }

func TestInlineSeams_OnlyDeclaredInlineSafeRunsInline(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want inlineSeams
	}{
		{"defaults", Options{}, inlineSeams{auth: true, admission: true, resolver: true}},
		{"pointer built-ins", Options{Auth: &model.NoAuth{TenantID: "t"}, Admission: &model.Unlimited{}, Providers: resolve.New(nil)},
			inlineSeams{auth: true, admission: true, resolver: true}},
		{"host auth wrapping NoAuth", Options{Auth: wrappedAuth{}}, inlineSeams{admission: true, resolver: true}},
		{"host admission wrapping Unlimited", Options{Admission: markedWrapper{}}, inlineSeams{auth: true, resolver: true}},
		{"host admission", Options{Admission: blockingAdmission{}}, inlineSeams{auth: true, resolver: true}},
		{"host auth declaring InlineSafe", Options{Auth: markedAuth{}}, inlineSeams{auth: true, admission: true, resolver: true}},
		{"host auth declaring InlineSafe, by pointer", Options{Auth: &markedAuth{}}, inlineSeams{auth: true, admission: true, resolver: true}},
		{"host resolver declaring InlineSafe", Options{Providers: &markedResolver{}}, inlineSeams{auth: true, admission: true, resolver: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inlineSeamsOf(tt.opts.withDefaults()); got != tt.want {
				t.Fatalf("inline = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestProxy_InlineSafeHostAuthServesTheCall: a host Authenticator that
// opts in runs inline and its tenant reaches the call (and the upstream).
func TestProxy_InlineSafeHostAuthServesTheCall(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer up.Close()
	base, _ := url.Parse(up.URL)
	p, err := New(Options{Auth: markedAuth{}, Providers: resolve.New(map[string]model.Provider{
		"openai": {Name: "openai", Wire: model.WirePassthrough, BaseURL: base},
	})})
	if err != nil {
		t.Fatal(err)
	}
	if !p.inline.auth {
		t.Fatal("a host Authenticator declaring InlineSafe did not run inline")
	}
	r := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	w := httptest.NewRecorder()
	p.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
}

type blockingAdmission struct{}

func (blockingAdmission) Acquire(ctx context.Context, _ model.Tenant, _ *model.Call) (func(), error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestGuarded_InlineRecoversAndHostKeepsItsDeadline: inline still turns a
// panic into ErrPanic; a host call that ignores the deadline is cut by it.
func TestGuarded_InlineRecoversAndHostKeepsItsDeadline(t *testing.T) {
	ctx := context.Background()
	_, err := guarded(ctx, true, time.Hour, func(context.Context) (int, error) { panic("boom") })
	if !chain.IsPanic(err) {
		t.Fatalf("inline panic: err = %v, want ErrPanic", err)
	}
	stuck := make(chan struct{})
	defer close(stuck)
	start := time.Now()
	_, err = guarded(ctx, false, 20*time.Millisecond, func(context.Context) (int, error) { <-stuck; return 1, nil })
	if !errors.Is(err, chain.ErrDeadline) || time.Since(start) > time.Second {
		t.Fatalf("host call: err = %v after %v, want ErrDeadline at its deadline", err, time.Since(start))
	}
	if v, err := guarded(ctx, true, time.Hour, func(context.Context) (int, error) { return 7, nil }); v != 7 || err != nil {
		t.Fatalf("inline = %d, %v", v, err)
	}
}

// TestProxy_HostAdmissionStillAnsweredAtItsDeadline: with built-in auth and
// resolver inline, a host Admission that never returns is still cut at
// AdmissionDeadline (non-negotiable 2).
func TestProxy_HostAdmissionStillAnsweredAtItsDeadline(t *testing.T) {
	p, err := New(Options{Admission: blockingAdmission{}, Limits: Limits{AdmissionDeadline: 30 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/openai/v1/chat/completions", nil)
	w := &statusRecorder{h: http.Header{}}
	p.Handler().ServeHTTP(w, r)
	if w.status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 at the admission deadline", w.status)
	}
}

type statusRecorder struct {
	h      http.Header
	status int
}

func (w *statusRecorder) Header() http.Header         { return w.h }
func (w *statusRecorder) WriteHeader(s int)           { w.status = s }
func (w *statusRecorder) Write(p []byte) (int, error) { return len(p), nil }
