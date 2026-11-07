package main

import (
	"context"
	"testing"
	"time"
)

// TestAtPeak_ReturnsWhenStreamsEndedUnseen: when every stream opened and
// ended between two looks (a fake with no chunk delay), atPeak must report
// zero open streams instead of waiting for a peak that never comes.
func TestAtPeak_ReturnsWhenStreamsEndedUnseen(t *testing.T) {
	l := newLoader("openai/chat-stream-text", 3)
	defer l.close()
	l.peak.Store(3) // all three opened and closed: open is back to 0
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan int64, 1)
	go func() { _, _, n := l.atPeak(ctx, 3); done <- n }()
	select {
	case n := <-done:
		if n != 0 || ctx.Err() != nil {
			t.Fatalf("streams = %d (ctx %v), want 0 before the deadline", n, ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("atPeak still waiting after every stream ended")
	}
}
