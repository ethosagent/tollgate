package metrics

import (
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// MaxProviders bounds the distinct provider label values a Registry keeps;
// further providers are counted as "other" (PRIV-8: no unbounded labels).
const MaxProviders = 32

// latencyBounds are the upper bounds, in seconds, of the call-duration
// histogram buckets (an implicit +Inf bucket follows).
var latencyBounds = [...]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 900}

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

// Snapshot is a copy of a Registry's counters.
type Snapshot struct {
	Calls   []CallCount
	Latency Histogram
	Tokens  model.Usage
}

// finishes are the Finish label values, in counter order; anything else is
// counted as "unknown" (the last).
var finishes = [...]model.Finish{model.FinishOK, model.FinishUpstreamError, model.FinishClientCancel,
	model.FinishRejected, model.FinishTimeout, "unknown"}

// classes are the status-class label values, in counter order.
var classes = [...]string{"none", "1xx", "2xx", "3xx", "4xx", "5xx", "other"}

// stripes is how many copies of every counter a Registry keeps. Observe
// adds to the copy its call ID hashes to and Snapshot sums them, so cores that
// finish calls at the same time rarely touch the same cache line.
const stripes = 8

// counts are one provider's call counters, by finish and status class.
type counts [len(finishes)][len(classes)]atomic.Int64

// series are a provider's counters, one copy per stripe.
type series [stripes]counts

// stripe is one copy of the per-call counters other than call counts,
// padded so two stripes never share a cache line.
type stripe struct {
	buckets  [len(latencyBounds) + 1]atomic.Int64 // per-bucket (not cumulative) counts, last is +Inf
	sumNanos atomic.Int64                         // total duration
	tokens   [4]atomic.Int64                      // input, output, cached input, reasoning
	_        [64]byte
}

// Registry aggregates every Metric into bounded-cardinality counters and one
// latency histogram, for /statusz and /metrics. It is safe for concurrent
// use. Observe takes no lock: every counter is a striped atomic, and the
// provider table is copy-on-write, so only a provider's first call (at most
// MaxProviders times) takes the mutex (HOT-6).
type Registry struct {
	mu        sync.Mutex                         // serialises adding a provider
	providers atomic.Pointer[map[string]*series] // read-only once published
	seed      maphash.Seed
	stripes   [stripes]stripe
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	r := &Registry{seed: maphash.MakeSeed()}
	r.providers.Store(&map[string]*series{})
	return r
}

// Observe records one call.
func (r *Registry) Observe(m model.Metric) {
	secs := m.Duration.Seconds()
	i := 0
	for i < len(latencyBounds) && secs > latencyBounds[i] {
		i++
	}
	k := maphash.String(r.seed, m.CallID) % stripes // call IDs differ per call: spreads cores, no shared state
	r.series(m.Provider)[k][finishIndex(m.Finish)][classIndex(m.Status)].Add(1)
	st := &r.stripes[k]
	st.buckets[i].Add(1)
	st.sumNanos.Add(int64(m.Duration))
	for j, n := range [...]int64{m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CachedInputTokens, m.Usage.ReasoningTokens} {
		if n != 0 {
			st.tokens[j].Add(n)
		}
	}
}

// series returns the counters of the bounded label value for name: "none"
// when empty, "other" once MaxProviders distinct names are known.
func (r *Registry) series(name string) *series {
	if name == "" {
		name = "none"
	}
	if s, ok := (*r.providers.Load())[name]; ok {
		return s
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := *r.providers.Load()
	if s, ok := cur[name]; ok {
		return s
	}
	if named := len(cur) - present(cur, "none") - present(cur, "other"); named >= MaxProviders && name != "none" {
		if s, ok := cur["other"]; ok {
			return s
		}
		name = "other"
	}
	next := make(map[string]*series, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	s := &series{}
	next[name] = s
	r.providers.Store(&next)
	return s
}

// present is 1 when name is in m, else 0.
func present(m map[string]*series, name string) int {
	if _, ok := m[name]; ok {
		return 1
	}
	return 0
}

func finishIndex(f model.Finish) int {
	for i, v := range finishes[:len(finishes)-1] {
		if v == f {
			return i
		}
	}
	return len(finishes) - 1
}

func classIndex(status int) int {
	switch {
	case status == 0:
		return 0
	case status < 100 || status > 599:
		return len(classes) - 1
	}
	return status / 100
}

// Snapshot returns a copy of the current counters. Each counter is exact;
// a call observed while Snapshot runs may appear in some counters and not
// yet in others. Latency.Count is the sum of the buckets, so the histogram
// is always self-consistent.
func (r *Registry) Snapshot() Snapshot {
	s := Snapshot{Calls: r.callCounts(), Latency: Histogram{Bounds: latencyBounds[:], Counts: make([]int64, len(latencyBounds))}}
	var buckets [len(latencyBounds) + 1]int64
	var sum time.Duration
	var tok [4]int64
	for k := range r.stripes {
		st := &r.stripes[k]
		for i := range buckets {
			buckets[i] += st.buckets[i].Load()
		}
		sum += time.Duration(st.sumNanos.Load())
		for j := range tok {
			tok[j] += st.tokens[j].Load()
		}
	}
	var cum int64
	for i, b := range buckets {
		if cum += b; i < len(latencyBounds) {
			s.Latency.Counts[i] = cum
		}
	}
	s.Latency.Count, s.Latency.Sum = cum, sum.Seconds()
	s.Tokens = model.Usage{InputTokens: tok[0], OutputTokens: tok[1], CachedInputTokens: tok[2], ReasoningTokens: tok[3]}
	return s
}

// callCounts sums every provider's stripes into one count per series.
func (r *Registry) callCounts() []CallCount {
	var out []CallCount
	for name, ser := range *r.providers.Load() {
		for fi := range finishes {
			for ci := range classes {
				var n int64
				for k := range ser {
					n += ser[k][fi][ci].Load()
				}
				if n > 0 {
					out = append(out, CallCount{Provider: name, Finish: finishes[fi], StatusClass: classes[ci], N: n})
				}
			}
		}
	}
	return out
}
