// Package forward moves bytes between the client and the upstream: the
// single upstream target check (Target, ADR 0007), the body cap, the pooled
// upstream transport, header filtering (strip tollgate's own and hop-by-hop
// headers, pass credentials through), and Forward, which makes the upstream
// call and relays the response byte for byte, flushing every read. The
// client-first SSE tee (Tee) writes and flushes each read to the client before
// a copy is scanned into SSE frames for a FrameQueue bounded in frames and
// bytes, which reports drops as gaps and the call's ending (ADR 0004). The
// max-call limit is a context deadline and first-byte/idle one re-armed
// timer; a stream that times out or drops ends with a provider-shaped error
// event.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package forward
