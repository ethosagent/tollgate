package metrics

import (
	"strconv"
	"sync"

	"github.com/ethosagent/tollgate/internal/model"
)

// MaxProviders bounds the distinct provider label values a Registry keeps;
// further providers are counted as "other" (PRIV-8: no unbounded labels).
const MaxProviders = 32

// latencyBounds are the upper bounds, in seconds, of the call-duration
// histogram buckets (an implicit +Inf bucket follows).
var latencyBounds = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900}

// CallCount is the number of calls with one (provider, finish, status class).
type CallCount struct {
	Provider    string
	Finish      model.Finish
	StatusClass string // "2xx".."5xx", "other" or "none"
	N           int64
}

// Histogram is a cumulative histogram: Counts[i] is the number of
// observations <= Bounds[i]; Count is every observation (the +Inf bucket).
type Histogram struct {
	Bounds []float64
	Counts []int64
	Sum    float64
	Count  int64
}

// Snapshot is a consistent copy of a Registry.
type Snapshot struct {
	Calls   []CallCount
	Latency Histogram
	Tokens  model.Usage
}

type callKey struct {
	provider string
	finish   model.Finish
	class    string
}

// Registry aggregates every Metric into bounded-cardinality counters and one
// latency histogram, for /statusz and /metrics. It is safe for concurrent
// use; Observe takes one short lock and never blocks on I/O.
type Registry struct {
	mu        sync.Mutex
	calls     map[callKey]int64
	providers map[string]struct{}
	buckets   []int64 // per-bucket (not cumulative) counts, last is +Inf
	sum       float64
	count     int64
	tokens    model.Usage
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{calls: map[callKey]int64{}, providers: map[string]struct{}{}, buckets: make([]int64, len(latencyBounds)+1)}
}

// Observe records one call.
func (r *Registry) Observe(m model.Metric) {
	secs := m.Duration.Seconds()
	i := 0
	for i < len(latencyBounds) && secs > latencyBounds[i] {
		i++
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[callKey{r.provider(m.Provider), finishLabel(m.Finish), statusClass(m.Status)}]++
	r.buckets[i]++
	r.sum += secs
	r.count++
	r.tokens.InputTokens += m.Usage.InputTokens
	r.tokens.OutputTokens += m.Usage.OutputTokens
	r.tokens.CachedInputTokens += m.Usage.CachedInputTokens
	r.tokens.ReasoningTokens += m.Usage.ReasoningTokens
}

// provider returns the bounded label value for name; r.mu is held.
func (r *Registry) provider(name string) string {
	if name == "" {
		return "none"
	}
	if _, ok := r.providers[name]; ok {
		return name
	}
	if len(r.providers) >= MaxProviders {
		return "other"
	}
	r.providers[name] = struct{}{}
	return name
}

func finishLabel(f model.Finish) model.Finish {
	switch f {
	case model.FinishOK, model.FinishUpstreamError, model.FinishClientCancel, model.FinishRejected, model.FinishTimeout:
		return f
	}
	return "unknown"
}

func statusClass(status int) string {
	switch {
	case status == 0:
		return "none"
	case status < 100 || status > 599:
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}

// Snapshot returns a copy of the current counters.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{Tokens: r.tokens, Latency: Histogram{Bounds: latencyBounds, Counts: make([]int64, len(latencyBounds)), Sum: r.sum, Count: r.count}}
	for k, n := range r.calls {
		s.Calls = append(s.Calls, CallCount{Provider: k.provider, Finish: k.finish, StatusClass: k.class, N: n})
	}
	var cum int64
	for i := range latencyBounds {
		cum += r.buckets[i]
		s.Latency.Counts[i] = cum
	}
	return s
}
