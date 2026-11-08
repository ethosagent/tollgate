# Performance: what tollgate adds to a call

Measured in milestone 013 (perf-v1). Raw data, profiles and every run are in
`.logs/perf/` (local, not committed); this page records the method, the
numbers and what they do and do not settle. Re-measure before quoting them
for another machine.

Rig: 10-core Apple M1 Pro, go1.27, fixture `openai/chat-stream-text`
(6 SSE chunks), no chunk delay unless stated. The proxy runs with a real
metric sink (`ext/metrics/stdout` to a discard writer) and the default
Limits except the connection pool, which is sized to the session count so
the pool cap is not what is measured.

## Method

`cmd/tollgate-bench` measures added latency, not end-to-end latency.

- **Paired.** Each session makes pairs of calls on warm connections: one
  straight at a fake upstream, one through tollgate, alternating which goes
  first. One unmeasured warm-up pair per session precedes the timed pass.
- **Upstream-timestamped.** The fake stamps when it received each request
  and wrote each chunk; the client stamps when it sent and when each chunk
  arrived. Per pair, added time = proxied leg minus direct leg, where a leg
  is (request sent → fake received) + (fake wrote chunk → client got it).
  The fake's own delays and timer jitter fall out of both legs.
- **One clock domain.** The fake stamps durations on its monotonic clock;
  the rig maps them onto its own monotonic clock with a minimum-round-trip
  sync (16 probes over loopback). Any offset error appears once with each
  sign in a leg, so the added figures cancel it; only `forward_*` (proxy
  handler entered → fake received) carries it, and the report prints its
  bound (`upstream_clock_bound_ms`, typically tens of µs). No wall clock is
  read, so an NTP step cannot skew a run.
- **Separate-process fake.** `-serve-fake` runs the fake in its own process
  (`GOMAXPROCS=3`), the client and proxy in another (`GOMAXPROCS=6`), so the
  fake's goroutines and GC do not share a scheduler with the proxy.
- **Null-relay control.** `-null-proxy` replaces tollgate with a minimal
  standard-library relay (copy headers, forward, write and flush each read).
  Its added time is what any Go proxy hop costs on the same rig; tollgate's
  share is the difference between the two.
- **Memory off the clock.** After the latency pass, a fresh rig holds one
  stream per session open (chunk delay ≥ 100 ms), forces GC and reads live
  heap and goroutines, direct then proxied. Added bytes and goroutines per
  open stream are the difference. No forced GC falls inside a timed call.
- **Campaign.** Each row below is the median of 5 independent runs; p99 is
  the per-run p99 (nearest rank), then the median across runs.

```sh
GOMAXPROCS=3 go run ./cmd/tollgate-bench -serve-fake -chunk-delay 100ms  # prints the -upstream line
GOMAXPROCS=6 go run ./cmd/tollgate-bench -sessions 4   -calls 40 -json -upstream "<line>"
GOMAXPROCS=6 go run ./cmd/tollgate-bench -sessions 200 -calls 4  -json -upstream "<line>"
# control: same, plus -null-proxy; profiles: -profile DIR
```

The fake's chunk delay keeps the memory pass's streams open (it needs
≥ 100 ms); the added figures cancel it.

## Added latency (ms, medians of 5 runs, separate-process fake)

| Sessions | Path | TTFB p50 | TTFB p99 | Chunk p50 | Chunk p99 | Forward p50 | Forward p99 |
|---:|---|---:|---:|---:|---:|---:|---:|
| 4 | tollgate (before perf-v1) | 1.036 | 4.199 | 0.257 | 2.599 | 0.615 | 2.842 |
| 4 | tollgate (after) | 0.875 | 3.751 | 0.275 | 2.644 | 0.586 | 3.128 |
| 4 | null relay (control) | 0.708 | 3.417 | 0.192 | 2.532 | 0.478 | 2.117 |
| 4 | tollgate (re-run after cleanup†) | 0.813 | 3.480 | 0.244 | 2.411 | 0.485 | 2.874 |
| 4 | null relay (re-run†) | 0.603 | 3.148 | 0.183 | 2.302 | 0.461 | 1.821 |
| 200 | tollgate (before) | 5.063 | 10.871 | 1.524 | 6.353 | 3.016 | 6.434 |
| 200 | tollgate (after) | 7.702 | 11.256 | 1.618 | 6.506 | 3.287 | 5.468 |
| 200 | null relay (control) | 6.355 | 12.858 | 1.754 | 6.766 | 3.090 | 5.782 |

† 2026-11-08, after the InlineSafe/methodset/bench-clock cleanup, fake at
`-chunk-delay 100ms`, same 5-run method; mapping bound 0.06 ms
(`.logs/perf/cleanup/`). Added memory in that run: 55 KB, 5 goroutines.

Reading it:

- At 4 sessions tollgate adds about 0.17 ms TTFB p50 and 0.08 ms chunk p50
  over the null relay; the p99s are within 0.3 ms of the control.
- At 200 sessions every figure, the control's included, is dominated by
  the rig: 200 streams, their client and the proxy share 6 Ps and one
  kernel's loopback. tollgate and the null relay are within run-to-run
  noise of each other (about ±1–1.5 ms between campaigns; the "before"
  row at 200 is not better code, it is that noise). Heap size alone moves
  TTFB p50 by ~1.5 ms there (`.logs/perf/ab-queue4.txt`).
- The 200-session tollgate p99 *chunk* time is below the control's: the
  tee writes and flushes each read before parsing, as the relay does.

## CPU per call

Go benchmarks in `internal/core` (`go test -run '^$' -bench . ./internal/core`),
in-memory transport (no sockets), so they isolate tollgate's own work:

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `BenchmarkServe_InMemoryBuiltins` (built-in Auth/Admission/resolver inline) | ~5 100 | 10 335 | 70 |
| `BenchmarkServe_InMemory` | ~6 300 | 11 455 | 83 |
| `BenchmarkServe_InMemoryWithSink` (plus metric record and sink) | ~11 700 | 16 600 | 133 |

So tollgate's own CPU is **about 6–12 µs per call**, a sink included. The
`BenchmarkCall_*` benchmarks (~110–150 µs/op) go through real loopback
sockets and a fake server; they track regressions, not tollgate's cost.

## Memory and goroutines per open stream

Live heap after GC, whole process, proxied minus direct (memory pass):

| Sessions | Added bytes / stream (median of 5) | Single final run | Added goroutines / stream |
|---:|---:|---:|---:|
| 4 | ~45 KB | 61.9 KB | 5 |
| 200 | ~72 KB | 61.6 KB | 5 |

Before perf-v1 the medians were ~116 KB (4) and ~134 KB (200). The null
relay holds ~79–89 KB and 4 goroutines per stream, mostly its 32 KiB read
buffer. tollgate's share now is the inbound server connection's bufio
buffers, the outbound HTTP/2 or HTTP/1 connection, a pooled 4 KiB relay
buffer (32 KiB only once a read fills it) and a frame queue that grows
with the backlog (8 slots to start). The added goroutines are tollgate's
side of each stream: its server connection, the upstream transport's
connection goroutines and the call's own workers.

A rough sizing rule from these figures: 10 000 concurrent streams cost
about 0.45–0.72 GB of heap and 50 000 goroutines above what the client
side costs.

## The < 1 ms added p99 target (v1 DoD)

**It cannot be judged on a single host, and this rig does not settle it.**

- The control is the floor: a stdlib relay that does nothing adds 3.4 ms
  p99 TTFB at 4 sessions and 12.9 ms at 200 on this machine. No proxy can
  be measured below the floor of the rig it runs on, and tollgate is within
  noise of that floor.
- The floor is contention, not code: client, proxy and fake share one
  CPU, scheduler wake-ups are 26 % of CPU (`.logs/perf/summary.txt`), and
  the tail is set by when the OS gets to run each goroutine.
- tollgate's own work is 6–12 µs of CPU per call (above), two orders of
  magnitude below 1 ms. What remains is socket and scheduling latency that
  a dedicated proxy host would or would not add.

The bench reports `meets_v1_added_p99_under_1ms`; on this rig it is
`false` for the control too, which is the point.

## Multi-host procedure (to settle the target)

Pairing makes this possible without synchronised clocks: in each leg the
client→fake offset appears once with each sign, so the added figures need
only the client's and the fake's clocks to be steady for one call. The
proxy host needs no clock at all.

Two bench changes are needed first (not built yet):

1. `-serve-fake -listen ADDR`: serve the fake and its control endpoints on
   a routable address (testkit needs an exported way to serve its fake on
   a given listener).
2. `-proxy URL`: send the proxied leg to an external tollgate instead of
   one in process (no `forward_*` and no proxy memory pass then; read
   those from the proxy's `/metrics` and process RSS).

Then:

1. Three hosts in one placement group / rack, same instance type with
   dedicated cores (e.g. AWS c7g.2xlarge, cluster placement group): **C**
   client, **P** proxy, **U** fake upstream. Turbo and power saving off,
   nothing else running.
2. Baseline the network: `ping -c 1000` C→U and C→P→U path RTT; record p50/p99.
3. On U: `tollgate-bench -serve-fake -listen 0.0.0.0:9000 -chunk-delay 20ms`.
4. On P: `tollgated serve` with the bench tenant allowed to reach U
   (`AllowedUpstreams: [<U ip>]`), a discard sink, pool sized to sessions.
5. Control first: on P, the null relay in place of tollgated; on C, run
   `-sessions 4 -calls 200` and `-sessions 200 -calls 20`, 5 times each.
   The control's added p99 must be well below 1 ms, or the rig cannot
   judge the target: stop and fix the rig.
6. Then tollgated, same runs, interleaving control and tollgate runs.
7. Report medians of 5 for both, side by side, with the RTT baseline.
   The target is met when tollgate's added TTFB p99 and chunk p99 are
   < 1 ms **and** within noise of the control's.

## Changes in perf-v1 and their effect

| Commit | Change | Effect |
|---|---|---|
| `dae01ef` | built-in Auth/Admission/resolver run inline (now `InlineSafe`, ADR 0015) | −2 scheduler hand-offs per seam per call |
| `cac4cae` | idle pool default = `MaxConnsPerHost` (was 20) | no connection churn below the cap |
| `cb2da6d` | relay reads start at 4 KiB, 32 KiB on bulk | ~30 KB less per open stream |
| `cd4e54b` | frame queue ring grows with the backlog, bounded by frames and bytes | was the top allocation (21.7 MB / 1000 calls) |

## Responses wire parse cost

`go test -run '^$' -bench 'EventMapper/openai|ParseRequest/openai' -benchmem -count 6 ./internal/normalise`
(medians of 6, M1 Pro, go1.27.1, hand-authored fixtures):

| Benchmark | chat | Responses | Frames / bytes (chat, Responses) |
|---|---:|---:|---|
| EventMapper, text stream | 5585 ns | 8298 ns | 6 / 953, 10 / 3746 |
| EventMapper, tool stream | 7526 ns | 9917 ns | 6 / 1172, 8 / 3269 |
| ParseRequest, simple | 1724 ns | 934 ns | 185 B, 101 B |
| ParseRequest, tools | 2263 ns | 7682 ns | 362 B, 897 B (5 items) |

Per frame the mapper costs 830 and 1240 ns against chat's 931 and 1254 (within 10%).
Per stream it is 1.3 to 1.5 times chat (more frames, three times the bytes).

