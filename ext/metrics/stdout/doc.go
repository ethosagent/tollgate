// Package stdout is a tollgate.MetricSink that writes one JSON object per
// Metric, one per line (JSON Lines), to an io.Writer such as os.Stdout. It is
// the simplest sink: pipe it to a log shipper or a file.
//
//	p, err := tollgate.New(tollgate.Options{Metrics: stdout.New(os.Stdout)})
//	if err != nil { /* bad options */ }
//	defer p.Close(ctx) // flushes queued metrics to the sink
//
// Fields (metadata only; never prompts, completions or credentials):
// call_id, tenant, session, provider, model, stream, status, finish,
// input_tokens, output_tokens, cached_input_tokens, reasoning_tokens, start
// (RFC 3339, UTC), ttfb_ms, duration_ms.
//
// Write encodes a whole batch first and writes it with a single call to the
// writer, under a mutex, so concurrent batches never interleave lines. It
// returns the writer's error, which the core counts as dropped metrics; it
// never retries. A Sink is safe for concurrent use; the writer is owned by
// the host.
package stdout
