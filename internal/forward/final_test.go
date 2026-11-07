package forward

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/normalise"
)

// sseResp is a 200 event-stream response whose body arrives in reads of n bytes.
func sseResp(raw []byte, n int) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(reads(raw, n))}
}

// upScript is the sequence of upstream responses a test serves, one per call.
type upScript struct {
	bodies []*http.Response
	reqs   []*http.Request
	sent   [][]byte
}

func (s *upScript) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	s.reqs, s.sent = append(s.reqs, r), append(s.sent, b)
	if len(s.reqs) > len(s.bodies) {
		return nil, errors.New("no more responses")
	}
	return s.bodies[len(s.reqs)-1], nil
}

// finalizeFor builds the barrier for a wire with a scripted Decide.
func finalizeFor(t *testing.T, f model.WireFormat, decide func(model.StreamSummary) model.Decision) *Finalize {
	t.Helper()
	w, err := normalise.ForWire(f)
	if err != nil {
		t.Fatal(err)
	}
	term, _ := normalise.TerminalFor(f)
	return &Finalize{Wire: w, Term: term, Decide: func(_ context.Context, s model.StreamSummary) model.Decision { return decide(s) }}
}

func forwardFinal(t *testing.T, rt http.RoundTripper, w http.ResponseWriter, fz *Finalize, q *FrameQueue) (Result, error) {
	t.Helper()
	target, _ := url.Parse("http://127.0.0.1:1/v1")
	h := http.Header{"Authorization": {"Bearer sk-test"}, "Content-Type": {"application/json"}}
	return Forward(context.Background(), rt, w, Request{Method: http.MethodPost, Target: target, Header: h,
		Body: []byte(`{"stream":true}`), Final: fz, Frames: q})
}

// frames reads a closed queue dry.
func frames(q *FrameQueue) []string {
	var out []string
	for f, _, ok := q.Next(); ok; f, _, ok = q.Next() {
		out = append(out, string(f))
	}
	return out
}

func release(model.StreamSummary) model.Decision { return model.Decision{} }

type fixtureCase struct {
	file string
	wire model.WireFormat
}

func fixtures(t *testing.T) []fixtureCase {
	t.Helper()
	var out []fixtureCase
	for _, w := range []struct {
		dir  string
		wire model.WireFormat
	}{{"openai", model.WireOpenAIChat}, {"anthropic", model.WireAnthropicMessages}} {
		files, _ := filepath.Glob("../fixtures/testdata/" + w.dir + "/*.sse")
		for _, f := range files {
			out = append(out, fixtureCase{f, w.wire})
		}
	}
	if len(out) == 0 {
		t.Fatal("no fixtures")
	}
	return out
}

// spans records every frame end the Scanner reports.
type spans struct {
	frames [][]byte // nil for frames without a field
	ends   []int
}

func (s *spans) frame(f []byte, end int) {
	s.frames, s.ends = append(s.frames, bytes.Clone(f)), append(s.ends, end)
}

// terminalStart is where the terminal region of raw begins, by the book:
// the start of the first frame the Terminal opens on, or -1.
func terminalStart(raw []byte, term normalise.Terminal) int {
	var sc Scanner
	sp := &spans{}
	sc.scan(raw, sp)
	sc.finish(sp)
	start := 0
	for i, f := range sp.frames {
		if f != nil {
			if ev, data := normalise.SplitFrame(f); term.Opens(ev, data) {
				return start
			}
		}
		start = sp.ends[i]
	}
	return -1
}

func TestFinal_ReleaseIsByteExactAndHoldsOnlyTheTerminal(t *testing.T) {
	for _, fc := range fixtures(t) {
		lf, err := os.ReadFile(fc.file)
		if err != nil {
			t.Fatal(err)
		}
		for _, eol := range []string{"\n", "\r\n", "\r"} { // the tee's Scanner takes all three
			raw := bytes.ReplaceAll(lf, []byte("\n"), []byte(eol))
			for _, n := range []int{1, 7, 37, 4096} {
				releaseCase(t, fc, raw, n, strconv.Quote(eol))
			}
		}
	}
}

// releaseCase checks one fixture, line ending and read size: the client gets
// the upstream bytes exactly, and at Decide it had exactly those before the
// terminal region.
func releaseCase(t *testing.T, fc fixtureCase, raw []byte, n int, eol string) {
	t.Helper()
	t.Run(filepath.Base(fc.file)+"/"+eol+"/"+strconv.Itoa(n), func(t *testing.T) {
		w := &recWriter{}
		var atDecide []byte
		fz := finalizeFor(t, fc.wire, release)
		fz.Decide = func(context.Context, model.StreamSummary) model.Decision {
			atDecide = bytes.Clone(w.buf.Bytes())
			return model.Decision{}
		}
		res, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, n)}}, w, fz, nil)
		if err != nil || res.Finish != model.FinishOK {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if !bytes.Equal(w.buf.Bytes(), raw) {
			t.Fatalf("client bytes differ from the upstream bytes:\n%q\n%q", w.buf.Bytes(), raw)
		}
		if at := terminalStart(raw, fz.Term); at >= 0 && !bytes.Equal(atDecide, raw[:at]) {
			t.Fatalf("at Decide the client had %d bytes, want exactly the %d before the terminal region", len(atDecide), at)
		}
	})
}

func TestFinal_NoTerminalMeansNoDecide(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n")
	fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { t.Fatal("Decide called"); return model.Decision{} })
	w := &recWriter{}
	if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 5)}}, w, fz, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.buf.Bytes(), raw) {
		t.Fatalf("out = %q", w.buf.Bytes())
	}
}

func TestFinal_UnterminatedLastFrameIsStillDecided(t *testing.T) {
	raw := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]") // no closing blank line
	calls := 0
	fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { calls++; return model.Decision{} })
	w := &recWriter{}
	if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 9)}}, w, fz, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Equal(w.buf.Bytes(), raw) {
		t.Fatalf("calls=%d out=%q", calls, w.buf.Bytes())
	}
}

func TestFinal_SummaryDescribesTheStream(t *testing.T) {
	raw, _ := os.ReadFile("../fixtures/testdata/openai/chat-stream-tools.sse")
	var got model.StreamSummary
	fz := finalizeFor(t, model.WireOpenAIChat, func(s model.StreamSummary) model.Decision { got = s; return model.Decision{} })
	if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 64)}}, &recWriter{}, fz, nil); err != nil {
		t.Fatal(err)
	}
	if got.FinishReason != "tool_calls" || got.Events == 0 || len(got.ToolCalls) == 0 || got.ToolCalls[0].Name == "" {
		t.Fatalf("summary = %+v", got)
	}
}

func TestFinal_ReplaceSendsTheReplacementInsteadOfTheTerminal(t *testing.T) {
	raw, _ := os.ReadFile("../fixtures/testdata/openai/chat-stream-text.sse")
	repl := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n")
	fz := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision {
		return model.Decision{Action: model.DecisionReplace, Response: &model.Response{Body: repl}}
	})
	w := &recWriter{}
	if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 50)}}, w, fz, nil); err != nil {
		t.Fatal(err)
	}
	want := append(slices.Clone(raw[:terminalStart(raw, fz.Term)]), repl...)
	if !bytes.Equal(w.buf.Bytes(), want) {
		t.Fatalf("out = %q\nwant %q", w.buf.Bytes(), want)
	}
}

func TestFinal_ConsumerSeesTheFramesTheClientGot(t *testing.T) {
	for _, fc := range fixtures(t) {
		raw, _ := os.ReadFile(fc.file)
		t.Run(filepath.Base(fc.file), func(t *testing.T) {
			plain, final := NewFrameQueue(len(raw)), NewFrameQueue(len(raw))
			if _, err := Tee(&recWriter{}, reads(raw, 29), plain, nil); err != nil {
				t.Fatal(err)
			}
			fz := finalizeFor(t, fc.wire, release)
			if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 29)}}, &recWriter{}, fz, final); err != nil {
				t.Fatal(err)
			}
			plain.finish(model.FinishOK)
			if got, want := frames(final), frames(plain); !slices.Equal(got, want) {
				t.Fatalf("frames\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestFinal_Failures(t *testing.T) {
	raw, _ := os.ReadFile("../fixtures/testdata/openai/chat-stream-text.sse")
	fz := func() *Finalize { return finalizeFor(t, model.WireOpenAIChat, release) }
	t.Run("client write error", func(t *testing.T) {
		res, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(raw, 64)}}, &recWriter{failAt: 1}, fz(), nil)
		if err != nil || res.Finish != model.FinishClientCancel {
			t.Fatalf("res=%+v err=%v, want client_cancel", res, err)
		}
	})
	t.Run("upstream drop after the terminal still releases it", func(t *testing.T) {
		body := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(&chunkReader{chunks: []string{string(raw)}, err: errors.New("reset")})}
		w := &recWriter{}
		res, err := forwardFinal(t, &upScript{bodies: []*http.Response{body}}, w, fz(), nil)
		if err != nil || res.Finish != model.FinishUpstreamError {
			t.Fatalf("res=%+v err=%v", res, err)
		}
		if !strings.HasPrefix(w.buf.String(), string(raw)) {
			t.Fatalf("the held terminal must go out before the error event: %q", w.buf.String())
		}
	})
	t.Run("an unending frame is relayed raw and the terminal is still held", func(t *testing.T) {
		big := append([]byte("data: "), bytes.Repeat([]byte("a"), MaxFrameBytes+10)...)
		big = append(big, "\n\ndata: [DONE]\n\n"...)
		called := false
		f := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { called = true; return model.Decision{} })
		w := &recWriter{}
		body := append([]byte("data: "+strings.Repeat("b", 10)), big[6:]...)
		if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{sseResp(body, 16<<10)}}, w, f, nil); err != nil {
			t.Fatal(err)
		}
		if !called || !bytes.Equal(w.buf.Bytes(), body) {
			t.Fatalf("called=%v, bytes equal=%v; want the [DONE] after the oversized frame decided on, bytes exact", called, bytes.Equal(w.buf.Bytes(), body))
		}
	})
	t.Run("not an event stream is relayed without the barrier", func(t *testing.T) {
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}
		f := finalizeFor(t, model.WireOpenAIChat, func(model.StreamSummary) model.Decision { t.Fatal("Decide called"); return model.Decision{} })
		w := &recWriter{}
		if _, err := forwardFinal(t, &upScript{bodies: []*http.Response{resp}}, w, f, nil); err != nil || w.buf.String() != "data: [DONE]\n\n" {
			t.Fatalf("err=%v out=%q", err, w.buf.String())
		}
	})
}

func mustTarget(t testing.TB) *url.URL {
	t.Helper()
	u, err := url.Parse("http://127.0.0.1:1/v1")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func benchFinalize() *Finalize {
	w, _ := normalise.ForWire(model.WireOpenAIChat)
	term, _ := normalise.TerminalFor(model.WireOpenAIChat)
	return &Finalize{Wire: w, Term: term, Decide: func(context.Context, model.StreamSummary) model.Decision { return model.Decision{} }}
}
