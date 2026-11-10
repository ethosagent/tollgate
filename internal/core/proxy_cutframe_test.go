package core_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/core"
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
