package core_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/model"
)

// stuck blocks until the test ends, ignoring its context: the worst case of a
// sidecar that hangs.
type stuck struct {
	unblock  chan struct{}
	released *atomic.Int32
}

func (s stuck) Authenticate(context.Context, *http.Request) (model.Tenant, error) {
	<-s.unblock
	return model.Tenant{ID: "late"}, nil
}

func (s stuck) Acquire(context.Context, model.Tenant, *model.Call) (func(), error) {
	<-s.unblock
	return func() { s.released.Add(1) }, nil
}

func (s stuck) Resolve(context.Context, model.Tenant, *http.Request) (model.Provider, string, error) {
	<-s.unblock
	return model.Provider{}, "", nil
}

// TestProxy_StuckExtensionIsBoundedByItsDeadline covers non-negotiable 2: an
// Auth, Admission or resolver call that ignores its context is cut at its
// Limits deadline and answered 503, and a slot an Admission hands out late is
// released, not leaked.
func TestProxy_StuckExtensionIsBoundedByItsDeadline(t *testing.T) {
	lim := core.Limits{ExtensionDeadline: 50 * time.Millisecond, AdmissionDeadline: 50 * time.Millisecond}
	tests := []struct {
		seam string
		set  func(*core.Options, stuck)
	}{
		{"auth", func(o *core.Options, s stuck) { o.Auth = s }},
		{"admission", func(o *core.Options, s stuck) { o.Admission = s }},
		{"resolver", func(o *core.Options, s stuck) { o.Providers = s }},
	}
	for _, tt := range tests {
		t.Run(tt.seam, func(t *testing.T) {
			s := stuck{unblock: make(chan struct{}), released: &atomic.Int32{}}
			up := newFakeUpstream(t, upstreamOptions{})
			logs := &syncBuf{}
			opts := core.Options{Limits: lim, Logger: slog.New(slog.NewTextHandler(logs, nil))}
			tt.set(&opts, s)
			base := newProxyServer(t, opts)

			start := time.Now()
			resp := post(t, base, "/anthropic/v1/messages", []byte(`{}`), via(up))
			body := errorBody(t, resp)
			if resp.StatusCode != http.StatusServiceUnavailable || body["type"] != "error" {
				t.Fatalf("status=%d body=%v, want an Anthropic-shaped 503", resp.StatusCode, body)
			}
			if took := time.Since(start); took > 2*time.Second {
				t.Fatalf("answered after %v: the deadline did not bound the seam", took)
			}
			if g := logs.String(); !strings.Contains(g, "extension timed out") || !strings.Contains(g, "seam="+tt.seam) {
				t.Fatalf("log = %q, want a metadata-only timeout line for %s", g, tt.seam)
			}
			if len(up.Requests()) != 0 {
				t.Fatal("upstream contacted after a timed-out extension")
			}
			close(s.unblock)
			if tt.seam == "admission" {
				eventually(t, "late admission slot released", func() bool { return s.released.Load() == 1 })
			}
		})
	}
}
