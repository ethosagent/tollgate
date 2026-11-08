package forward

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// TestFinal_DeclaredLengthBypassesTheBarrier: an event stream that declared
// its Content-Length relays that header, so the barrier must not change the
// body's length. The client, through a real net/http server that enforces
// the length, gets the upstream bytes exactly, and Decide is never asked.
func TestFinal_DeclaredLengthBypassesTheBarrier(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/testdata/openai/chat-stream-text.sse")
	if err != nil {
		t.Fatal(err)
	}
	decided := 0
	fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision {
		decided++
		return model.Decision{Action: model.DecisionReplace, Response: &model.Response{Body: bytes.Repeat([]byte("x"), 4096)}}
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up := sseResp(raw, 50)
		up.Header.Set("Content-Length", strconv.Itoa(len(raw)))
		req := Request{Method: http.MethodPost, Target: mustTarget(t), Header: http.Header{"Content-Type": {"application/json"}},
			Body: []byte(`{"stream":true}`), Final: fz}
		if _, err := Forward(r.Context(), &upScript{bodies: []*http.Response{up}}, w, req); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("client read: %v (got %d of %d bytes)", err, len(got), len(raw))
	}
	if !bytes.Equal(got, raw) || decided != 0 {
		t.Fatalf("client got %d bytes (want %d exact), Decide called %d times (want 0)", len(got), len(raw), decided)
	}
}

// TestTerminate_InBandOnlyWithoutDeclaredLength: the in-band error event
// lengthens the body, so a response whose head declared a length is cut
// (terminate reports false) instead.
func TestTerminate_InBandOnlyWithoutDeclaredLength(t *testing.T) {
	tests := []struct {
		name       string
		header     http.Header
		wantInBand bool
	}{
		{"event stream, chunked", http.Header{"Content-Type": {"text/event-stream"}}, true},
		{"event stream, declared length", http.Header{"Content-Type": {"text/event-stream"}, "Content-Length": {"100"}}, false},
		{"json body", http.Header{"Content-Type": {"application/json"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &recWriter{}
			got := terminate(w, tt.header, normalise.ShapeOpenAI, model.FinishUpstreamError)
			if got != tt.wantInBand || (w.buf.Len() > 0) != tt.wantInBand {
				t.Fatalf("terminate = %v, wrote %d bytes; want in-band %v", got, w.buf.Len(), tt.wantInBand)
			}
		})
	}
}
