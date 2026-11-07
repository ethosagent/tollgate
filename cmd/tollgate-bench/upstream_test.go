package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

// TestServeFake_StampsLandOnTheClientsClock runs the -serve-fake body in
// process: it announces its addresses, serves the fixture, and its records,
// mapped through the clock sync, fall between the client's own stamps of
// the same call (within the reported bound), in order.
func TestServeFake_StampsLandOnTheClientsClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveFake(ctx, time.Millisecond, outW) }()
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("address line %q: %v", line, err)
	}
	u, err := dialUpstream(line)
	if err != nil {
		t.Fatal(err)
	}
	defer u.stop()
	before, after := call(ctx, t, u, "c1")
	recs, bound, err := u.records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := recs["c1"]
	if !ok || len(r.ChunksWritten) != len(testkit.Chunks(fx)) {
		t.Fatalf("records = %+v, want c1 with every chunk", recs)
	}
	last := r.ChunksWritten[len(r.ChunksWritten)-1]
	if bound <= 0 || r.Received.Before(before.Add(-bound)) || last.After(after.Add(bound)) || r.ChunksWritten[0].Before(r.Received) {
		t.Fatalf("received %v, last chunk %v, bound %v: want within [%v, %v]", r.Received, last, bound, before, after)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveFake after its context ended: %v", err)
	}
}

const fx = "openai/chat-stream-text"

// call makes one fixture call straight at the fake and returns when it was
// sent and when its body was fully read.
func call(ctx context.Context, t *testing.T, u *fakeClient, id string) (sent, read time.Time) {
	t.Helper()
	_, body := testkit.RequestFor(fx)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.url()+testkit.PathFor(fx), bytes.NewReader(body))
	req.Header.Set(testkit.FixtureHeader, fx)
	req.Header.Set(callHeader, id)
	sent = time.Now()
	resp, err := u.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return sent, time.Now()
}

func TestDialUpstream_RejectsABadLine(t *testing.T) {
	for _, spec := range []string{"", "FAKE http://127.0.0.1:1", "http://a b c", "FAKE nohost 2", "FAKE http://127.0.0.1:1 0", "FAKE http://127.0.0.1:1 70000", "FAKE http://127.0.0.1:1 http://x"} {
		if _, err := dialUpstream(spec); err == nil {
			t.Errorf("%q was accepted", spec)
		}
	}
	if _, err := dialUpstream("http://127.0.0.1:1 2"); err != nil {
		t.Errorf("a line without the FAKE prefix: %v", err)
	}
}

func TestLocalUpstream_SameControlPathAsAnotherProcess(t *testing.T) {
	u, err := startLocalUpstream(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer u.stop()
	recs, bound, err := u.records(context.Background())
	if err != nil || len(recs) != 0 || u.url() == "" || bound <= 0 {
		t.Fatalf("fresh fake: %v %v %v %q", recs, bound, err, u.url())
	}
	before, after := call(context.Background(), t, u, "l1")
	recs, bound, err = u.records(context.Background())
	if r := recs["l1"]; err != nil || r.Received.Before(before.Add(-bound)) || r.Received.After(after.Add(bound)) {
		t.Fatalf("in-process record %+v (err %v) outside [%v, %v] ± %v", r, err, before, after, bound)
	}
}
