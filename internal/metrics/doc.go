// Package metrics owns the bounded metric queue between the request path and
// the MetricSink: one Metric per completed call is enqueued without blocking,
// a single goroutine batches to the sink, and overflow or sink failure drops
// and counts. The proxy never slows down for its own bookkeeping.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package metrics
