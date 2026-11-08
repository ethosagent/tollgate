package core_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

const tinyRequest = `{"model":"m","messages":[]}`

func TestHooks_OnResponseSeesAndCanReplaceTheBufferedBody(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	want, _ := fixtures.Bytes("openai/chat")
	var seenBody atomic.Value
	done := make(chan model.Metric, 1)
	h := &tHook{name: "rewrite",
		onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
			seenBody.Store(string(r.Body))
			r.Header.Set("X-Hooked", "1")
			r.Body = []byte(`{"replaced":true}`)
			return r, nil
		},
		after: func(_ context.Context, _ *model.Call, m model.Metric) { done <- m }}
	base, _ := newHookedProxy(t, core.Options{}, h)
	resp := post(t, base, chatPath, []byte(plainRequest), via(up))
	body := readAll(t, resp)
	if string(body) != `{"replaced":true}` || resp.Header.Get("X-Hooked") != "1" || resp.Header.Get("Content-Type") != "application/json" ||
		resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) || resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%q headers=%v", resp.StatusCode, body, resp.Header)
	}
	if seenBody.Load() != string(want) {
		t.Fatalf("hook saw %q, want the upstream body", seenBody.Load())
	}
	if m := <-done; m.Usage.InputTokens != 12 || m.Usage.OutputTokens != 2 || m.Stream || m.Status != 200 {
		t.Fatalf("metric = %+v, want usage from the original response", m)
	}
}

func TestHooks_OnResponseUnchangedIsByteExact(t *testing.T) {
	for _, name := range []string{"openai/chat", "openai/error-429", "anthropic/messages", "anthropic/error-500"} {
		t.Run(name, func(t *testing.T) {
			up := newFakeUpstream(t, upstreamOptions{})
			var calls atomic.Int32
			h := &tHook{name: "obs", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
				calls.Add(1)
				return r, nil
			}}
			base, _ := newHookedProxy(t, core.Options{}, h)
			resp := post(t, base, pathFor(name), []byte(`{"model":"m","messages":[]}`), via(up, fixtureHeader, name))
			want, _ := fixtures.Bytes(name)
			if got := readAll(t, resp); !bytes.Equal(got, want) || calls.Load() != 1 {
				t.Fatalf("body %q (hook calls %d), want the fixture once", got, calls.Load())
			}
		})
	}
}

func TestHooks_ResponseNotBufferedIsPassedThroughAndCounted(t *testing.T) {
	payload := strings.Repeat("x", 200)
	tests := []struct {
		name, reason string
		handler      http.HandlerFunc
		want         []byte
	}{
		{"over cap, length unknown", "over_cap", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, payload[:100])
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, payload[100:])
		}, []byte(payload)},
		{"over cap, length declared", "over_cap", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = io.WriteString(w, payload)
		}, []byte(payload)},
		{"encoded body", "encoded", func(w http.ResponseWriter, _ *http.Request) {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			_, _ = zw.Write([]byte(`{"ok":true}`))
			_ = zw.Close()
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(b.Bytes())
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)
			logs := &syncBuf{}
			var calls atomic.Int32
			h := &tHook{name: "obs", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
				calls.Add(1)
				r.Body = []byte("changed")
				return r, nil
			}}
			base, p := newHookedProxy(t, core.Options{Logger: slogTo(logs), Limits: core.Limits{MaxRequestBody: 64}}, h)
			resp := post(t, base, chatPath, []byte(tinyRequest), map[string]string{"x-upstream-url": srv.URL, "Accept-Encoding": "gzip"})
			resp.Header.Del("Content-Length")
			got := readAllRaw(t, resp)
			if tt.want != nil && !bytes.Equal(got, tt.want) {
				t.Fatalf("body = %q, want it unmodified", got)
			}
			if string(got) == "changed" || calls.Load() != 0 {
				t.Fatalf("hook ran (%d) or its edit reached the client: %q", calls.Load(), got)
			}
			eventually(t, "the skip count", func() bool { return p.UnbufferedResponses() == 1 })
			if g := logs.String(); !strings.Contains(g, "reason="+tt.reason) || !strings.Contains(g, "tenant=acme") || !strings.Contains(g, "call_id=") {
				t.Fatalf("log = %q", g)
			}
		})
	}
}

func TestHooks_BrokenBufferedBodyIsCutNotRepaired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{"partial":`)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	var calls atomic.Int32
	h := &tHook{name: "obs", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
		calls.Add(1)
		return r, nil
	}}
	base, _ := newHookedProxy(t, core.Options{}, h)
	resp := post(t, base, chatPath, []byte(tinyRequest), map[string]string{"x-upstream-url": srv.URL})
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil || string(got) != `{"partial":` || calls.Load() != 0 {
		t.Fatalf("body=%q err=%v hook calls=%d, want the partial body then a cut connection and no OnResponse", got, err, calls.Load())
	}
}

func TestHooks_NormalisedWireOnlyUnparseableAndPassthroughSkipParsing(t *testing.T) {
	up := newFakeUpstream(t, upstreamOptions{})
	var befores, responses atomic.Int32
	h := &tHook{name: "obs",
		before: func(_ context.Context, c *model.Call) (*model.Response, error) {
			befores.Add(1)
			if c.Model != "" || c.Stream || len(c.Messages) != 0 {
				panic("call was parsed on a pass-through wire")
			}
			return nil, nil
		},
		onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
			responses.Add(1)
			return r, nil
		}}
	t.Run("pass-through wire runs Before only", func(t *testing.T) {
		opts := core.Options{Providers: rawResolver{up}}
		base, _ := newHookedProxy(t, opts, h)
		resp := post(t, base, "/raw/v1/chat/completions", requestBody(t, streamFixture), via(up))
		want, _ := fixtures.Bytes(streamFixture)
		if got := readAll(t, resp); !bytes.Equal(got, want) || befores.Load() != 1 || responses.Load() != 0 {
			t.Fatalf("befores=%d responses=%d body ok=%v", befores.Load(), responses.Load(), bytes.Equal(got, want))
		}
	})
	t.Run("unparseable body skips every hook", func(t *testing.T) {
		befores.Store(0)
		base, _ := newHookedProxy(t, core.Options{}, h)
		resp := post(t, base, chatPath, []byte(`not json`), via(up, fixtureHeader, "openai/chat"))
		want, _ := fixtures.Bytes("openai/chat")
		if got := readAll(t, resp); !bytes.Equal(got, want) || befores.Load() != 0 || responses.Load() != 0 {
			t.Fatalf("befores=%d responses=%d, want no hook for a body that does not parse", befores.Load(), responses.Load())
		}
	})
}
