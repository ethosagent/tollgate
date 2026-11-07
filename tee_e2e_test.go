package tollgate_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/internal/forward"
	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/testkit"
)

// teeCall runs forward.Forward against a fake upstream replaying fixture.
func teeCall(t *testing.T, up *testkit.FakeUpstream, fixture string, frames *forward.FrameQueue) (*httptest.ResponseRecorder, forward.Result, time.Duration) {
	t.Helper()
	target, err := url.Parse(up.URL + "/v1" + testkit.PathFor(fixture))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	res, err := forward.Forward(context.Background(), up.Client().Transport, rec,
		forward.Request{Method: "POST", Target: target, Header: http.Header{testkit.FixtureHeader: {fixture}}, Body: []byte(`{"stream":true}`), Frames: frames})
	if err != nil {
		t.Fatal(err)
	}
	return rec, res, time.Since(start)
}

func TestTee_ByteExactWithAndWithoutConsumerOverFixtures(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	for _, name := range testkit.FixtureNames() {
		if !strings.Contains(name, "stream") || strings.Contains(name, "/request-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			want, _ := testkit.Fixture(name)
			plain, _, _ := teeCall(t, up, name, nil)
			q := forward.NewFrameQueue(4096)
			tapped, res, _ := teeCall(t, up, name, q)
			if !bytes.Equal(plain.Body.Bytes(), want) || !bytes.Equal(tapped.Body.Bytes(), want) || res.Finish != model.FinishOK {
				t.Fatalf("client bytes differ from the fixture (finish %s)", res.Finish)
			}
			frames := 0
			for range q.Frames() { // closed by Forward
				frames++
			}
			if frames == 0 || q.Dropped() != 0 {
				t.Fatalf("frames=%d dropped=%d", frames, q.Dropped())
			}
		})
	}
}

func TestTee_StuckConsumerDoesNotDelayRealStream(t *testing.T) {
	const delay, slack = 20 * time.Millisecond, 50 * time.Millisecond
	chunks := testkit.Chunks(streamFixture)
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: delay})
	q := forward.NewFrameQueue(1) // never read: the consumer is stuck
	rec, res, took := teeCall(t, up, streamFixture, q)
	fixtureTime := time.Duration(len(chunks)-1) * delay
	if took > fixtureTime+slack {
		t.Fatalf("stream took %v, want at most %v", took, fixtureTime+slack)
	}
	if res.Finish != model.FinishOK || !bytes.Equal(rec.Body.Bytes(), bytes.Join(chunks, nil)) {
		t.Fatalf("finish=%s, body changed", res.Finish)
	}
	if q.Dropped() == 0 {
		t.Fatal("no frame was dropped although the consumer never read")
	}
}

func TestProxy_StreamLimitsEndWithErrorEvent(t *testing.T) {
	first := testkit.Chunks(streamFixture)[0]
	tests := []struct {
		name   string
		limits tollgate.Limits
	}{
		{"idle stream timeout", tollgate.Limits{IdleStreamTimeout: 150 * time.Millisecond}},
		{"max call duration", tollgate.Limits{MaxCallDuration: 150 * time.Millisecond}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: time.Minute})
			base := newProxyServer(t, tollgate.Options{Limits: tt.limits})
			resp := post(t, base, "/openai/v1/chat/completions", []byte(`{"stream":true}`), via(up))
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || !bytes.HasPrefix(got, first) || !strings.Contains(string(got[len(first):]), `"timeout_error"`) {
				t.Fatalf("body=%q err=%v, want the first chunk then a timeout error event and a clean end", got, err)
			}
		})
	}
}
