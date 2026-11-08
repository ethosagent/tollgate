package core_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

// sessHook records the Session each call is given in Before and every
// session OnSessionClosed receives.
type sessHook struct {
	model.NoopHook
	mu     sync.Mutex
	seen   []model.Session
	closed []model.Session
}

func (h *sessHook) Name() string { return "sess" }

func (h *sessHook) Before(_ context.Context, c *model.Call) (*model.Response, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, c.Session)
	return nil, nil
}

func (h *sessHook) OnSessionClosed(_ context.Context, s model.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = append(h.closed, s)
}

func (h *sessHook) sessions() []model.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.Session(nil), h.seen...)
}

func (h *sessHook) closedSessions() []model.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.Session(nil), h.closed...)
}

// closeProxy closes p when the test ends, as a host does on shutdown.
func closeProxy(t *testing.T, p *core.Proxy) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func fixtureBody(t testing.TB, name string) []byte {
	t.Helper()
	b, err := fixtures.Bytes(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func agentStep(t testing.TB, n int) []byte {
	return fixtureBody(t, fmt.Sprintf("anthropic/request-agent-step-%d", n))
}

// tenantAuth names the tenant by the x-tenant header and lets it target the fake upstream.
type tenantAuth struct{}

func (tenantAuth) Authenticate(_ context.Context, r *http.Request) (model.Tenant, error) {
	return model.Tenant{ID: r.Header.Get("x-tenant"), Policy: &model.Policy{AllowedUpstreams: []string{"127.0.0.0/8"}}}, nil
}

func newSessionProxy(t *testing.T, opts core.Options) (base string, p *core.Proxy, h *sessHook) {
	t.Helper()
	h = &sessHook{}
	base, p = newHookedProxy(t, opts, h)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	closeProxy(t, p)
	return base, p, h
}

func TestSession_AgentRunPredictedAsOneSession(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	sink := &collectSink{}
	base, p, h := newSessionProxy(t, core.Options{Metrics: sink, Limits: core.Limits{MetricFlushInterval: time.Millisecond}})
	for step := 1; step <= 6; step++ {
		if resp := post(t, base, msgsPath, agentStep(t, step), via(up)); resp.StatusCode != 200 {
			t.Fatalf("step %d: status %d", step, resp.StatusCode)
		} else {
			_ = readAll(t, resp)
		}
	}
	other := post(t, base, msgsPath, fixtureBody(t, "anthropic/request-agent-other"), via(up))
	_ = readAll(t, other)

	seen := h.sessions()
	if len(seen) != 7 {
		t.Fatalf("hook saw %d calls, want 7", len(seen))
	}
	run := seen[0].ID
	for i, s := range seen[:6] {
		if s.ID != run || s.Calls != i+1 || s.Source != model.SessionPredicted || s.TenantID != "acme" {
			t.Fatalf("step %d session = %+v, want one predicted session %q with %d calls", i+1, s, run, i+1)
		}
	}
	if run == "" || seen[6].ID == run {
		t.Fatalf("new conversation with the same system prompt got session %q, want a new one (run was %q)", seen[6].ID, run)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for _, m := range sink.metrics() {
		byID[m.SessionID]++
	}
	if byID[run] != 6 || byID[seen[6].ID] != 1 {
		t.Fatalf("Metric.SessionID counts = %v, want 6 for the run and 1 for the new conversation", byID)
	}
}

func TestSession_HeaderIsPartitionedByTenantAndStripped(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base, _, h := newSessionProxy(t, core.Options{Auth: tenantAuth{}})
	for _, tenant := range []string{"acme", "acme", "globex"} {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up, "x-tenant", tenant, "x-session-id", "run-7")))
	}
	got := h.sessions()
	if len(got) != 3 {
		t.Fatalf("saw %d calls, want 3", len(got))
	}
	if got[0].ID != "run-7" || got[0].Source != model.SessionFromHeader || got[1].Calls != 2 {
		t.Fatalf("acme sessions = %+v, %+v", got[0], got[1])
	}
	if got[2].TenantID != "globex" || got[2].Calls != 1 {
		t.Fatalf("same header id in another tenant joined the first session: %+v", got[2])
	}
	for _, r := range up.Requests() {
		if r.Header.Get("x-session-id") != "" {
			t.Fatal("the session header reached the upstream")
		}
	}
}

func TestSession_QuietSessionClosesOnceAndRunsOnSessionClosed(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base, _, h := newSessionProxy(t, core.Options{Limits: core.Limits{SessionTTL: 60 * time.Millisecond}})
	for range 2 {
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up, "x-session-id", "quiet")))
	}
	eventually(t, "session closed", func() bool { return len(h.closedSessions()) == 1 })
	got := h.closedSessions()
	if len(got) != 1 || got[0].ID != "quiet" || got[0].Calls != 2 {
		t.Fatalf("OnSessionClosed got %+v, want the quiet session once with 2 calls", got)
	}
}

func TestSession_OnlyAnOnSessionClosedHookStillTracksSessions(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	h := &closedOnlyHook{}
	base, p := newHookedProxy(t, core.Options{Limits: core.Limits{SessionTTL: 40 * time.Millisecond}}, h)
	closeProxy(t, p)
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up, "x-session-id", "solo")))
	eventually(t, "closed", func() bool { return h.count() == 1 })
}

type closedOnlyHook struct {
	model.NoopHook
	mu sync.Mutex
	n  int
}

func (*closedOnlyHook) Name() string { return "closed-only" }
func (h *closedOnlyHook) OnSessionClosed(context.Context, model.Session) {
	h.mu.Lock()
	h.n++
	h.mu.Unlock()
}
func (h *closedOnlyHook) count() int { h.mu.Lock(); defer h.mu.Unlock(); return h.n }

// fakeStrategy is a host-supplied SessionStrategy.
type fakeStrategy struct {
	closed  chan model.Session
	panics  bool
	headers chan string // Call.SessionHeader of each Resolve, when not nil
}

func (f *fakeStrategy) Resolve(_ context.Context, _ model.Tenant, c *model.Call) model.Session {
	if f.panics {
		panic("strategy bug")
	}
	if f.headers != nil {
		f.headers <- c.SessionHeader
	}
	return model.Session{ID: "host-session", Source: model.SessionFromHeader}
}
func (f *fakeStrategy) Closed() <-chan model.Session { return f.closed }

func TestSession_HostStrategyReplacesTheDefault(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	fs := &fakeStrategy{closed: make(chan model.Session, 1)}
	base, _, h := newSessionProxy(t, core.Options{Sessions: fs})
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	fs.closed <- model.Session{ID: "host-session"}
	eventually(t, "host session closed", func() bool { return len(h.closedSessions()) == 1 })
	if got := h.sessions(); len(got) != 1 || got[0].ID != "host-session" {
		t.Fatalf("hook saw %+v, want the host strategy's session", got)
	}
}

// TestSession_HostStrategyReadsTheSessionHeader: the core strips the session
// header before forwarding, so a strategy outside the core (which may not
// import internal/) reads it from the documented Call.SessionHeader, under
// whatever name HeaderNames.Session gives it.
func TestSession_HostStrategyReadsTheSessionHeader(t *testing.T) {
	tests := []struct {
		name, header, value, want string
	}{
		{name: "default header name", header: "x-session-id", value: " run-9 ", want: "run-9"},
		{name: "host-chosen header name", header: "x-run", value: "r-1", want: "r-1"},
		{name: "no header", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			fs := &fakeStrategy{closed: make(chan model.Session), headers: make(chan string, 1)}
			opts := core.Options{Sessions: fs}
			if tt.header != "x-session-id" {
				opts.Headers.Session = "x-run"
			}
			base, _, _ := newSessionProxy(t, opts)
			var kv []string
			if tt.header != "" {
				kv = []string{tt.header, tt.value}
			}
			_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up, kv...)))
			if got := <-fs.headers; got != tt.want {
				t.Fatalf("Call.SessionHeader = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSession_PanickingStrategyDegradesToNoSession(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	base, _, h := newSessionProxy(t, core.Options{Sessions: &fakeStrategy{panics: true}})
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	_ = readAll(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want the call served despite the strategy panic", resp.StatusCode)
	}
	if got := h.sessions(); len(got) != 1 || got[0].ID != "" {
		t.Fatalf("session = %+v, want none", got)
	}
}

// msgsPath is the proxied path of the Anthropic messages API.
const msgsPath = "/anthropic/v1/messages"
