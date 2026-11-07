package testkit_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// probeHook is a Hook that does nothing but can mutate the call or panic, to
// show what the harness records around it.
type probeHook struct {
	tollgate.NoopHook
	mutate bool
	panics bool
}

func (probeHook) Name() string { return "probe" }

func (h probeHook) Before(_ context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	if h.panics {
		panic("probe: boom")
	}
	if h.mutate {
		c.Body = []byte(`{"mutated":true}`)
		c.Meta = map[string]any{"seen": true}
	}
	return nil, nil
}

type rejectAuth struct{}

func (rejectAuth) Authenticate(_ context.Context, r *http.Request) (tollgate.Tenant, error) {
	if r.Header.Get("X-Team-Key") != "good" {
		return tollgate.Tenant{}, statusErr{http.StatusForbidden}
	}
	return tollgate.Tenant{ID: "team-a"}, nil
}

type statusErr struct{ code int }

func (e statusErr) Error() string   { return "rejected" }
func (e statusErr) HTTPStatus() int { return e.code }

type releaseFinalizer struct{}

func (releaseFinalizer) Name() string { return "release" }
func (releaseFinalizer) Decide(context.Context, *tollgate.Call, tollgate.StreamSummary) tollgate.Decision {
	return tollgate.Decision{Action: tollgate.DecisionRelease}
}

type captureSink struct {
	mu      sync.Mutex
	batches [][]tollgate.Metric
}

func (s *captureSink) Write(_ context.Context, b []tollgate.Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, slices.Clone(b))
	return nil
}

func TestRun_RecordsHookLifecycle(t *testing.T) {
	rec := testkit.Run(t, probeHook{}, "openai/chat-stream-text")

	stages := rec.Stages()
	if stages[0] != testkit.StageBefore || stages[len(stages)-1] != testkit.StageSessionClosed {
		t.Fatalf("stages = %v, want Before first and OnSessionClosed last", stages)
	}
	if got := rec.Of(testkit.StageAfter); len(got) != 1 {
		t.Fatalf("After calls = %d, want 1", len(got))
	}
	if n := len(rec.Of(testkit.StageOnEvent)); n < 2 {
		t.Fatalf("OnEvent calls = %d, want the stream's events", n)
	}
	before := rec.Of(testkit.StageBefore)[0]
	if before.Plugin != "probe" || before.Call.Model != "gpt-4.1-mini" || !before.Call.Stream || len(before.Call.Messages) == 0 {
		t.Fatalf("Before saw %+v", before.Call)
	}
	if m := rec.Of(testkit.StageAfter)[0].Metric; m.Finish != tollgate.FinishOK || m.Status != 200 {
		t.Fatalf("After metric = %+v", m)
	}
	want, _ := testkit.Fixture("openai/chat-stream-text")
	if len(rec.Replies) != 1 || rec.Replies[0].Status != 200 || !bytes.Equal(rec.Replies[0].Body, want) {
		t.Fatalf("reply = %+v, want the fixture byte for byte", rec.Replies)
	}
	if s := rec.Of(testkit.StageSessionClosed)[0].Session; s == nil || s.Calls != 1 {
		t.Fatalf("closed session = %+v", s)
	}
}

func TestRun_OrderIsDeterministic(t *testing.T) {
	first := testkit.Run(t, probeHook{}, "openai/chat-stream-text", "openai/chat").Stages()
	for i := range 20 {
		if got := testkit.Run(t, probeHook{}, "openai/chat-stream-text", "openai/chat").Stages(); !slices.Equal(got, first) {
			t.Fatalf("run %d: stages = %v, want %v", i, got, first)
		}
	}
}

func TestRun_BufferedResponseReachesOnResponse(t *testing.T) {
	rec := testkit.Run(t, probeHook{}, "openai/chat")
	got := rec.Of(testkit.StageOnResponse)
	want, _ := testkit.Fixture("openai/chat")
	if len(got) != 1 || !bytes.Equal(got[0].Response.Body, want) || got[0].Response.Status != 200 {
		t.Fatalf("OnResponse = %+v", got)
	}
}

func TestRun_UpstreamErrorFixture(t *testing.T) {
	rec := testkit.Run(t, probeHook{}, "openai/error-429")
	if rec.Replies[0].Status != 429 {
		t.Fatalf("status = %d, want 429", rec.Replies[0].Status)
	}
	if m := rec.Of(testkit.StageAfter)[0].Metric; m.Finish != tollgate.FinishUpstreamError {
		t.Fatalf("finish = %q", m.Finish)
	}
}

func TestRun_RecordsInputsNotLaterMutations(t *testing.T) {
	rec := testkit.Run(t, probeHook{mutate: true}, "openai/chat-stream-text")
	before := rec.Of(testkit.StageBefore)[0].Call
	if bytes.Contains(before.Body, []byte("mutated")) || before.Meta["seen"] != nil {
		t.Fatalf("Before's recorded call changed after the hook ran: %s %v", before.Body, before.Meta)
	}
}

func TestRun_PanickingHookIsRecordedAndCallStillServed(t *testing.T) {
	rec := testkit.Run(t, probeHook{panics: true}, "openai/chat-stream-text")
	if len(rec.Of(testkit.StageBefore)) != 1 {
		t.Fatal("the panicking Before call was not recorded")
	}
	if rec.Replies[0].Status != 200 {
		t.Fatalf("status = %d, want the call served despite the panic", rec.Replies[0].Status)
	}
}

func TestRun_Authenticator(t *testing.T) {
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer upstream-secret")
	hdr.Set("X-Api-Key", "also-secret")
	hdr.Set("X-Team-Key", "bad")
	rec := testkit.RunWith(t, testkit.Config{Plugins: []any{rejectAuth{}}, Fixtures: []string{"openai/chat"}, Headers: hdr})

	got := rec.Of(testkit.StageAuthenticate)
	if len(got) != 1 || got[0].Err == "" {
		t.Fatalf("Authenticate = %+v, want one rejected call", got)
	}
	for _, name := range []string{"Authorization", "X-Api-Key"} {
		if v := got[0].Request.Header.Get(name); v != "" {
			t.Fatalf("recorded header %s = %q, credentials must be left out", name, v)
		}
	}
	if got[0].Request.Header.Get(testkit.FixtureHeader) != "openai/chat" || got[0].Request.Path != "/openai/v1/chat/completions" {
		t.Fatalf("request = %+v", got[0].Request)
	}
	if rec.Replies[0].Status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Replies[0].Status)
	}
}

func TestRun_AuthenticatorAccepts(t *testing.T) {
	hdr := http.Header{"X-Team-Key": {"good"}}
	rec := testkit.RunWith(t, testkit.Config{Plugins: []any{rejectAuth{}}, Fixtures: []string{"openai/chat"}, Headers: hdr})
	got := rec.Of(testkit.StageAuthenticate)[0]
	if got.Tenant == nil || got.Tenant.ID != "team-a" || got.Err != "" || rec.Replies[0].Status != 200 {
		t.Fatalf("auth = %+v reply = %+v", got, rec.Replies[0])
	}
}

func TestRun_Finalizer(t *testing.T) {
	rec := testkit.Run(t, releaseFinalizer{}, "openai/chat-stream-text")
	got := rec.Of(testkit.StageDecide)
	if len(got) != 1 {
		t.Fatalf("Decide calls = %d, want 1", len(got))
	}
	d := got[0]
	if d.Plugin != "release" || d.Summary.FinishReason != "stop" || d.Summary.Events == 0 || d.Decision.Action != tollgate.DecisionRelease {
		t.Fatalf("Decide = %+v summary %+v", d, d.Summary)
	}
}

func TestRun_MetricSink(t *testing.T) {
	sink := &captureSink{}
	rec := testkit.Run(t, sink, "openai/chat-stream-text", "anthropic/messages")
	got := rec.Of(testkit.StageWrite)
	if len(got) != 1 || len(got[0].Batch) != 2 {
		t.Fatalf("Write = %+v, want one batch of both calls", got)
	}
	if got[0].Batch[0].Provider != "openai" || got[0].Batch[1].Provider != "anthropic" {
		t.Fatalf("batch order = %s, %s", got[0].Batch[0].Provider, got[0].Batch[1].Provider)
	}
	if len(sink.batches) != 1 {
		t.Fatalf("the wrapped sink saw %d batches, want the harness to pass writes through", len(sink.batches))
	}
}

func TestRun_AllPluginKindsInOneRun(t *testing.T) {
	sink := &captureSink{}
	rec := testkit.RunWith(t, testkit.Config{
		Plugins:  []any{rejectAuth{}, probeHook{}, releaseFinalizer{}, sink},
		Fixtures: []string{"openai/chat-stream-text"},
		Headers:  http.Header{"X-Team-Key": {"good"}},
	})
	want := []testkit.Stage{testkit.StageAuthenticate, testkit.StageBefore}
	if got := rec.Stages()[:2]; !slices.Equal(got, want) {
		t.Fatalf("first stages = %v, want %v", got, want)
	}
	for _, s := range []testkit.Stage{testkit.StageDecide, testkit.StageAfter, testkit.StageWrite, testkit.StageSessionClosed} {
		if len(rec.Of(s)) == 0 {
			t.Errorf("stage %s not recorded", s)
		}
	}
}

// failTB turns Fatalf into a recorded message plus Goexit, so a test can see
// the harness reject bad input.
type failTB struct {
	testing.TB
	msg string
}

func (f *failTB) Helper() {}
func (f *failTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func TestRun_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name     string
		plugin   any
		fixture  string
		wantText string
	}{
		{"a value that is no plugin", 42, "openai/chat", "not an Authenticator"},
		{"an unknown fixture", probeHook{}, "openai/nope", "unknown fixture"},
		{"a request input used as a response", probeHook{}, "openai/request-chat-stream", "unknown fixture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ftb := &failTB{TB: t}
			done := make(chan struct{})
			go func() {
				defer close(done)
				testkit.Run(ftb, tt.plugin, tt.fixture)
			}()
			<-done
			if !strings.Contains(ftb.msg, tt.wantText) {
				t.Fatalf("Fatalf message = %q, want it to contain %q", ftb.msg, tt.wantText)
			}
		})
	}
}
