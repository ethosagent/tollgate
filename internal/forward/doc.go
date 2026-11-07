// Package forward moves bytes between the client and the upstream: the
// pooled upstream transport, header filtering (strip tollgate's own and
// hop-by-hop headers, pass credentials through), and the client-first SSE
// tee that writes and flushes each chunk before handing a copy to the event
// parser.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package forward
