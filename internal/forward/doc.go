// Package forward moves bytes between the client and the upstream: the
// single upstream target check (Target, ADR 0007), the body cap, the pooled
// upstream transport, header filtering (strip tollgate's own and hop-by-hop
// headers, pass credentials through), and Forward, which makes the upstream
// call and relays the response byte for byte, flushing every read. The
// client-first SSE tee builds on Relay.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package forward
