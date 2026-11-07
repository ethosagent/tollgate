package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"time"
)

// v1AddedP99 is the v1 target for the latency tollgate adds (plan/000).
const v1AddedP99 = time.Millisecond

// latency is p50 and p99, in milliseconds, of the two times measured per call.
type latency struct {
	TTFBP50  float64 `json:"ttfb_p50_ms"`
	TTFBP99  float64 `json:"ttfb_p99_ms"`
	TotalP50 float64 `json:"total_p50_ms"`
	TotalP99 float64 `json:"total_p99_ms"`
}

// overhead is what the proxy adds, from paired calls and the fake upstream's
// own timestamps, so the fake's chunk delays are not part of it.
type overhead struct {
	TTFBAddedP50  float64 `json:"ttfb_added_p50_ms"` // per pair: proxied minus direct time to first byte
	TTFBAddedP99  float64 `json:"ttfb_added_p99_ms"`
	ChunkAddedP50 float64 `json:"chunk_added_p50_ms"` // per chunk: proxied minus direct upstream-write -> client
	ChunkAddedP99 float64 `json:"chunk_added_p99_ms"`
	ForwardP50    float64 `json:"forward_p50_ms"` // proxy got the request -> upstream got it
	ForwardP99    float64 `json:"forward_p99_ms"`
	Pairs         int     `json:"pairs"`
	Chunks        int     `json:"chunks"`
	MeetsV1       bool    `json:"meets_v1_added_p99_under_1ms"`
}

// memory is live bytes per open stream for the whole process (client, fake
// upstream and, proxied, tollgate), so Added is tollgate's share.
type memory struct {
	HoldChunkDelay       string  `json:"hold_chunk_delay"`
	DirectStreams        int64   `json:"direct_streams"`
	ProxyStreams         int64   `json:"proxy_streams"`
	DirectBytesPerStream float64 `json:"direct_bytes_per_stream"`
	ProxyBytesPerStream  float64 `json:"proxy_bytes_per_stream"`
	AddedBytesPerStream  float64 `json:"added_bytes_per_stream"`
	// Goroutines per open stream, whole process, read with the bytes.
	DirectGoroutinesPerStream float64 `json:"direct_goroutines_per_stream"`
	ProxyGoroutinesPerStream  float64 `json:"proxy_goroutines_per_stream"`
	AddedGoroutinesPerStream  float64 `json:"added_goroutines_per_stream"`
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
	CallsPerSession int          `json:"calls_per_session"` // measured pairs per session
	TotalCalls      int          `json:"total_calls"`       // measured, per path
	WarmupCalls     int          `json:"warmup_calls"`      // unmeasured, per path: one per session
	ChunkDelay      string       `json:"chunk_delay"`
	Direct          latency      `json:"direct"` // end to end, includes the fake's delays
	Proxy           latency      `json:"proxy"`
	Overhead        overhead     `json:"overhead"`
	PeakStreams     int64        `json:"peak_concurrent_streams"`
	Memory          memory       `json:"memory"`
	Statusz         dropCounters `json:"statusz"`
	UpstreamProc    bool         `json:"upstream_proc"` // the fake ran in its own process
	NullProxy       bool         `json:"null_proxy"`    // control run: tollgate replaced by a minimal relay
	GOMAXPROCS      int          `json:"gomaxprocs"`    // of the client+proxy process
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

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Microsecond)) / 1000 }

func latencyOf(ttfb, total []time.Duration) latency {
	return latency{
		TTFBP50: ms(percentile(ttfb, 0.5)), TTFBP99: ms(percentile(ttfb, 0.99)),
		TotalP50: ms(percentile(total, 0.5)), TotalP99: ms(percentile(total, 0.99)),
	}
}

func overheadOf(t timings) overhead {
	ttfb99, chunk99 := percentile(t.ttfbAdded, 0.99), percentile(t.chunkAdded, 0.99)
	return overhead{
		TTFBAddedP50: ms(percentile(t.ttfbAdded, 0.5)), TTFBAddedP99: ms(ttfb99),
		ChunkAddedP50: ms(percentile(t.chunkAdded, 0.5)), ChunkAddedP99: ms(chunk99),
		ForwardP50: ms(percentile(t.forward, 0.5)), ForwardP99: ms(percentile(t.forward, 0.99)),
		Pairs: len(t.ttfbAdded), Chunks: len(t.chunkAdded),
		MeetsV1: len(t.ttfbAdded) > 0 && ttfb99 < v1AddedP99 && chunk99 < v1AddedP99,
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
	pr("end to end (ms)    %10s %10s\n", "direct", "proxy")
	row := func(name string, d, p float64) { pr("%-18s %10.3f %10.3f\n", name, d, p) }
	row("ttfb p50", r.Direct.TTFBP50, r.Proxy.TTFBP50)
	row("ttfb p99", r.Direct.TTFBP99, r.Proxy.TTFBP99)
	row("total p50", r.Direct.TotalP50, r.Proxy.TotalP50)
	row("total p99", r.Direct.TotalP99, r.Proxy.TotalP99)
	o := r.Overhead
	pr("\nadded by the proxy (ms, paired, upstream-stamped)  p50 / p99\n")
	pr("  time to first byte   %8.3f / %8.3f   (%d pairs)\n", o.TTFBAddedP50, o.TTFBAddedP99, o.Pairs)
	pr("  per chunk            %8.3f / %8.3f   (%d chunks)\n", o.ChunkAddedP50, o.ChunkAddedP99, o.Chunks)
	pr("  request forward      %8.3f / %8.3f\n", o.ForwardP50, o.ForwardP99)
	pr("  v1 target (added p99 < 1 ms): %t\n", o.MeetsV1)
	m := r.Memory
	pr("\nbytes per open stream (hold %s): direct %.0f (%d streams), proxy %.0f (%d streams), added %.0f\n",
		m.HoldChunkDelay, m.DirectBytesPerStream, m.DirectStreams, m.ProxyBytesPerStream, m.ProxyStreams, m.AddedBytesPerStream)
	pr("goroutines per open stream: direct %.2f, proxy %.2f, added %.2f\n",
		m.DirectGoroutinesPerStream, m.ProxyGoroutinesPerStream, m.AddedGoroutinesPerStream)
	pr("peak concurrent streams (latency pass): %d\n", r.PeakStreams)
	pr("dropped: metrics %d, events %d, sink errors %d, closed sessions %d (calls seen %d, metrics written %d)\n",
		r.Statusz.MetricsDropped, r.Statusz.EventsDropped, r.Statusz.SinkErrors, r.Statusz.SessionsClosedDropped, r.Statusz.Calls, r.Statusz.MetricsWritten)
	return err
}
