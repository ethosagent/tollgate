package stdout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
)

func TestSink_WritesOneJSONLinePerMetric(t *testing.T) {
	var buf bytes.Buffer
	s := New(&buf)
	start := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	batch := []tollgate.Metric{
		{CallID: "c1", TenantID: "acme", SessionID: "s1", Provider: "openai", Model: "gpt-x", Stream: true, Status: 200,
			Finish: tollgate.FinishOK, Usage: tollgate.Usage{InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2, ReasoningTokens: 1},
			Start: start, TTFB: 120 * time.Millisecond, Duration: 2500 * time.Millisecond},
		{CallID: "c2", Finish: tollgate.FinishRejected, Status: 401, Start: start},
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2: %q", len(lines), buf.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line 0 is not JSON: %v", err)
	}
	want := map[string]any{"call_id": "c1", "tenant": "acme", "session": "s1", "provider": "openai", "model": "gpt-x",
		"stream": true, "status": float64(200), "finish": "ok", "input_tokens": float64(10), "output_tokens": float64(5),
		"cached_input_tokens": float64(2), "reasoning_tokens": float64(1), "ttfb_ms": float64(120), "duration_ms": float64(2500),
		"start": "2026-11-08T10:00:00Z"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("line has %d fields, want exactly %d (no extra data): %v", len(got), len(want), got)
	}
}

func TestSink_EmptyBatchWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	if err := New(&buf).Write(context.Background(), nil); err != nil || buf.Len() != 0 {
		t.Fatalf("err = %v, wrote %q", err, buf.String())
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestSink_ReturnsTheWriterError(t *testing.T) {
	err := New(failWriter{}).Write(context.Background(), []tollgate.Metric{{CallID: "x"}})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the writer's error", err)
	}
}

func TestSink_HonoursAnEndedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	if err := New(&buf).Write(ctx, []tollgate.Metric{{CallID: "x"}}); !errors.Is(err, context.Canceled) || buf.Len() != 0 {
		t.Fatalf("err = %v, wrote %q; want context.Canceled and no output", err, buf.String())
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func TestSink_ConcurrentWritesNeverInterleaveLines(t *testing.T) {
	var lb lockedBuf
	s := New(&lb)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = s.Write(context.Background(), []tollgate.Metric{{CallID: "c", Provider: "openai"}, {CallID: "d"}})
			}
		}()
	}
	wg.Wait()
	for i, line := range strings.Split(strings.TrimSuffix(lb.b.String(), "\n"), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not valid JSON (interleaved write?): %q", i, line)
		}
	}
}
