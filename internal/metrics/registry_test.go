package metrics

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

func countOf(s Snapshot, provider string, finish model.Finish, class string) int64 {
	for _, c := range s.Calls {
		if c.Provider == provider && c.Finish == finish && c.StatusClass == class {
			return c.N
		}
	}
	return 0
}

func TestRegistry_CountsCallsByProviderFinishAndStatusClass(t *testing.T) {
	tests := []struct {
		name     string
		m        model.Metric
		provider string
		finish   model.Finish
		class    string
	}{
		{"ok", model.Metric{Provider: "openai", Finish: model.FinishOK, Status: 200}, "openai", model.FinishOK, "2xx"},
		{"upstream error", model.Metric{Provider: "openai", Finish: model.FinishUpstreamError, Status: 503}, "openai", model.FinishUpstreamError, "5xx"},
		{"rejected before resolve has no provider", model.Metric{Finish: model.FinishRejected, Status: 401}, "none", model.FinishRejected, "4xx"},
		{"no status yet", model.Metric{Provider: "anthropic", Finish: model.FinishClientCancel}, "anthropic", model.FinishClientCancel, "none"},
		{"unknown finish is bounded", model.Metric{Provider: "openai", Finish: "weird", Status: 200}, "openai", "unknown", "2xx"},
		{"out of range status is bounded", model.Metric{Provider: "openai", Finish: model.FinishOK, Status: 999}, "openai", model.FinishOK, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRegistry()
			r.Observe(tt.m)
			if got := countOf(r.Snapshot(), tt.provider, tt.finish, tt.class); got != 1 {
				t.Fatalf("calls{%s,%s,%s} = %d, want 1 (snapshot %+v)", tt.provider, tt.finish, tt.class, got, r.Snapshot().Calls)
			}
		})
	}
}

func TestRegistry_ProviderLabelValuesAreBounded(t *testing.T) {
	r := NewRegistry()
	for i := range 1000 {
		r.Observe(model.Metric{Provider: fmt.Sprintf("p%d", i), Finish: model.FinishOK, Status: 200})
	}
	seen := map[string]bool{}
	var total int64
	for _, c := range r.Snapshot().Calls {
		seen[c.Provider] = true
		total += c.N
	}
	if len(seen) > MaxProviders+1 || total != 1000 || !seen["other"] {
		t.Fatalf("%d provider values, %d calls, other=%v; want <= %d values, all 1000 calls, an \"other\" bucket", len(seen), total, seen["other"], MaxProviders+1)
	}
}

func TestRegistry_LatencyHistogramIsCumulative(t *testing.T) {
	r := NewRegistry()
	for _, d := range []time.Duration{3 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, time.Hour} {
		r.Observe(model.Metric{Provider: "openai", Finish: model.FinishOK, Status: 200, Duration: d})
	}
	h := r.Snapshot().Latency
	if h.Count != 4 || len(h.Counts) != len(h.Bounds) {
		t.Fatalf("histogram = %+v", h)
	}
	for i := 1; i < len(h.Counts); i++ {
		if h.Counts[i] < h.Counts[i-1] {
			t.Fatalf("bucket counts not cumulative: %v", h.Counts)
		}
	}
	for i, b := range h.Bounds {
		if b == 0.1 && h.Counts[i] != 3 {
			t.Fatalf("le=0.1 count = %d, want 3", h.Counts[i])
		}
	}
	if h.Sum < 3600 {
		t.Fatalf("Sum = %v, want >= 3600 (one hour observed)", h.Sum)
	}
}

func TestRegistry_SumsTokens(t *testing.T) {
	r := NewRegistry()
	r.Observe(model.Metric{Usage: model.Usage{InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2, ReasoningTokens: 1}})
	r.Observe(model.Metric{Usage: model.Usage{InputTokens: 1}})
	if u := r.Snapshot().Tokens; u != (model.Usage{InputTokens: 11, OutputTokens: 5, CachedInputTokens: 2, ReasoningTokens: 1}) {
		t.Fatalf("tokens = %+v", u)
	}
}

func TestRegistry_ConcurrentObserveIsExact(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 250 {
				r.Observe(model.Metric{Provider: "openai", Finish: model.FinishOK, Status: 200, Duration: time.Millisecond})
			}
		}()
	}
	wg.Wait()
	s := r.Snapshot()
	if countOf(s, "openai", model.FinishOK, "2xx") != 2000 || s.Latency.Count != 2000 {
		t.Fatalf("snapshot = %+v, want 2000 calls", s)
	}
}
