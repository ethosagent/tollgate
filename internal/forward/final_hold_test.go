package forward

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// probeReader serves chunks and, just before it reports EOF, records what the
// client has been sent so far.
type probeReader struct {
	chunks []string
	w      *recWriter
	atEOF  string
}

func (r *probeReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		r.atEOF = r.w.buf.String()
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

// TestFinal_TerminalIsHeldOnlyWhenAFinalizerIsRegistered is the STR-6 test: the
// same stream, with Final nil the [DONE] is on the client's wire before the
// upstream ends; with a Finalize it is not.
func TestFinal_TerminalIsHeldOnlyWhenAFinalizerIsRegistered(t *testing.T) {
	chunks := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, withFinalizer := range []bool{false, true} {
		w := &recWriter{}
		src := &probeReader{chunks: chunks, w: w}
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(src)}
		var fz *Finalize
		if withFinalizer {
			fz = finalizeFor(t, model.WireOpenAIChat, release)
		}
		target := mustTarget(t)
		if _, err := Forward(context.Background(), &upScript{bodies: []*http.Response{resp}}, w,
			Request{Method: http.MethodPost, Target: target, Final: fz}); err != nil {
			t.Fatal(err)
		}
		held := !strings.Contains(src.atEOF, "[DONE]")
		if held != withFinalizer {
			t.Fatalf("finalizer=%v: terminal held=%v, client had %q before EOF", withFinalizer, held, src.atEOF)
		}
		if !strings.Contains(src.atEOF, `"content":"hi"`) {
			t.Fatalf("content before the terminal must not wait: %q", src.atEOF)
		}
		if !bytes.Equal(w.buf.Bytes(), []byte(strings.Join(chunks, ""))) {
			t.Fatalf("final bytes differ: %q", w.buf.Bytes())
		}
	}
}

// BenchmarkForwardStream is the STR-6 cost: one streamed call through
// Forward, per chunk. "plain" has no Finalize (the unchanged tee path);
// "finalizing" holds and releases the terminal.
func BenchmarkForwardStream(b *testing.B) {
	chunk := []byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n")
	body := append(bytes.Repeat(chunk, 64), "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"...)
	target := mustTarget(b)
	for _, withFinalizer := range []bool{false, true} {
		name := map[bool]string{false: "plain", true: "finalizing"}[withFinalizer]
		b.Run(name, func(b *testing.B) {
			var fz *Finalize
			if withFinalizer {
				fz = benchFinalize()
			}
			w := &nullWriter{h: http.Header{}}
			rt := rtFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
					Body: io.NopCloser(oneChunkPerRead{src: bytes.NewReader(body), size: len(chunk)})}, nil
			})
			req := Request{Method: http.MethodPost, Target: target, Final: fz}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Forward(context.Background(), rt, w, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
