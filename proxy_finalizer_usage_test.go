package tollgate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
)

// usageProxy is a started proxy with a sink and fs, closed when t ends.
func usageProxy(t *testing.T, sink *collectSink, fs ...tollgate.Finalizer) string {
	t.Helper()
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("acme"), Metrics: sink, Finalizers: fs,
		Limits: tollgate.Limits{MetricFlushInterval: 5 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	closeProxy(t, p)
	return serve(t, p)
}

// lastUsage makes one call against bodies and returns its Metric's usage.
func lastUsage(t *testing.T, wc wireCase, fs []tollgate.Finalizer, bodies ...[]byte) tollgate.Usage {
	t.Helper()
	sink := &collectSink{}
	_ = callWith(t, usageProxy(t, sink, fs...), wc, newSeqUpstream(t, bodies...))
	eventually(t, "the call's metric", func() bool { return len(sink.metrics()) == 1 })
	return sink.metrics()[0].Usage
}

// TestFinalizer_MetricUsageCountsEveryUpstreamCall: a recovered stream cost
// two upstream calls, so its Metric carries the usage of both, including the
// original terminal the client never received.
func TestFinalizer_MetricUsageCountsEveryUpstreamCall(t *testing.T) {
	for _, wc := range wireCases {
		t.Run(wc.name, func(t *testing.T) {
			first := fixtureOrFail(t, wc.first)
			a := lastUsage(t, wc, nil, first)               // the original alone
			b := lastUsage(t, wc, nil, []byte(wc.recovery)) // the recovery alone
			once := &finalizer{fn: func(n int, _ tollgate.StreamSummary) tollgate.Decision {
				if n == 1 {
					return recoverDecision
				}
				return tollgate.Decision{}
			}}
			got := lastUsage(t, wc, []tollgate.Finalizer{once}, first, []byte(wc.recovery))
			want := tollgate.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
				CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens, ReasoningTokens: a.ReasoningTokens + b.ReasoningTokens}
			if got != want || want == (tollgate.Usage{}) {
				t.Fatalf("usage = %+v, want %+v (original %+v + recovery %+v)", got, want, a, b)
			}
		})
	}
}

// TestFinalizer_UnheldTerminalRegionIsCounted: a terminal region too large
// to hold goes to the client undecided, and /statusz counts it.
func TestFinalizer_UnheldTerminalRegionIsCounted(t *testing.T) {
	pad := "data: {\"pad\":\"" + strings.Repeat("p", 64<<10) + "\"}\n\n"
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" + strings.Repeat(pad, 20) + "data: [DONE]\n\n"
	f := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { return tollgate.Decision{} }}
	p, err := tollgate.New(tollgate.Options{Auth: allowLocal("acme"), Finalizers: []tollgate.Finalizer{f}})
	if err != nil {
		t.Fatal(err)
	}
	closeProxy(t, p)
	got := callWith(t, serve(t, p), wireCases[0], newSeqUpstream(t, []byte(body)))
	if string(got) != body || f.calls.Load() != 0 {
		t.Fatalf("client got %d bytes (want %d), Decide calls %d (want 0)", len(got), len(body), f.calls.Load())
	}
	if n := getStatusz(t, adminServer(t, p)).Hooks.UnheldTerminals; n != 1 {
		t.Fatalf("statusz hooks.unheld_terminals = %d, want 1", n)
	}
}

// TestFinalizer_SeveralChoicesAreNotHeld: the barrier's terminal region and
// recovery splice assume one choice, so a request for several (OpenAI n > 1)
// is relayed without the barrier: unchanged and undecided.
func TestFinalizer_SeveralChoicesAreNotHeld(t *testing.T) {
	first := fixtureOrFail(t, wireCases[0].first)
	f := &finalizer{fn: func(int, tollgate.StreamSummary) tollgate.Decision { return recoverDecision }}
	base := finalizerProxy(t, tollgate.Options{}, f)
	up := newSeqUpstream(t, first)
	resp := post(t, base, wireCases[0].path, []byte(`{"model":"m","stream":true,"n":2,"messages":[{"role":"user","content":"hi"}]}`),
		map[string]string{"x-upstream-url": up.srv.URL})
	if got := readAll(t, resp); string(got) != string(first) || f.calls.Load() != 0 {
		t.Fatalf("client bytes equal = %v, Decide calls = %d; want the stream unchanged and no Decide", string(got) == string(first), f.calls.Load())
	}
}
