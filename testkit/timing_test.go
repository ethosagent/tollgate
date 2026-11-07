package testkit

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func TestFakeUpstream_StampsArrivalAndEveryChunkWrite(t *testing.T) {
	const fx, delay = "openai/chat-stream-text", 3 * time.Millisecond
	f := NewFakeUpstream(t, Options{ChunkDelay: delay})
	before := time.Now()
	resp := send(t, f, http.MethodPost, PathFor(fx), fx, `{}`)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	r := f.Requests()[0]
	if r.Received.Before(before) || r.Received.After(time.Now()) {
		t.Fatalf("Received = %v, not within the call", r.Received)
	}
	if len(r.ChunksWritten) != len(Chunks(fx)) {
		t.Fatalf("ChunksWritten has %d stamps, want one per chunk (%d)", len(r.ChunksWritten), len(Chunks(fx)))
	}
	prev := r.Received
	for i, w := range r.ChunksWritten {
		if w.Before(prev) || (i > 0 && w.Sub(prev) < delay) {
			t.Fatalf("chunk %d written at +%v after the previous stamp, want in order and >= %v apart", i, w.Sub(prev), delay)
		}
		prev = w
	}
}

func TestFakeUpstream_RequestsAreCopies(t *testing.T) {
	f := NewFakeUpstream(t, Options{})
	resp := send(t, f, http.MethodPost, PathFor("openai/chat"), "openai/chat", `{}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	got := f.Requests()
	got[0].ChunksWritten[0] = time.Time{}
	if f.Requests()[0].ChunksWritten[0].IsZero() {
		t.Fatal("changing a returned Request changed the fake's record")
	}
}
