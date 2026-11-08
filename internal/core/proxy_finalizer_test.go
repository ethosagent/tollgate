package core_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

// seqUpstream answers its n-th request with bodies[n] (the last one repeats)
// as an event stream, and records every request it gets.
type seqUpstream struct {
	srv    *httptest.Server
	bodies [][]byte
	mu     sync.Mutex
	hdrs   []http.Header
	sent   [][]byte
}

func newSeqUpstream(t testing.TB, bodies ...[]byte) *seqUpstream {
	t.Helper()
	u := &seqUpstream{bodies: bodies}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		u.mu.Lock()
		n := len(u.hdrs)
		u.hdrs, u.sent = append(u.hdrs, r.Header.Clone()), append(u.sent, b.Bytes())
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		body := u.bodies[min(n, len(u.bodies)-1)]
		for len(body) > 0 { // many small writes, each flushed, like a live stream
			k := min(len(body), 61)
			_, _ = w.Write(body[:k])
			w.(http.Flusher).Flush()
			body = body[k:]
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *seqUpstream) requests() (hdrs []http.Header, sent [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]http.Header(nil), u.hdrs...), append([][]byte(nil), u.sent...)
}

// finalizer is a Finalizer whose answer is a test-supplied function of the
// call number (1-based) and the summary.
type finalizer struct {
	calls atomic.Int32
	fn    func(n int, s model.StreamSummary) model.Decision
}

func (f *finalizer) Name() string { return "test" }
func (f *finalizer) Decide(_ context.Context, _ *model.Call, s model.StreamSummary) model.Decision {
	return f.fn(int(f.calls.Add(1)), s)
}

var recoverDecision = model.Decision{Action: model.DecisionRecover, Body: []byte(`{"stream":true,"model":"m","messages":[{"role":"user","content":"again"}]}`)}

type wireCase struct {
	name     string
	path     string
	first    string // fixture of the original stream
	recovery string // body of the recovery stream
	terminal string // marks the one terminal event
}

var wireCases = []wireCase{
	{"openai", "/openai/v1/chat/completions", "openai/chat-stream-text", "data: {\"id\":\"chatcmpl-two\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-two\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" Again\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl-two\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", "[DONE]"},
	{"responses", "/openai/v1/responses", "openai/responses-stream-text",
		"event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture_two\",\"status\":\"in_progress\"}}\n\n" +
			"event: response.in_progress\ndata: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_fixture_two\"}}\n\n" +
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_fixture_two\",\"type\":\"message\"}}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\" Again\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":3}}}\n\n", "event: response.completed"},
	{"anthropic", "/anthropic/v1/messages", "anthropic/messages-stream-text", "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" Again\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "event: message_stop"},
}

func fixtureOrFail(t testing.TB, name string) []byte {
	t.Helper()
	b, err := fixtures.Bytes(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func finalizerProxy(t testing.TB, opts core.Options, fs ...model.Finalizer) string {
	t.Helper()
	opts.Finalizers = fs
	return newProxyServer(t, opts)
}

func callWith(t testing.TB, base string, wc wireCase, up *seqUpstream) []byte {
	t.Helper()
	resp := post(t, base, wc.path, requestBody(t, wc.first), map[string]string{
		"x-upstream-url": up.srv.URL, "Authorization": "Bearer sk-live-123", "x-api-key": "sk-ant-9"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	return readAll(t, resp)
}

// TestFinalizer_RecoverOnceThenRelease is the milestone demo: the finalizer
// recovers the first stream, releases the second; the client reads the
// original content, the recovery content and one terminal event, and the
// upstream saw a second request with the same credentials.
func TestFinalizer_RecoverOnceThenRelease(t *testing.T) {
	for _, wc := range wireCases {
		t.Run(wc.name, func(t *testing.T) {
			first := fixtureOrFail(t, wc.first)
			up := newSeqUpstream(t, first, []byte(wc.recovery))
			f := &finalizer{fn: func(n int, _ model.StreamSummary) model.Decision {
				if n == 1 {
					return recoverDecision
				}
				return model.Decision{}
			}}
			got := string(callWith(t, finalizerProxy(t, core.Options{}, f), wc, up))
			for _, want := range []string{"Hello", "world", "Again"} {
				if !strings.Contains(got, want) {
					t.Errorf("client stream lacks %q:\n%s", want, got)
				}
			}
			if n := strings.Count(got, wc.terminal); n != 1 {
				t.Errorf("%d terminal events (%s), want exactly one:\n%s", n, wc.terminal, got)
			}
			if wc.name == "anthropic" && strings.Count(got, "event: message_start") != 1 {
				t.Errorf("the client must see one message_start:\n%s", got)
			}
			if wc.name == "responses" {
				if n := strings.Count(got, "event: response.created"); n != 1 {
					t.Errorf("the client must see one response.created, saw %d:\n%s", n, got)
				}
				if !strings.Contains(got, `"output_index":1`) {
					t.Errorf("the recovery's output item must continue after the original's:\n%s", got)
				}
			}
			if f.calls.Load() != 2 {
				t.Errorf("Decide ran %d times, want 2 (recover, then release)", f.calls.Load())
			}
			hdrs, sent := up.requests()
			if len(hdrs) != 2 {
				t.Fatalf("upstream saw %d requests, want 2", len(hdrs))
			}
			for _, h := range []string{"Authorization", "X-Api-Key"} {
				if hdrs[1].Get(h) == "" || hdrs[1].Get(h) != hdrs[0].Get(h) {
					t.Errorf("recovery %s = %q, original %q", h, hdrs[1].Get(h), hdrs[0].Get(h))
				}
			}
			if !bytes.Equal(sent[1], recoverDecision.Body) {
				t.Errorf("recovery body = %q", sent[1])
			}
		})
	}
}

// TestFinalizer_ReleaseIsByteIdenticalToNoFinalizer runs every streamed
// fixture with and without a releasing finalizer.
func TestFinalizer_ReleaseIsByteIdenticalToNoFinalizer(t *testing.T) {
	for _, name := range fixtures.Names() {
		if !strings.Contains(name, "stream") || strings.Contains(name, "/request-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{ChunkDelay: 0})
			rel := &finalizer{fn: func(int, model.StreamSummary) model.Decision { return model.Decision{} }}
			run := func(base string) []byte {
				return readAll(t, post(t, base, pathFor(name), requestBody(t, name), via(up, fixtureHeader, name)))
			}
			plain, held := run(newProxyServer(t, core.Options{})), run(finalizerProxy(t, core.Options{}, rel))
			want, _ := fixtures.Bytes(name)
			if !bytes.Equal(plain, want) || !bytes.Equal(held, want) {
				t.Fatalf("client bytes differ from the fixture")
			}
		})
	}
}
