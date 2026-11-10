package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
	"github.com/ethosagent/tollgate/internal/model"
)

// cutUpstream sends one whole SSE frame, then half of another, then drops the
// connection.
func cutUpstream(t *testing.T, first, partial string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(first))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(partial))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // the connection drops without the final chunk
	}))
	t.Cleanup(srv.Close)
	return srv
}

func cutBody(t *testing.T, path string, up *httptest.Server) string {
	t.Helper()
	base := newProxyServer(t, core.Options{})
	resp := post(t, base, path, []byte(`{"model":"m","stream":true}`), map[string]string{"x-upstream-url": up.URL})
	return string(readAllRaw(t, resp))
}

// TestProxy_CutMidFrameTerminatesThePartialFrame: the injected error event
// starts on its own frame, whatever the wire, so the client never reads it as
// the tail of the half frame that came before.
func TestProxy_CutMidFrameTerminatesThePartialFrame(t *testing.T) {
	tests := []struct {
		name, path, first, partial string
	}{
		{"chat mid-line", "/openai/v1/chat/completions", "data: {\"choices\":[]}\n\n", `data: {"choices":[{"delta":{"con`},
		{"responses mid-line", "/openai/v1/responses", "event: response.created\ndata: {}\n\n", "event: response.output_text.delta\ndata: {\"type\":\"response.output_te"},
		{"anthropic mid-line", "/anthropic/v1/messages", "event: ping\ndata: {\"type\":\"ping\"}\n\n", "event: content_block_delta\ndata: {\"type\":\"content_bl"},
		{"chat line complete, frame open", "/openai/v1/chat/completions", "data: {\"choices\":[]}\n\n", "data: {\"choices\":[]}\n"},
		{"chat at a frame break", "/openai/v1/chat/completions", "data: {\"choices\":[]}\n\n", "data: {\"choices\":[]}\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cutBody(t, tt.path, cutUpstream(t, tt.first, tt.partial))
			sent := tt.first + tt.partial
			if !strings.HasPrefix(got, sent) {
				t.Fatalf("body %q does not start with what the upstream sent", got)
			}
			rest := strings.TrimPrefix(got, sent)
			wantBreak := ""
			switch {
			case strings.HasSuffix(sent, "\n\n"): // already at a frame break
			case strings.HasSuffix(sent, "\n"):
				wantBreak = "\n"
			default:
				wantBreak = "\n\n"
			}
			if !strings.HasPrefix(rest, wantBreak) || strings.HasPrefix(rest[len(wantBreak):], "\n") {
				t.Fatalf("after %q the proxy wrote %q, want %q then an error frame", sent, rest, wantBreak)
			}
			frame := rest[len(wantBreak):]
			if !strings.HasPrefix(frame, "data: ") && !strings.HasPrefix(frame, "event: error\ndata: ") || !strings.HasSuffix(frame, "\n\n") {
				t.Fatalf("error frame = %q", frame)
			}
		})
	}
}

// TestProxy_CutResponsesStreamGetsAResponsesErrorEvent: the in-band error on
// a Responses stream is the typed "event: error" the API sends, which Codex
// and the SDKs parse; chat keeps its bare data frame.
func TestProxy_CutResponsesStreamGetsAResponsesErrorEvent(t *testing.T) {
	up := cutUpstream(t, "event: response.created\ndata: {}\n\n", "event: response.output_text.delta\ndata: {\"type\":\"resp")
	got := cutBody(t, "/openai/v1/responses", up)
	_, frame, ok := strings.Cut(got, "\n\nevent: error\ndata: ")
	if !ok {
		t.Fatalf("body = %q, want an event: error frame", got)
	}
	if !strings.HasPrefix(frame, `{"code":"server_error","message":"upstream error","param":null,"type":"error"}`) {
		t.Fatalf("error frame data = %q", frame)
	}
}

// TestProxy_CutMidFrameOnTheHeldAndCopiedPaths: a stream:false call whose
// upstream answers with an event stream goes through Capture (an OnResponse
// hook) or BodyCopy (a usage reader). Both pass the stream straight on, and
// the injected error event must still start on its own frame.
func TestProxy_CutMidFrameOnTheHeldAndCopiedPaths(t *testing.T) {
	onResp := &tHook{name: "obs", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) { return r, nil }}
	tests := []struct {
		name  string
		hooks []model.Hook
	}{
		{"capture (OnResponse hook)", []model.Hook{onResp}},
		{"body copy (usage reader)", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := cutUpstream(t, "data: {\"choices\":[]}\n\n", `data: {"choices":[{"delta":{"con`)
			base, _, _ := metricProxy(t, core.Options{}, tt.hooks...)
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
				map[string]string{"x-upstream-url": up.URL})
			got := string(readAllRaw(t, resp))
			want := "data: {\"choices\":[]}\n\ndata: {\"choices\":[{\"delta\":{\"con\n\ndata: {\"error\":"
			if !strings.HasPrefix(got, want) {
				t.Fatalf("body = %q, want the error frame on its own after %q", got, want)
			}
		})
	}
}
