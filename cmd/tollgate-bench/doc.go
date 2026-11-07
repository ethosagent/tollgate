// Command tollgate-bench measures what tollgate adds to a streamed call.
//
// It starts a testkit fake upstream and an in-process tollgate in front of
// it, then runs -sessions concurrent sessions. After one unmeasured warm-up
// pair per session, each session makes -calls pairs of calls on its warm
// connections: one straight at the fake and one through the proxy,
// alternating which goes first. The fake stamps when it received each
// request and wrote each chunk; the client stamps when each chunk arrived;
// all on one monotonic clock. Per pair, the proxy's added time is the
// proxied path's (request sent -> fake received, plus fake wrote -> client
// got) minus the direct path's, so the fake's -chunk-delay sleeps and their
// timer jitter fall out of the figure. It reports p50 and p99 of that added
// time to first byte and per chunk, the time from the proxy's handler to the
// fake, end-to-end times for context, and the drop counters from /statusz.
//
// Memory is measured afterwards on a fresh rig, never inside a timed call:
// one stream per session is held open (chunk delay at least 100ms) and live
// memory is read after garbage collection, direct and proxied; the
// difference per stream is tollgate's share.
//
//	go run ./cmd/tollgate-bench -sessions 200 -calls 4 -fixture openai/chat-stream-text -chunk-delay 20ms -json
//
// The memory pass also counts goroutines per open stream.
//
// By default everything runs in one process over loopback, so absolute
// numbers depend on the machine; compare runs on the same machine.
// -upstream-proc moves the fake upstream into a child process (pinned with
// -upstream-gomaxprocs; pin this process with the GOMAXPROCS env var), so the
// fake's goroutines and GC no longer share a scheduler with the proxy; its
// stamps then cross over as wall-clock times. Dev-only profiles of the timed
// pass: -cpuprofile, -memprofile (plus FILE.before for pprof -diff_base),
// -blockprofile, -mutexprofile; -heapprofile is the live heap while the
// memory pass holds every proxied stream open. -null-proxy is the control:
// a minimal stdlib relay replaces tollgate, so its figures are what any
// proxy hop costs on the rig. Dependency law
// (internal/archtest, layer "tool", rule D5): the public API, testkit
// included, never internal/. It is a developer tool and is not shipped
// (ADR 0014).
package main
