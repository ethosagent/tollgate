package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/testkit"
)

func TestRun_JSONReport(t *testing.T) {
	var out bytes.Buffer
	args := []string{"-sessions", "4", "-calls", "3", "-fixture", "openai/chat-stream-text", "-chunk-delay", "2ms", "-json"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("output is not one JSON document: %v\n%s", err, out.String())
	}
	if r.TotalCalls != 12 {
		t.Fatalf("calls = %d, want 12 measured pairs", r.TotalCalls)
	}
	chunks := len(testkit.Chunks("openai/chat-stream-text"))
	if o := r.Overhead; o.Pairs != 12 || o.Chunks != 12*chunks || o.ForwardP50 <= 0 || o.ForwardP99 < o.ForwardP50 ||
		o.TTFBAddedP99 < o.TTFBAddedP50 || o.ChunkAddedP99 < o.ChunkAddedP50 {
		t.Fatalf("overhead = %+v, want 12 pairs, %d chunks, ordered percentiles and a positive forward time", o, 12*chunks)
	}
	if m := r.Memory; m.DirectStreams != 4 || m.ProxyStreams != 4 || m.ProxyBytesPerStream <= 0 {
		t.Fatalf("memory = %+v, want all 4 streams held open on both paths", m)
	}
	if r.Direct.TTFBP50 <= 0 || r.Proxy.TotalP99 < r.Proxy.TotalP50 || r.Proxy.TTFBP99 < r.Proxy.TTFBP50 {
		t.Fatalf("latencies are not ordered: direct %+v proxy %+v", r.Direct, r.Proxy)
	}
	if r.PeakStreams < 1 || r.PeakStreams > 4 {
		t.Fatalf("peak streams = %d, want 1..4 (one open stream per session)", r.PeakStreams)
	}
	if r.WarmupCalls != 4 || r.Statusz.Calls != int64(r.TotalCalls+r.WarmupCalls) {
		t.Fatalf("warm-up calls = %d, statusz saw %d calls, want 4 and 16", r.WarmupCalls, r.Statusz.Calls)
	}
	if r.Statusz.MetricsDropped != 0 || r.Statusz.SinkErrors != 0 || r.Statusz.EventsDropped != 0 {
		t.Fatalf("unexpected drops at this load: %+v", r.Statusz)
	}
}

func TestRun_TableReport(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-sessions", "2", "-calls", "2"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"direct", "proxy", "added by the proxy", "ttfb p50", "per chunk", "v1 target", "bytes per open stream", "dropped"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRun_RejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no sessions", []string{"-sessions", "0"}, "-sessions"},
		{"no calls", []string{"-calls", "0"}, "-calls"},
		{"unknown fixture", []string{"-fixture", "openai/nope"}, "unknown fixture"},
		{"a request input is no response", []string{"-fixture", "openai/request-chat-stream"}, "unknown fixture"},
		{"an error fixture never answers 200", []string{"-fixture", "openai/error-429"}, "unknown fixture"},
		{"negative delay", []string{"-chunk-delay", "-1s"}, "-chunk-delay"},
		{"unknown flag", []string{"-wat"}, "wat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), tt.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestRun_StopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := run(ctx, []string{"-sessions", "2", "-calls", "1000000", "-chunk-delay", "5ms"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a canceled run reported success")
	}
}

func TestPercentile(t *testing.T) {
	ms := func(n ...int) []time.Duration {
		out := make([]time.Duration, len(n))
		for i, v := range n {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name string
		in   []time.Duration
		p    float64
		want time.Duration
	}{
		{"empty is zero", nil, 0.5, 0},
		{"single value", ms(7), 0.99, 7 * time.Millisecond},
		{"median of an odd list", ms(5, 1, 3), 0.5, 3 * time.Millisecond},
		{"p99 of 100 is the 99th", ms(seq(100)...), 0.99, 99 * time.Millisecond},
		{"p50 of 100 is the 50th", ms(seq(100)...), 0.5, 50 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.in, tt.p); got != tt.want {
				t.Fatalf("percentile = %v, want %v", got, tt.want)
			}
		})
	}
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}
