package metrics

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// sample is one parsed exposition line.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// exposition is a parsed Prometheus text exposition (version 0.0.4).
type exposition struct {
	types   map[string]string
	help    map[string]bool
	samples []sample
}

var (
	sampleRe = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})? (\S+)$`)
	labelRe  = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)="([^"\\]*)"$`)
)

// parseExposition is a strict parser for what Write emits: every sample line
// must match the grammar and follow a TYPE line for its family.
func parseExposition(t *testing.T, text string) exposition {
	t.Helper()
	e := exposition{types: map[string]string{}, help: map[string]bool{}}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			t.Fatalf("blank line in exposition")
		case strings.HasPrefix(line, "# HELP "):
			e.help[strings.Fields(line)[2]] = true
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			if len(f) != 4 || !strings.Contains("counter gauge histogram", f[3]) {
				t.Fatalf("bad TYPE line %q", line)
			}
			e.types[f[2]] = f[3]
		case strings.HasPrefix(line, "#"):
			t.Fatalf("unknown comment %q", line)
		default:
			e.samples = append(e.samples, parseSample(t, line))
		}
	}
	for _, s := range e.samples {
		fam := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s.name, "_bucket"), "_sum"), "_count")
		if e.types[s.name] == "" && e.types[fam] != "histogram" {
			t.Fatalf("sample %q has no TYPE line", s.name)
		}
		if !e.help[fam] && !e.help[s.name] {
			t.Fatalf("sample %q has no HELP line", s.name)
		}
	}
	return e
}

func parseSample(t *testing.T, line string) sample {
	t.Helper()
	m := sampleRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("not a sample line: %q", line)
	}
	v, err := strconv.ParseFloat(m[4], 64)
	if err != nil {
		t.Fatalf("bad value in %q: %v", line, err)
	}
	s := sample{name: m[1], labels: map[string]string{}, value: v}
	if m[3] != "" {
		for _, kv := range strings.Split(m[3], ",") {
			lm := labelRe.FindStringSubmatch(kv)
			if lm == nil {
				t.Fatalf("bad label %q in %q", kv, line)
			}
			s.labels[lm[1]] = lm[2]
		}
	}
	return s
}

func (e exposition) value(name string, kv ...string) (float64, bool) {
next:
	for _, s := range e.samples {
		if s.name != name {
			continue
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if s.labels[kv[i]] != kv[i+1] {
				continue next
			}
		}
		return s.value, true
	}
	return 0, false
}

func testReport() Report {
	r := NewRegistry()
	for _, m := range []model.Metric{
		{Provider: "openai", Finish: model.FinishOK, Status: 200, Duration: 40 * time.Millisecond, Usage: model.Usage{InputTokens: 7, OutputTokens: 3}},
		{Provider: "openai", Finish: model.FinishOK, Status: 200, Duration: 2 * time.Second},
		{Provider: "anthropic", Finish: model.FinishUpstreamError, Status: 529, Duration: time.Second},
	} {
		r.Observe(m)
	}
	return Report{Snapshot: r.Snapshot(), InFlight: 2, Queue: &Stats{Depth: 5, Dropped: 9, SinkErrors: 4, Written: 100, Ready: false},
		Hooks: HookCounts{UnbufferedResponses: 6, DroppedEvents: 8}}
}

func TestWriteProm_ParsesAndCarriesEveryFamily(t *testing.T) {
	var b strings.Builder
	if err := WriteProm(&b, testReport()); err != nil {
		t.Fatal(err)
	}
	e := parseExposition(t, b.String())
	for _, c := range []struct {
		name string
		kv   []string
		want float64
	}{
		{"tollgate_calls_total", []string{"provider", "openai", "finish", "ok", "status_class", "2xx"}, 2},
		{"tollgate_calls_total", []string{"provider", "anthropic", "finish", "upstream_error", "status_class", "5xx"}, 1},
		{"tollgate_calls_in_flight", nil, 2},
		{"tollgate_metrics_queue_depth", nil, 5},
		{"tollgate_metrics_dropped_total", nil, 9},
		{"tollgate_metrics_sink_errors_total", nil, 4},
		{"tollgate_metrics_written_total", nil, 100},
		{"tollgate_metrics_sink_ready", nil, 0},
		{"tollgate_tokens_total", []string{"kind", "input"}, 7},
		{"tollgate_tokens_total", []string{"kind", "output"}, 3},
		{"tollgate_hook_skips_total", []string{"reason", "unbuffered_response"}, 6},
		{"tollgate_hook_skips_total", []string{"reason", "dropped_event"}, 8},
		{"tollgate_call_duration_seconds_count", nil, 3},
	} {
		if got, ok := e.value(c.name, c.kv...); !ok || got != c.want {
			t.Errorf("%s%v = %v (found %v), want %v", c.name, c.kv, got, ok, c.want)
		}
	}
	if e.types["tollgate_call_duration_seconds"] != "histogram" || e.types["tollgate_calls_total"] != "counter" || e.types["tollgate_calls_in_flight"] != "gauge" {
		t.Fatalf("types = %v", e.types)
	}
}

func TestWriteProm_HistogramBucketsAreMonotonicAndEndAtInf(t *testing.T) {
	var b strings.Builder
	if err := WriteProm(&b, testReport()); err != nil {
		t.Fatal(err)
	}
	e := parseExposition(t, b.String())
	var prev float64 = -1
	var inf float64
	n := 0
	for _, s := range e.samples {
		if s.name != "tollgate_call_duration_seconds_bucket" {
			continue
		}
		n++
		if s.value < prev {
			t.Fatalf("bucket le=%s = %v, below the previous %v", s.labels["le"], s.value, prev)
		}
		prev = s.value
		if s.labels["le"] == "+Inf" {
			inf = s.value
		}
	}
	count, _ := e.value("tollgate_call_duration_seconds_count")
	if n != len(latencyBounds)+1 || inf != count || inf != 3 {
		t.Fatalf("%d buckets, +Inf=%v, count=%v; want %d buckets and +Inf == count == 3", n, inf, count, len(latencyBounds)+1)
	}
	if got, _ := e.value("tollgate_call_duration_seconds_bucket", "le", "0.05"); got != 1 {
		t.Fatalf("le=0.05 = %v, want 1 (the 40ms call)", got)
	}
}

func TestWriteProm_WithoutQueueOmitsSinkFamilies(t *testing.T) {
	var b strings.Builder
	if err := WriteProm(&b, Report{Snapshot: NewRegistry().Snapshot()}); err != nil {
		t.Fatal(err)
	}
	e := parseExposition(t, b.String())
	if _, ok := e.value("tollgate_metrics_dropped_total"); ok {
		t.Fatal("sink families present with no sink configured")
	}
	if _, ok := e.value("tollgate_calls_in_flight"); !ok {
		t.Fatal("in-flight gauge missing")
	}
}

func TestWriteProm_LabelValuesStayBoundedUnderManyProviders(t *testing.T) {
	r := NewRegistry()
	for i := range 500 {
		r.Observe(model.Metric{Provider: fmt.Sprintf("tenant-%d", i), Finish: model.FinishOK, Status: 200})
	}
	var b strings.Builder
	if err := WriteProm(&b, Report{Snapshot: r.Snapshot()}); err != nil {
		t.Fatal(err)
	}
	e := parseExposition(t, b.String())
	providers := map[string]bool{}
	for _, s := range e.samples {
		if p, ok := s.labels["provider"]; ok {
			providers[p] = true
		}
	}
	if len(providers) > MaxProviders+1 {
		t.Fatalf("%d provider label values, want <= %d", len(providers), MaxProviders+1)
	}
}

func TestEscapeLabel(t *testing.T) {
	if got := escapeLabel("a\"b\\c\nd"); got != `a\"b\\c\nd` {
		t.Fatalf("escapeLabel = %q", got)
	}
}
