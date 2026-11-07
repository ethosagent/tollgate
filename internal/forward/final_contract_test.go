package forward

import (
	"bytes"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// framesOf is what the tee's Scanner makes of b: the frames a consumer of
// a plain (unheld) stream of these bytes receives.
func framesOf(b []byte) []string {
	var out []string
	var sc Scanner
	emit := func(f []byte) { out = append(out, string(f)) }
	sc.Scan(b, emit)
	sc.Finish(emit)
	return out
}

// usageOf is the usage a Summarizer reads from one upstream response.
func usageOf(t *testing.T, f model.WireFormat, b []byte) model.Usage {
	t.Helper()
	w, err := normalise.ForWire(f)
	if err != nil {
		t.Fatal(err)
	}
	z := normalise.NewSummarizer(w)
	for _, fr := range framesOf(b) {
		z.Frame(normalise.SplitFrame([]byte(fr)))
	}
	return z.Summary().Usage
}

// TestFinal_ConsumerSeesExactlyWhatTheClientGot is the FrameQueue contract
// on the barrier path: a copy of every frame the client was sent, after it
// was sent. A terminal a Finalizer discarded (Replace, Recover) never reaches
// the consumer; a replacement's frames do.
func TestFinal_ConsumerSeesExactlyWhatTheClientGot(t *testing.T) {
	repl := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n")
	cases := []struct {
		name, first, recovery string
		wire                  model.WireFormat
		decide                func(model.StreamSummary) model.Decision
	}{
		{"release openai", "openai/chat-stream-text.sse", "", model.WireOpenAIChat, release},
		{"replace openai", "openai/chat-stream-text.sse", "", model.WireOpenAIChat, func(model.StreamSummary) model.Decision {
			return model.Decision{Action: model.DecisionReplace, Response: &model.Response{Body: repl}}
		}},
		{"recover openai", "openai/chat-stream-text.sse", oaRecovery, model.WireOpenAIChat, nil},
		{"recover anthropic", "anthropic/messages-stream-text.sse", anRecovery, model.WireAnthropicMessages, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := fixtureBytes(t, tc.first)
			bodies := []*http.Response{sseResp(first, 23)}
			decide := tc.decide
			if tc.recovery != "" {
				var seen []model.StreamSummary
				decide = recoverN(1, &seen)
				bodies = append(bodies, sseResp([]byte(tc.recovery), 17))
			}
			q := NewFrameQueue(1024)
			w := &recWriter{}
			if _, err := forwardFinal(t, &upScript{bodies: bodies}, w, finalizeFor(t, tc.wire, decide), q); err != nil {
				t.Fatal(err)
			}
			if got, want := frames(q), framesOf(w.buf.Bytes()); !slices.Equal(got, want) {
				t.Fatalf("consumer frames\n got %q\nwant %q (the client's)", got, want)
			}
			if q.Dropped() != 0 {
				t.Fatalf("dropped %d frames", q.Dropped())
			}
		})
	}
}

// TestFinal_BareCRStreamIsHeldLikeAnyOther: the barrier frames with the
// tee's Scanner, so a stream with CR-only line endings is decided on, sent
// byte for byte, and its consumer sees what a plain tee would deliver.
func TestFinal_BareCRStreamIsHeldLikeAnyOther(t *testing.T) {
	raw := bytes.ReplaceAll(fixtureBytes(t, "openai/chat-stream-text.sse"), []byte("\n"), []byte("\r"))
	calls := 0
	fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { calls++; return model.Decision{} })
	q := NewFrameQueue(1024)
	w := &recWriter{}
	if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 11)}}, w, fz, q); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Equal(w.buf.Bytes(), raw) {
		t.Fatalf("Decide calls = %d, bytes equal = %v; want 1, true", calls, bytes.Equal(w.buf.Bytes(), raw))
	}
	if got, want := frames(q), framesOf(raw); !slices.Equal(got, want) || len(want) == 0 {
		t.Fatalf("consumer frames\n got %q\nwant %q", got, want)
	}
}

// TestFinal_OversizedFramesAreCountedAccurately: a frame over MaxFrameBytes
// is passed to the client and counted as one dropped frame, and the barrier
// still holds the terminal. A terminal region over MaxFrameBytes cannot be
// held in bounded memory: it is released undecided (Result.Final.Unheld)
// and every frame still reaches the consumer.
func TestFinal_OversizedFramesAreCountedAccurately(t *testing.T) {
	fin := "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	big := "data: " + strings.Repeat("a", MaxFrameBytes+10) + "\n\n"
	fat := "data: {\"pad\":\"" + strings.Repeat("p", 64<<10) + "\"}\n\n" // 64 KiB: under the frame cap
	cases := []struct {
		name       string
		raw        string
		decided    bool
		dropped    int64
		consumer   int // frames the consumer must see
		wantUnheld bool
	}{
		{"oversized frame before the terminal", "data: {\"x\":1}\n\n" + big + fin + "data: [DONE]\n\n", true, 1, 3, false},
		{"terminal region over the cap", fin + strings.Repeat(fat, 20) + "data: [DONE]\n\n", false, 0, 22, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decided := false
			fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { decided = true; return model.Decision{} })
			q := NewFrameQueue(1 << 16)
			w := &recWriter{}
			res, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp([]byte(tc.raw), 16<<10)}}, w, fz, q)
			if err != nil {
				t.Fatal(err)
			}
			if w.buf.String() != tc.raw {
				t.Fatalf("client bytes differ (%d vs %d)", w.buf.Len(), len(tc.raw))
			}
			got := frames(q)
			if decided != tc.decided || q.Dropped() != tc.dropped || len(got) != tc.consumer {
				t.Fatalf("decided=%v dropped=%d consumer frames=%d; want %v %d %d", decided, q.Dropped(), len(got), tc.decided, tc.dropped, tc.consumer)
			}
			if res.Final == nil || res.Final.Unheld != tc.wantUnheld {
				t.Fatalf("Result.Final = %+v, want Unheld %v", res.Final, tc.wantUnheld)
			}
		})
	}
}

// TestFinal_UsageCountsEveryUpstreamResponse: the Metric's usage for a
// finalized stream is the barrier's, summed over the original response and
// each recovery, including a terminal the client never received.
func TestFinal_UsageCountsEveryUpstreamResponse(t *testing.T) {
	first := fixtureBytes(t, "anthropic/messages-stream-text.sse")
	var seen []model.StreamSummary
	fz := finalizeFor(t, model.WireAnthropicMessages, recoverN(1, &seen))
	up := &upScript{bodies: []*http.Response{sseResp(first, 40), sseResp([]byte(anRecovery), 40)}}
	res, err := forwardFinal(t, up, &recWriter{}, fz, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := usageOf(t, model.WireAnthropicMessages, first), usageOf(t, model.WireAnthropicMessages, []byte(anRecovery))
	want := model.Usage{InputTokens: a.InputTokens + b.InputTokens, OutputTokens: a.OutputTokens + b.OutputTokens,
		CachedInputTokens: a.CachedInputTokens + b.CachedInputTokens, ReasoningTokens: a.ReasoningTokens + b.ReasoningTokens}
	if res.Final == nil || res.Final.Usage != want || want.OutputTokens == 0 {
		t.Fatalf("Result.Final = %+v, want usage %+v (both upstream responses)", res.Final, want)
	}
}

// FuzzFinalRelease: whatever the bytes and however they are split, a
// released stream reaches the client unchanged and its consumer gets the
// frames a plain tee would deliver: one framer, two paths.
func FuzzFinalRelease(f *testing.F) {
	for _, name := range []string{"openai/chat-stream-text.sse", "anthropic/messages-stream-text.sse"} {
		raw, err := os.ReadFile("../fixtures/testdata/" + name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw, uint8(7))
		f.Add(bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n")), uint8(1))
		f.Add(bytes.ReplaceAll(raw, []byte("\n"), []byte("\r")), uint8(3))
	}
	f.Add([]byte(": keep-alive\n\ndata: [DONE]\r\n\r"), uint8(2))
	f.Fuzz(func(t *testing.T, raw []byte, n uint8) {
		size := int(n%64) + 1
		fz := finalizeFor(t, model.WireOpenAIChat, release)
		q := NewFrameQueue(len(raw) + 1)
		w := &recWriter{}
		if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, size)}}, w, fz, q); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w.buf.Bytes(), raw) {
			t.Fatalf("client bytes\n got %q\nwant %q", w.buf.Bytes(), raw)
		}
		if got, want := frames(q), framesOf(raw); !slices.Equal(got, want) {
			t.Fatalf("consumer frames\n got %q\nwant %q", got, want)
		}
	})
}
