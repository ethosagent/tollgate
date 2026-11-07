package tollgate_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

func TestFinalizer_ReplaceSendsTheReplacementTerminal(t *testing.T) {
	repl := map[string]string{
		"openai":    "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n",
		"anthropic": "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for _, wc := range wireCases {
		t.Run(wc.name, func(t *testing.T) {
			first := fixtureOrFail(t, wc.first)
			f := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision {
				return tollgate.Decision{Action: tollgate.DecisionReplace, Response: &tollgate.Response{Body: []byte(repl[wc.name])}}
			}}
			got := callWith(t, finalizerProxy(t, tollgate.Options{}, f), wc, newSeqUpstream(t, first))
			cut := bytes.Index(first, []byte(map[string]string{"openai": `"finish_reason":"stop"`, "anthropic": "event: message_delta"}[wc.name]))
			cut = bytes.LastIndex(first[:cut], []byte("\n\n")) + 2
			if want := append(append([]byte(nil), first[:cut]...), repl[wc.name]...); !bytes.Equal(got, want) {
				t.Fatalf("client bytes\n got %q\nwant %q", got, want)
			}
		})
	}
}

// TestFinalizer_RecoveryIsBudgeted: a finalizer that always recovers is cut
// off by FinalizerBudget, and by RecoveryCeiling across finalizers; the stream
// then ends with one terminal event (release).
func TestFinalizer_RecoveryIsBudgeted(t *testing.T) {
	always := func() *finalizer {
		return &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { return recoverDecision }}
	}
	cases := []struct {
		name     string
		limits   tollgate.Limits
		fs       []tollgate.Finalizer
		wantReqs int // 1 original + granted recoveries
	}{
		{"per-finalizer budget", tollgate.Limits{FinalizerBudget: 2, RecoveryCeiling: 10}, []tollgate.Finalizer{always()}, 3},
		{"per-call ceiling", tollgate.Limits{FinalizerBudget: 2, RecoveryCeiling: 3}, []tollgate.Finalizer{always(), always()}, 4},
	}
	for _, tc := range cases {
		for _, wc := range wireCases {
			t.Run(tc.name+"/"+wc.name, func(t *testing.T) {
				up := newSeqUpstream(t, fixtureOrFail(t, wc.first), []byte(wc.recovery))
				got := string(callWith(t, finalizerProxy(t, tollgate.Options{Limits: tc.limits}, tc.fs...), wc, up))
				if hdrs, _ := up.requests(); len(hdrs) != tc.wantReqs {
					t.Fatalf("upstream saw %d requests, want %d", len(hdrs), tc.wantReqs)
				}
				if n := strings.Count(got, wc.terminal); n != 1 {
					t.Fatalf("%d terminal events, want one:\n%s", n, got)
				}
			})
		}
	}
}

// TestFinalizer_PanickingOrSlowDecideReleases: the client gets the upstream's
// bytes unchanged, and a Decide that ignores its context costs at most the
// deadline.
func TestFinalizer_PanickingOrSlowDecideReleases(t *testing.T) {
	limits := tollgate.Limits{FinalizerDeadline: 40 * time.Millisecond}
	boom := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { panic("secret") }}
	// slow ignores its context until the test ends; each abandoned call
	// reports its return, so the leak check never races it (no sleep).
	gate, returned := make(chan struct{}), make(chan struct{}, 16)
	slow := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision {
		defer func() { returned <- struct{}{} }()
		<-gate
		return recoverDecision
	}}
	defer func() {
		close(gate)
		for range slow.calls.Load() {
			<-returned
		}
	}()
	for name, f := range map[string]*finalizer{"panic": boom, "slow": slow} {
		for _, wc := range wireCases {
			t.Run(name+"/"+wc.name, func(t *testing.T) {
				first := fixtureOrFail(t, wc.first)
				up := newSeqUpstream(t, first, []byte(wc.recovery))
				start := time.Now()
				got := callWith(t, finalizerProxy(t, tollgate.Options{Limits: limits}, f), wc, up)
				if !bytes.Equal(got, first) {
					t.Fatalf("client bytes differ from the upstream's")
				}
				if took := time.Since(start); took > 250*time.Millisecond {
					t.Fatalf("a slow Decide delayed the stream by %v", took)
				}
				if hdrs, _ := up.requests(); len(hdrs) != 1 {
					t.Fatalf("upstream saw %d requests, want 1", len(hdrs))
				}
			})
		}
	}
}

// TestFinalizer_OnlyStreamsAreHeld: stream:false is never held, and OnEvent
// hooks see the same events as without a finalizer.
func TestFinalizer_OnlyStreamsAreHeld(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	f := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { return tollgate.Decision{} }}
	base := finalizerProxy(t, tollgate.Options{}, f)
	want, _ := testkit.Fixture("openai/chat")
	req := []byte(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`)
	got := readAll(t, post(t, base, "/openai/v1/chat/completions", req, via(up)))
	if !bytes.Equal(got, want) || f.calls.Load() != 0 {
		t.Fatalf("stream:false: Decide ran %d times, body equal=%v", f.calls.Load(), bytes.Equal(got, want))
	}
}

func TestFinalizer_OnEventHooksStillSeeTheStream(t *testing.T) {
	const name = "openai/chat-stream-tools"
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	var mu sync.Mutex
	var evs []tollgate.Event
	done := make(chan struct{})
	h := &tHook{name: "rec",
		onEvent: func(_ context.Context, _ *tollgate.Call, e tollgate.Event) {
			mu.Lock()
			evs = append(evs, e)
			mu.Unlock()
		},
		after: func(context.Context, *tollgate.Call, tollgate.Metric) { close(done) }}
	f := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { return tollgate.Decision{} }}
	base, p := newHookedProxy(t, tollgate.Options{Finalizers: []tollgate.Finalizer{f}}, h)
	_ = p
	want, _ := testkit.Fixture(name)
	if body := readAll(t, post(t, base, pathFor(name), requestBody(t, name), via(up, testkit.FixtureHeader, name))); !bytes.Equal(body, want) {
		t.Fatal("client bytes changed")
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	if exp := expectedEvents(t, name); !reflect.DeepEqual(evs, exp) {
		t.Fatalf("events\n got %+v\nwant %+v", evs, exp)
	}
}

func TestFinalizer_SummaryReachesDecide(t *testing.T) {
	const name = "anthropic/messages-stream-tool-use"
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	var got tollgate.StreamSummary
	f := &finalizer{fn: func(_ int, s tollgate.StreamSummary) tollgate.Decision { got = s; return tollgate.Decision{} }}
	readAll(t, post(t, finalizerProxy(t, tollgate.Options{}, f), pathFor(name), requestBody(t, name), via(up, testkit.FixtureHeader, name)))
	if got.FinishReason != "tool_use" || len(got.ToolCalls) == 0 || got.ToolCalls[0].Name == "" || got.Usage.OutputTokens == 0 || got.Events == 0 {
		t.Fatalf("summary = %+v", got)
	}
}
