package core_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

const (
	chatPath     = "/openai/v1/chat/completions"
	plainRequest = `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`
)

func TestHooks_BeforeRunsInUseOrderAndRewritesBody(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	var mu sync.Mutex
	var order []string
	var first model.Call
	var secondSawRewrite bool
	h1 := &tHook{name: "first", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		order, first = append(order, "first"), *c
		c.Body = bytes.Replace(c.Body, []byte("gpt-x"), []byte("gpt-y-longer"), 1)
		return nil, nil
	}}
	h2 := &tHook{name: "second", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		order, secondSawRewrite = append(order, "second"), bytes.Contains(c.Body, []byte("gpt-y-longer"))
		return nil, nil
	}}
	base, _ := newHookedProxy(t, core.Options{}, h1, h2)
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	want, _ := fixtures.Bytes("openai/chat")
	if got := readAll(t, resp); !bytes.Equal(got, want) {
		t.Fatalf("response = %q, want the upstream fixture unchanged", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "first,second" || !secondSawRewrite {
		t.Fatalf("order=%v secondSawRewrite=%v, want Use order and the rewrite visible to the next hook", order, secondSawRewrite)
	}
	rewritten := strings.Replace(plainRequest, "gpt-x", "gpt-y-longer", 1)
	got := up.Requests()[0]
	if string(got.Body) != rewritten || got.Header.Get("Content-Length") != strconv.Itoa(len(rewritten)) {
		t.Fatalf("upstream body=%q content-length=%q, want the rewritten body and its length", got.Body, got.Header.Get("Content-Length"))
	}
	if first.Model != "gpt-x" || first.Stream || len(first.Messages) != 1 || first.Messages[0].Text != "hi" ||
		first.Provider.Name != "openai" || first.Path != chatPath || first.Tenant.ID != "acme" || first.ID == "" {
		t.Fatalf("hook saw call %+v, want it parsed once from the request", first)
	}
}

func TestHooks_BeforeResponseShortCircuitsWithoutUpstream(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	var ranAfter atomic.Bool
	gate := &tHook{name: "gate", before: func(context.Context, *model.Call) (*model.Response, error) {
		return &model.Response{Status: http.StatusTeapot, Header: http.Header{"X-Gate": {"1"}}, Body: []byte(`{"cached":true}`)}, nil
	}}
	later := &tHook{name: "later", before: func(context.Context, *model.Call) (*model.Response, error) {
		ranAfter.Store(true)
		return nil, nil
	}}
	done := make(chan model.Metric, 1)
	gate.after = func(_ context.Context, _ *model.Call, m model.Metric) { done <- m }
	base, _ := newHookedProxy(t, core.Options{}, gate, later)
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusTeapot || string(body) != `{"cached":true}` || resp.Header.Get("X-Gate") != "1" ||
		resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("status=%d body=%q headers=%v", resp.StatusCode, body, resp.Header)
	}
	if len(up.Requests()) != 0 || ranAfter.Load() {
		t.Fatalf("upstream contacted (%d) or a later hook ran (%v) after a short-circuit", len(up.Requests()), ranAfter.Load())
	}
	if m := <-done; m.Status != http.StatusTeapot || m.Finish != model.FinishRejected {
		t.Fatalf("After metric = %+v, want the short-circuit status and finish rejected", m)
	}
}

func TestHooks_BeforeErrorSkipsTheHookAndTheCallProceeds(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	logs := &syncBuf{}
	bad := &tHook{name: "bad", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		c.Body = []byte("corrupted")
		return nil, errors.New("tenant-secret-detail")
	}}
	base, _ := newHookedProxy(t, core.Options{Logger: slogTo(logs)}, bad)
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	want, _ := fixtures.Bytes("openai/chat")
	if got := readAll(t, resp); !bytes.Equal(got, want) || string(up.Requests()[0].Body) != plainRequest {
		t.Fatalf("response %q / upstream body %q, want pass-through of the original", got, up.Requests()[0].Body)
	}
	if g := logs.String(); !strings.Contains(g, "hook=bad") || strings.Contains(g, "tenant-secret-detail") {
		t.Fatalf("log = %q, want the hook named and the error text withheld", g)
	}
}

func TestHooks_UseAfterHandlerAppliesOnlyToLaterCalls(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	entered, gate := make(chan struct{}), make(chan struct{})
	var h1Calls, h2Befores, h2Afters atomic.Int32
	h1 := &tHook{name: "h1", before: func(context.Context, *model.Call) (*model.Response, error) {
		if h1Calls.Add(1) == 1 {
			close(entered)
			<-gate
		}
		return nil, nil
	}}
	h2 := &tHook{name: "h2",
		before: func(context.Context, *model.Call) (*model.Response, error) { h2Befores.Add(1); return nil, nil },
		after:  func(context.Context, *model.Call, model.Metric) { h2Afters.Add(1) }}
	opts := core.Options{Limits: core.Limits{HookBeforeDeadline: 10 * time.Second, HookChainDeadline: 20 * time.Second}}
	base, settled, p := metricProxy(t, opts, h1) // a call's Metric is emitted after its After hooks ran

	inFlight := make(chan struct{})
	go func() {
		defer close(inFlight)
		_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	}()
	<-entered
	p.Use(h2) // call A is inside its Before chain
	close(gate)
	<-inFlight
	eventually(t, "call A settled", func() bool { return len(settled.all()) == 1 })
	if h2Befores.Load() != 0 || h2Afters.Load() != 0 {
		t.Fatal("a hook added mid-call ran for that call")
	}
	_ = readAll(t, post(t, base, chatPath, []byte(plainRequest), via(up)))
	eventually(t, "h2 After for call B", func() bool { return h2Afters.Load() == 1 })
	if h2Befores.Load() != 1 || h1Calls.Load() != 2 {
		t.Fatalf("h2 Before ran %d times, h1 %d; want 1 and 2", h2Befores.Load(), h1Calls.Load())
	}
}

// TestChain_PanickingHookDegradesToPassThrough is the 005 demo: a hook that
// panics in any method is logged and skipped, later hooks still run, and the
// call completes byte-exact.
func TestChain_PanickingHookDegradesToPassThrough(t *testing.T) {
	tests := []struct {
		method, fixture string
		stream          bool
		panics          func(h *tHook)
		saw             func(g *goodHook) bool
	}{
		{"before", "openai/chat-stream-text", true,
			func(h *tHook) {
				h.before = func(context.Context, *model.Call) (*model.Response, error) { panic("x") }
			},
			func(g *goodHook) bool { return g.befores.Load() == 1 }},
		{"on_event", "openai/chat-stream-text", true,
			func(h *tHook) { h.onEvent = func(context.Context, *model.Call, model.Event) { panic("x") } },
			func(g *goodHook) bool { return g.events.Load() > 0 }},
		{"on_response", "openai/chat", false,
			func(h *tHook) {
				h.onResp = func(context.Context, *model.Call, *model.Response) (*model.Response, error) { panic("x") }
			},
			func(g *goodHook) bool { return g.responses.Load() == 1 }},
		{"after", "openai/chat-stream-text", true,
			func(h *tHook) { h.after = func(context.Context, *model.Call, model.Metric) { panic("x") } },
			func(*goodHook) bool { return true }},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			logs := &syncBuf{}
			bad := &tHook{name: "bad"}
			tt.panics(bad)
			good := newGoodHook()
			base, _ := newHookedProxy(t, core.Options{Logger: slogTo(logs)}, bad, good.hook())
			body := []byte(plainRequest)
			if tt.stream {
				body = requestBody(t, tt.fixture)
			}
			resp := post(t, base, chatPath, body, via(up))
			want, _ := fixtures.Bytes(tt.fixture)
			if got := readAll(t, resp); !bytes.Equal(got, want) || resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%q, want the fixture byte-exact", resp.StatusCode, got)
			}
			<-good.afterDone // After is the last stage: every log line is written
			g := logs.String()
			for _, w := range []string{"hook=bad", "method=" + tt.method, "tenant=acme", "call_id=", "hook panicked"} {
				if !strings.Contains(g, w) {
					t.Errorf("log %q lacks %q", g, w)
				}
			}
			if !tt.saw(good) {
				t.Fatalf("the healthy hook did not run its %s stage after the panicking one", tt.method)
			}
		})
	}
}

// goodHook counts what a healthy hook sees.
type goodHook struct {
	befores, events, responses atomic.Int32
	afterDone                  chan struct{}
}

func newGoodHook() *goodHook { return &goodHook{afterDone: make(chan struct{})} }

func (g *goodHook) hook() *tHook {
	return &tHook{name: "good",
		before:  func(context.Context, *model.Call) (*model.Response, error) { g.befores.Add(1); return nil, nil },
		onEvent: func(context.Context, *model.Call, model.Event) { g.events.Add(1) },
		onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
			g.responses.Add(1)
			return r, nil
		},
		after: func(context.Context, *model.Call, model.Metric) { close(g.afterDone) }}
}
