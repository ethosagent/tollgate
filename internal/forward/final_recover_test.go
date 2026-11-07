package forward

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testkit/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const oaRecovery = `data: {"id":"chatcmpl-two","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-two","choices":[{"index":0,"delta":{"content":" Again"},"finish_reason":null}]}

data: {"id":"chatcmpl-two","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

const anRecovery = `event: message_start
data: {"type":"message_start","message":{"id":"msg_two","usage":{"input_tokens":5,"output_tokens":1}}}

event: ping
data: {"type":"ping"}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Again"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

// from returns the part of the stream b that starts at the frame containing marker.
func from(b []byte, marker string) []byte {
	i := bytes.Index(b, []byte(marker))
	return b[bytes.LastIndex(b[:i], []byte("\n\n"))+2:]
}

func normaliseTerm(t *testing.T) normalise.Terminal {
	t.Helper()
	term, err := normalise.TerminalFor(model.WireOpenAIChat)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

// recoverN recovers the first n times, then releases, and records each summary.
func recoverN(n int, seen *[]model.StreamSummary) func(model.StreamSummary) model.Decision {
	return func(s model.StreamSummary) model.Decision {
		*seen = append(*seen, s)
		if len(*seen) <= n {
			return model.Decision{Action: model.DecisionRecover, Body: []byte(`{"stream":true,"recover":true}`)}
		}
		return model.Decision{}
	}
}

func TestFinal_RecoverSplicesOneResponseOpenAI(t *testing.T) {
	first := fixtureBytes(t, "openai/chat-stream-text.sse")
	var seen []model.StreamSummary
	fz := finalizeFor(t, model.WireOpenAIChat, recoverN(1, &seen))
	up := &upScript{bodies: []*http.Response{sseResp(first, 40), sseResp([]byte(oaRecovery), 33)}}
	w := &recWriter{}
	res, err := forwardFinal(t, up, w, fz, nil)
	if err != nil || res.Finish != model.FinishOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	prefix := first[:terminalStart(first, fz.Term)]
	want := append(append([]byte(nil), prefix...), from([]byte(oaRecovery), `"content":" Again"`)...)
	if !bytes.Equal(w.buf.Bytes(), want) {
		t.Fatalf("client bytes\n got %q\nwant %q", w.buf.Bytes(), want)
	}
	if n := strings.Count(w.buf.String(), "[DONE]"); n != 1 {
		t.Fatalf("%d terminal events, want exactly one", n)
	}
	if len(seen) != 2 || seen[1].FinishReason != "stop" || seen[1].Events == 0 {
		t.Fatalf("summaries = %+v", seen)
	}
	assertSecondCall(t, up)
}

// assertSecondCall: the recovery went to the same target with the same
// credentials and the finalizer's body.
func assertSecondCall(t *testing.T, up *upScript) {
	t.Helper()
	if len(up.reqs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(up.reqs))
	}
	a, b := up.reqs[0], up.reqs[1]
	if b.URL.String() != a.URL.String() || b.Method != a.Method {
		t.Fatalf("recovery went to %s %s, want %s %s", b.Method, b.URL, a.Method, a.URL)
	}
	for _, h := range []string{"Authorization", "Content-Type"} {
		if b.Header.Get(h) == "" || b.Header.Get(h) != a.Header.Get(h) {
			t.Fatalf("header %s = %q, want %q", h, b.Header.Get(h), a.Header.Get(h))
		}
	}
	if string(up.sent[1]) != `{"stream":true,"recover":true}` || string(up.sent[0]) != `{"stream":true}` {
		t.Fatalf("bodies = %q", up.sent)
	}
}

func TestFinal_RecoverSplicesOneMessageAnthropic(t *testing.T) {
	first := fixtureBytes(t, "anthropic/messages-stream-text.sse")
	var seen []model.StreamSummary
	fz := finalizeFor(t, model.WireAnthropicMessages, recoverN(2, &seen))
	up := &upScript{bodies: []*http.Response{sseResp(first, 41), sseResp([]byte(anRecovery), 25), sseResp([]byte(anRecovery), 1000)}}
	w := &recWriter{}
	if res, err := forwardFinal(t, up, w, fz, nil); err != nil || res.Finish != model.FinishOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	out := w.buf.String()
	for sub, want := range map[string]int{"message_start\n": 1, "event: message_stop": 1, "event: message_delta": 1, "event: ping": 1} {
		if got := strings.Count(out, sub); got != want {
			t.Errorf("%q appears %d times, want %d", sub, got, want)
		}
	}
	// The original block has a start, two deltas and a stop; each recovery block a start, a delta and a stop.
	for idx, want := range map[string]int{`"index":0`: 4, `"index":1`: 3, `"index":2`: 3} {
		if got := strings.Count(out, idx); got != want {
			t.Errorf("%s appears %d times, want %d", idx, got, want)
		}
	}
	if len(seen) != 3 || seen[2].FinishReason != "end_turn" || seen[2].Usage.OutputTokens != 3 {
		t.Fatalf("summaries = %+v", seen)
	}
	if !strings.HasSuffix(out, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n") {
		t.Fatalf("stream does not end with the one message_stop: %q", out)
	}
}

func TestFinal_RecoveryThatCannotRunReleasesTheOriginalTerminal(t *testing.T) {
	first := fixtureBytes(t, "openai/chat-stream-text.sse")
	json500 := &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{}}`))}
	html := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<html>"))}
	noTerminal := sseResp([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"), 10)
	for name, second := range map[string]*http.Response{"upstream 500": json500, "not an event stream": html, "no terminal event": noTerminal, "transport error": nil} {
		t.Run(name, func(t *testing.T) {
			bodies := []*http.Response{sseResp(first, 64)}
			if second != nil {
				bodies = append(bodies, second)
			}
			var seen []model.StreamSummary
			w := &recWriter{}
			res, err := forwardFinal(t, &upScript{bodies: bodies}, w, finalizeFor(t, model.WireOpenAIChat, recoverN(1, &seen)), nil)
			if err != nil || res.Finish != model.FinishOK {
				t.Fatalf("res=%+v err=%v", res, err)
			}
			if got := w.buf.String(); !strings.HasSuffix(got, string(first[terminalStart(first, normaliseTerm(t)):])) {
				t.Fatalf("the original terminal was not released:\n%q", got)
			}
			if n := strings.Count(w.buf.String(), "[DONE]"); n != 1 {
				t.Fatalf("%d terminal events, want one", n)
			}
		})
	}
}

func TestFinal_RecoveryBreakingMidStreamEndsWithAnErrorEvent(t *testing.T) {
	first := fixtureBytes(t, "openai/chat-stream-text.sse")
	broken := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(&chunkReader{chunks: []string{oaRecovery[:200]}, err: errors.New("reset")})}
	var seen []model.StreamSummary
	w := &recWriter{}
	res, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(first, 64), broken}}, w, finalizeFor(t, model.WireOpenAIChat, recoverN(1, &seen)), nil)
	if err != nil || res.Finish != model.FinishUpstreamError || res.Cut {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(w.buf.String(), `"error"`) {
		t.Fatalf("no error event after the broken recovery: %q", w.buf.String())
	}
}
