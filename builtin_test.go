package tollgate

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/chain"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// wrappedAuth is a host Authenticator that embeds the built-in: it is host
// code, so it must keep its deadline goroutine.
type wrappedAuth struct{ NoAuth }

func TestBuiltinSeams_OnlyExactBuiltinsRunInline(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want inlineSeams
	}{
		{"defaults", Options{}, inlineSeams{auth: true, admission: true, resolver: true}},
		{"pointer built-ins", Options{Auth: &NoAuth{TenantID: "t"}, Admission: &Unlimited{}, Providers: resolve.New(nil)},
			inlineSeams{auth: true, admission: true, resolver: true}},
		{"host auth wrapping NoAuth", Options{Auth: wrappedAuth{}}, inlineSeams{admission: true, resolver: true}},
		{"host admission", Options{Admission: blockingAdmission{}}, inlineSeams{auth: true, resolver: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := builtinSeams(tt.opts.withDefaults()); got != tt.want {
				t.Fatalf("inline = %+v, want %+v", got, tt.want)
			}
		})
	}
}

type blockingAdmission struct{}

func (blockingAdmission) Acquire(ctx context.Context, _ Tenant, _ *Call) (func(), error) {
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
