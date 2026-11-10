# 0019. Cache-write tokens on Usage and a Dispatched flag on Metric

- **Status:** Proposed
- **Date:** 2026-11-10
- **Source:** Meterwise verifier review of plan 05 (N3 cache writes, N8 Accept-Encoding) and its need to tell refusals from calls

## Context

Three gaps showed up when a host costs and audits calls from tollgate's `Metric`.

1. Anthropic's `input_tokens` excludes prompt-cache writes
   (`cache_creation_input_tokens`), which are billed at a premium. tollgate
   parsed only `cache_read_input_tokens`, so a host under-reported spend for
   agents that use prompt caching.
2. `Metric.Finish` has `FinishRejected`, but it covers more than "refused
   before the upstream": a Before hook may also reject. A host that must not
   bill or alert on a refusal could not tell it from a dispatched call.
3. A verifier saw `Accept-Encoding: gzip` upstream and attributed it to tollgate.

## Decision

1. `model.Usage` gains `CacheWriteInputTokens int64` (`json:",omitempty"`).
   The Anthropic wire fills it from `usage.cache_creation_input_tokens` in
   `message_start`, `message_delta` and a buffered body, with the same
   present-zero overlay as the other fields. The OpenAI wires leave it 0.
   Recovery sums include it. The Prometheus registry, stdout and Postgres
   sinks do not export it yet; a host reads it from `Metric.Usage`.
2. `model.Metric` gains `Dispatched bool`: true once the proxy attempted the
   upstream request (transport errors, timeouts and client cancels mid-flight
   included), false for auth, admission, resolver, target-check and
   body-read refusals and for a Before hook's short-circuit Response.
   Alternative rejected: a new `Finish` value, because adding or splitting a
   value breaks consumers that switch on `Finish` and loses the reason
   (`FinishUpstreamError` vs refused) the other way round.
3. No transport change for Accept-Encoding. `forward.NewTransport` sets
   `DisableCompression`, so the default never adds the header and never
   decodes a body; `TestProxy_AcceptEncodingIsTheClients` pins it end to end.
   The header a probe sees comes from Go's own `http.Client`, or from a host
   `Options.Transport` built without `DisableCompression`; `Options.Transport`
   now documents that.

## Consequences

- Additive only: new struct fields, zero values mean "unchanged behaviour".
  Unkeyed `Usage{...}` literals in host code would break; keyed ones do not.
- Hosts price `CacheWriteInputTokens` separately from `InputTokens` and
  `CachedInputTokens`; they must not add it to `InputTokens`.
- Release: minor (v0.2.0), together with ADR 0018.
- Enforced by `TestAnthropicUsage_CacheWriteTokens`,
  `TestProxy_OneMetricPerCall`, `TestProxy_MetricDispatchedSignalsAnUpstreamAttempt`.
