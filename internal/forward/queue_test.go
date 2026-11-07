package forward

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

type delivered struct {
	frame string
	gap   int64
}

func readAllFrames(q *FrameQueue) []delivered {
	var out []delivered
	for f, gap, ok := q.Next(); ok; f, gap, ok = q.Next() {
		out = append(out, delivered{string(f), gap})
	}
	return out
}

// TestFrameQueue_ByteBoundDropsAndMarksGaps covers review-003 STR-3: the
// queue holds at most its byte budget; frames over it are dropped, counted
// and reported as a gap on the next delivered frame or at End.
func TestFrameQueue_ByteBoundDropsAndMarksGaps(t *testing.T) {
	q := NewFrameQueue(8) // 8 frames, max(8*4 KiB, MaxFrameBytes) = 1 MiB
	big := bytes.Repeat([]byte("x"), 600<<10)
	q.offer(big)               // 600 KiB queued
	q.offer(big)               // 1.2 MiB would pass the cap: dropped
	q.offer([]byte("data: a")) // fits: carries gap 1
	q.offer(big)               // still over: dropped, trailing gap
	if got := q.bytes.Load(); got != int64(len(big))+7 {
		t.Fatalf("queued bytes = %d", got)
	}
	q.finish(model.FinishOK)
	got := readAllFrames(q)
	if len(got) != 2 || got[0].gap != 0 || got[1] != (delivered{"data: a", 1}) {
		t.Fatalf("delivered = %+v", got)
	}
	if q.Dropped() != 2 || q.End() != (End{Finish: model.FinishOK, Gap: 1}) || q.bytes.Load() != 0 {
		t.Fatalf("dropped=%d end=%+v bytes=%d", q.Dropped(), q.End(), q.bytes.Load())
	}
}

func TestFrameQueue_CountBoundMarksGap(t *testing.T) {
	q := NewFrameQueue(1)
	q.offer([]byte("a"))
	q.offer([]byte("b")) // full
	if f, gap, ok := q.Next(); !ok || string(f) != "a" || gap != 0 {
		t.Fatalf("first = %q %d %v", f, gap, ok)
	}
	q.offer([]byte("c"))
	q.finish(model.FinishTimeout)
	if got := readAllFrames(q); !slices.Equal(got, []delivered{{"c", 1}}) || q.End().Finish != model.FinishTimeout {
		t.Fatalf("got %+v end %+v", got, q.End())
	}
}

// TestForward_ConsumerSeesMidStreamDrop covers review-003 TEST-3: with a
// queue attached, an upstream that breaks mid-frame closes the queue, the
// partial frame is discarded, delivered frames survive and End says why.
func TestForward_ConsumerSeesMidStreamDrop(t *testing.T) {
	rt := rtFunc(func(*http.Request) (*http.Response, error) {
		body := &chunkReader{chunks: []string{"data: 1\n\n", "data: 2\n\ndata: part"}, err: errors.New("reset")}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: noopCloser{body}}, nil
	})
	target, _ := url.Parse("http://upstream/v1")
	q := NewFrameQueue(8)
	rec := httptest.NewRecorder()
	res, err := Forward(context.Background(), rt, rec, Request{Method: "POST", Target: target, Frames: q})
	if err != nil || res.Finish != model.FinishUpstreamError || res.Cut {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got := readAllFrames(q); !slices.Equal(got, []delivered{{"data: 1", 0}, {"data: 2", 0}}) {
		t.Fatalf("frames = %+v", got)
	}
	if q.End() != (End{Finish: model.FinishUpstreamError}) {
		t.Fatalf("end = %+v", q.End())
	}
}
