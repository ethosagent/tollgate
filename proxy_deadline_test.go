package tollgate_test

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// stuck blocks until the test ends, ignoring its context: the worst case of a
// sidecar that hangs.
type stuck struct {
	unblock  chan struct{}
	released *atomic.Int32
}

func (s stuck) Authenticate(context.Context, *http.Request) (tollgate.Tenant, error) {
	<-s.unblock
	return tollgate.Tenant{ID: "late"}, nil
}

func (s stuck) Acquire(context.Context, tollgate.Tenant, *tollgate.Call) (func(), error) {
	<-s.unblock
	return func() { s.released.Add(1) }, nil
}

func (s stuck) Resolve(context.Context, tollgate.Tenant, *http.Request) (tollgate.Provider, string, error) {
	<-s.unblock
	return tollgate.Provider{}, "", nil
}

// TestProxy_StuckExtensionIsBoundedByItsDeadline covers non-negotiable 2: an
// Auth, Admission or resolver call that ignores its context is cut at its
// Limits deadline and answered 503, and a slot an Admission hands out late is
// released, not leaked.
func TestProxy_StuckExtensionIsBoundedByItsDeadline(t *testing.T) {
	lim := tollgate.Limits{ExtensionDeadline: 50 * time.Millisecond, AdmissionDeadline: 50 * time.Millisecond}
	tests := []struct {
		seam string
		set  func(*tollgate.Options, stuck)
	}{
		{"auth", func(o *tollgate.Options, s stuck) { o.Auth = s }},
		{"admission", func(o *tollgate.Options, s stuck) { o.Admission = s }},
		{"resolver", func(o *tollgate.Options, s stuck) { o.Providers = s }},
	}
	for _, tt := range tests {
		t.Run(tt.seam, func(t *testing.T) {
			s := stuck{unblock: make(chan struct{}), released: &atomic.Int32{}}
			up := testkit.NewFakeUpstream(t, testkit.Options{})
			logs := &syncBuf{}
			opts := tollgate.Options{Limits: lim, Logger: slog.New(slog.NewTextHandler(logs, nil))}
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
