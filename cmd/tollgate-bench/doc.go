// Command tollgate-bench measures what tollgate adds to a streamed call.
//
// It starts a testkit fake upstream and an in-process tollgate in front of it,
// then runs -sessions concurrent sessions of -calls sequential calls each,
// first straight at the fake and then through the proxy. It reports p50 and
// p99 time to first byte and total time for both, the difference (the added
// latency), memory per open stream, and the drop counters from /statusz.
//
//	go run ./cmd/tollgate-bench -sessions 200 -calls 4 -fixture openai/chat-stream-text -json
//
// Everything runs in one process over loopback, so absolute numbers depend on
// the machine; compare runs on the same machine. Dependency law
// (internal/archtest, rule D5): the public API only, never internal/.
package main
