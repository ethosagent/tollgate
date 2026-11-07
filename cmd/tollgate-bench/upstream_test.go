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

// TestServeFake_RecordsCrossTheProcessBoundary runs the -serve-fake body in
// process: it announces its addresses, serves the fixture, reports each
// call's stamps as wall-clock records, and ends with its context.
func TestServeFake_RecordsCrossTheProcessBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveFake(ctx, time.Millisecond, outW) }()
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("address line %q: %v", line, err)
	}
	u, err := dialRemoteUpstream(line)
	if err != nil {
		t.Fatal(err)
	}
	defer u.stop()
	const fx = "openai/chat-stream-text"
	_, body := testkit.RequestFor(fx)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.url()+testkit.PathFor(fx), bytes.NewReader(body))
	req.Header.Set(testkit.FixtureHeader, fx)
	req.Header.Set(callHeader, "c1")
	before := time.Now()
	resp, err := u.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	recs, err := u.records(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := recs["c1"]
	if !ok || len(r.ChunksWritten) != len(testkit.Chunks(fx)) || r.Received.Before(before.Add(-time.Second)) ||
		r.ChunksWritten[0].Before(r.Received) {
		t.Fatalf("records = %+v, want c1 with every chunk stamped after it was received", recs)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveFake after its context ended: %v", err)
	}
}

func TestDialRemoteUpstream_RejectsABadLine(t *testing.T) {
	for _, spec := range []string{"", "FAKE http://127.0.0.1:1", "http://a b c", "FAKE nohost http://127.0.0.1:2/records"} {
		if _, err := dialRemoteUpstream(spec); err == nil {
			t.Errorf("%q was accepted", spec)
		}
	}
	if _, err := dialRemoteUpstream("http://127.0.0.1:1 http://127.0.0.1:2/records"); err != nil {
		t.Errorf("a line without the FAKE prefix: %v", err)
	}
}

func TestLocalUpstream_RecordsByCallID(t *testing.T) {
	u := newLocalUpstream(0)
	defer u.stop()
	recs, err := u.records(context.Background())
	if err != nil || len(recs) != 0 || u.url() == "" {
		t.Fatalf("fresh fake: %v %v %q", recs, err, u.url())
	}
}
