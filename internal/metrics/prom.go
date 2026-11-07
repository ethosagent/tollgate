package metrics

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// HookCounts are the hook machinery's give-up counters (never silent, STR-8).
type HookCounts struct {
	UnbufferedResponses int64 // stream:false responses that bypassed OnResponse
	DroppedEvents       int64 // SSE frames that never reached OnEvent
}

// Report is everything /metrics exposes. Queue is nil when no metric sink
// is configured; the sink families are then omitted.
type Report struct {
	Snapshot Snapshot
	InFlight int64
	Queue    *Stats
	Hooks    HookCounts
}

// WriteProm writes r in the Prometheus text exposition format (0.0.4) using
// only the standard library. Label values come from bounded sets (provider
// capped at MaxProviders, finish, status class, token kind, skip reason), so
// series count cannot grow with traffic.
func WriteProm(w io.Writer, r Report) error {
	bw := bufio.NewWriter(w)
	p := &promWriter{w: bw}
	p.family("tollgate_calls_total", "counter", "Completed calls by provider, finish and status class.")
	calls := slices.Clone(r.Snapshot.Calls)
	slices.SortFunc(calls, func(a, b CallCount) int {
		return cmp.Or(strings.Compare(a.Provider, b.Provider), strings.Compare(string(a.Finish), string(b.Finish)),
			strings.Compare(a.StatusClass, b.StatusClass))
	})
	for _, c := range calls {
		p.sample("tollgate_calls_total", num(c.N), "provider", c.Provider, "finish", string(c.Finish), "status_class", c.StatusClass)
	}
	p.histogram("tollgate_call_duration_seconds", "Call duration in seconds, handler start to finish.", r.Snapshot.Latency)
	p.family("tollgate_tokens_total", "counter", "Tokens reported by providers, by kind.")
	t := r.Snapshot.Tokens
	p.sample("tollgate_tokens_total", num(t.InputTokens), "kind", "input")
	p.sample("tollgate_tokens_total", num(t.OutputTokens), "kind", "output")
	p.sample("tollgate_tokens_total", num(t.CachedInputTokens), "kind", "cached_input")
	p.sample("tollgate_tokens_total", num(t.ReasoningTokens), "kind", "reasoning")
	p.family("tollgate_calls_in_flight", "gauge", "Calls currently being handled.")
	p.sample("tollgate_calls_in_flight", num(r.InFlight))
	p.family("tollgate_hook_skips_total", "counter", "Hook work given up on, by reason.")
	p.sample("tollgate_hook_skips_total", num(r.Hooks.UnbufferedResponses), "reason", "unbuffered_response")
	p.sample("tollgate_hook_skips_total", num(r.Hooks.DroppedEvents), "reason", "dropped_event")
	if r.Queue != nil {
		p.queue(*r.Queue)
	}
	if p.err != nil {
		return p.err
	}
	return bw.Flush()
}

func (p *promWriter) queue(q Stats) {
	p.single("tollgate_metrics_queue_depth", "gauge", "Metrics waiting for the sink.", int64(q.Depth))
	p.single("tollgate_metrics_dropped_total", "counter", "Metrics dropped: queue full, closed, or in a failed batch.", q.Dropped)
	p.single("tollgate_metrics_sink_errors_total", "counter", "Failed sink writes.", q.SinkErrors)
	p.single("tollgate_metrics_written_total", "counter", "Metrics the sink accepted.", q.Written)
	ready := int64(0)
	if q.Ready {
		ready = 1
	}
	p.single("tollgate_metrics_sink_ready", "gauge", "1 while the sink is healthy, 0 after consecutive failures.", ready)
}

type promWriter struct {
	w   *bufio.Writer
	err error
}

func (p *promWriter) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

func (p *promWriter) family(name, typ, help string) {
	p.printf("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *promWriter) single(name, typ, help string, v int64) {
	p.family(name, typ, help)
	p.sample(name, num(v))
}

// sample writes one line; kv are label name/value pairs.
func (p *promWriter) sample(name, value string, kv ...string) {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `%s="%s"`, kv[i], escapeLabel(kv[i+1]))
	}
	if b.Len() == 0 {
		p.printf("%s %s\n", name, value)
		return
	}
	p.printf("%s{%s} %s\n", name, b.String(), value)
}

func (p *promWriter) histogram(name, help string, h Histogram) {
	p.family(name, "histogram", help)
	for i, b := range h.Bounds {
		p.sample(name+"_bucket", num(h.Counts[i]), "le", strconv.FormatFloat(b, 'g', -1, 64))
	}
	p.sample(name+"_bucket", num(h.Count), "le", "+Inf")
	p.sample(name+"_sum", strconv.FormatFloat(h.Sum, 'g', -1, 64))
	p.sample(name+"_count", num(h.Count))
}

func num(n int64) string { return strconv.FormatInt(n, 10) }

// escapeLabel escapes a label value per the text format: backslash, double
// quote and newline.
func escapeLabel(s string) string { return labelEscaper.Replace(s) }

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
