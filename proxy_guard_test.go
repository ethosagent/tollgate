package tollgate_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

type panicAuth struct{}

func (panicAuth) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	panic("auth-panic-detail")
}

type panicAdmission struct{}

func (panicAdmission) Acquire(context.Context, tollgate.Tenant, *tollgate.Call) (func(), error) {
	panic("admission-panic-detail")
}

type panicResolver struct{}

func (panicResolver) Resolve(context.Context, tollgate.Tenant, *http.Request) (tollgate.Provider, string, error) {
	panic("resolver-panic-detail")
}

// TestProxy_PanickingExtensionIsA500 covers STR-4: a panic in Auth,
// Admission or the resolver is recovered, answered with a provider-shaped
// 500 and logged with metadata only.
func TestProxy_PanickingExtensionIsA500(t *testing.T) {
	tests := []struct {
		name, seam string
		opts       tollgate.Options
	}{
		{"auth", "auth", tollgate.Options{Auth: panicAuth{}}},
		{"admission", "admission", tollgate.Options{Admission: panicAdmission{}}},
		{"resolver", "resolver", tollgate.Options{Providers: panicResolver{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{})
			logs := &syncBuf{}
			tt.opts.Logger = slog.New(slog.NewTextHandler(logs, nil))
			base := newProxyServer(t, tt.opts)
			resp := post(t, base, "/anthropic/v1/messages", []byte(`{}`), via(up))
			body := errorBody(t, resp)
			if resp.StatusCode != http.StatusInternalServerError || body["type"] != "error" {
				t.Fatalf("status=%d body=%v, want an Anthropic-shaped 500", resp.StatusCode, body)
			}
			got := logs.String()
			if !strings.Contains(got, "extension panicked") || !strings.Contains(got, "seam="+tt.seam) || strings.Contains(got, "panic-detail") {
				t.Fatalf("log = %q, want a metadata-only panic line for %s", got, tt.seam)
			}
			if len(up.Requests()) != 0 {
				t.Fatal("upstream contacted after a panic")
			}
		})
	}
}

// waitAdmission blocks until the caller's context ends.
type waitAdmission struct{ entered chan struct{} }

func (a waitAdmission) Acquire(ctx context.Context, _ tollgate.Tenant, _ *tollgate.Call) (func(), error) {
	close(a.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestProxy_ClientCancelDuringAdmissionIsNotA429 covers TEST-7/PRIV-8: a
// client that leaves while queued is recorded as client_cancel, not 429.
func TestProxy_ClientCancelDuringAdmissionIsNotA429(t *testing.T) {
	logs := &syncBuf{}
	adm := waitAdmission{entered: make(chan struct{})}
	base := newProxyServer(t, tollgate.Options{Admission: adm, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	// No body: net/http notices a client disconnect only once the request
	// body is consumed, and admission runs before the body is read.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/openai/v1/chat/completions", http.NoBody)
	go func() { <-adm.entered; cancel() }()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("request completed although the client cancelled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "proxy call") {
		if time.Now().After(deadline) {
			t.Fatal("no access log line")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := logs.String(); !strings.Contains(got, "finish=client_cancel") || strings.Contains(got, "status=429") {
		t.Fatalf("log = %q, want client_cancel and no 429", got)
	}
}
