package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"time"
)

// latency is p50 and p99, in milliseconds, of the two times measured per call.
type latency struct {
	TTFBP50  float64 `json:"ttfb_p50_ms"`
	TTFBP99  float64 `json:"ttfb_p99_ms"`
	TotalP50 float64 `json:"total_p50_ms"`
	TotalP99 float64 `json:"total_p99_ms"`
}

// memory is bytes per open stream for the whole process (client, fake
// upstream and, for the proxied phase, tollgate), so Added is the proxy's share.
type memory struct {
	DirectBytesPerStream float64 `json:"direct_bytes_per_stream"`
	ProxyBytesPerStream  float64 `json:"proxy_bytes_per_stream"`
	AddedBytesPerStream  float64 `json:"added_bytes_per_stream"`
}

// dropCounters come from the proxy's /statusz after the run.
type dropCounters struct {
	Calls                 int64 `json:"calls"`
	MetricsWritten        int64 `json:"metrics_written"`
	MetricsDropped        int64 `json:"metrics_dropped"`
	SinkErrors            int64 `json:"sink_errors"`
	EventsDropped         int64 `json:"events_dropped"`
	SessionsClosedDropped int64 `json:"sessions_closed_dropped"`
}

// report is the whole result; -json prints it as is.
type report struct {
	Fixture         string       `json:"fixture"`
	Sessions        int          `json:"sessions"`
	CallsPerSession int          `json:"calls_per_session"`
	TotalCalls      int          `json:"total_calls"`  // measured, per phase
	WarmupCalls     int          `json:"warmup_calls"` // unmeasured, per phase: one per session
	ChunkDelay      string       `json:"chunk_delay"`
	Direct          latency      `json:"direct"`
	Proxy           latency      `json:"proxy"`
	Added           latency      `json:"added"`
	PeakStreams     int64        `json:"peak_concurrent_streams"`
	Memory          memory       `json:"memory"`
	Errors          int          `json:"errors"`
	Statusz         dropCounters `json:"statusz"`
}

// percentile is the nearest-rank percentile (p in 0..1) of ds; 0 for none.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	rank := int(math.Ceil(p*float64(len(s)))) - 1
	return s[min(max(rank, 0), len(s)-1)]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func latencyOf(p phase) latency {
	return latency{
		TTFBP50: ms(percentile(p.ttfb, 0.5)), TTFBP99: ms(percentile(p.ttfb, 0.99)),
		TotalP50: ms(percentile(p.total, 0.5)), TotalP99: ms(percentile(p.total, 0.99)),
	}
}

func buildReport(cfg config, direct, proxied phase, drops dropCounters) report {
	d, p := latencyOf(direct), latencyOf(proxied)
	return report{
		Fixture: cfg.fixture, Sessions: cfg.sessions, CallsPerSession: cfg.calls,
		TotalCalls: cfg.sessions * cfg.calls, WarmupCalls: proxied.warmup, ChunkDelay: cfg.chunkDelay.String(),
		Direct: d, Proxy: p,
		Added:       latency{p.TTFBP50 - d.TTFBP50, p.TTFBP99 - d.TTFBP99, p.TotalP50 - d.TotalP50, p.TotalP99 - d.TotalP99},
		PeakStreams: proxied.peakStreams,
		Memory: memory{DirectBytesPerStream: direct.bytesPerStream, ProxyBytesPerStream: proxied.bytesPerStream,
			AddedBytesPerStream: proxied.bytesPerStream - direct.bytesPerStream},
		Errors:  direct.errors + proxied.errors,
		Statusz: drops,
	}
}

// writeTable prints r for a person.
func writeTable(w io.Writer, r report) error {
	var err error
	pr := func(format string, a ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, a...)
		}
	}
	pr("tollgate-bench  fixture=%s  sessions=%d  calls/session=%d  chunk-delay=%s\n\n", r.Fixture, r.Sessions, r.CallsPerSession, r.ChunkDelay)
	pr("%-18s %10s %10s %10s\n", "", "direct", "proxy", "added")
	row := func(name string, d, p, a float64) { pr("%-18s %10.3f %10.3f %+10.3f\n", name, d, p, a) }
	row("ttfb p50 (ms)", r.Direct.TTFBP50, r.Proxy.TTFBP50, r.Added.TTFBP50)
	row("ttfb p99 (ms)", r.Direct.TTFBP99, r.Proxy.TTFBP99, r.Added.TTFBP99)
	row("total p50 (ms)", r.Direct.TotalP50, r.Proxy.TotalP50, r.Added.TotalP50)
	row("total p99 (ms)", r.Direct.TotalP99, r.Proxy.TotalP99, r.Added.TotalP99)
	pr("\npeak concurrent streams: %d\n", r.PeakStreams)
	pr("bytes per open stream: direct %.0f, proxy %.0f, added %.0f\n", r.Memory.DirectBytesPerStream, r.Memory.ProxyBytesPerStream, r.Memory.AddedBytesPerStream)
	pr("dropped: metrics %d, events %d, sink errors %d, closed sessions %d (calls seen %d, metrics written %d)\n",
		r.Statusz.MetricsDropped, r.Statusz.EventsDropped, r.Statusz.SinkErrors, r.Statusz.SessionsClosedDropped, r.Statusz.Calls, r.Statusz.MetricsWritten)
	pr("errors: %d\n", r.Errors)
	return err
}
