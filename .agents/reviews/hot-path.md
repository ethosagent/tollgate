# Hot path (`HOT-*`)

The proxy's own work must stay well under 1 ms per call; upstreams take seconds.

## Already covered by CI

- Lint bans `http.DefaultClient` and friends (no timeouts) and requires a context on outbound requests (`noctx`).

## Checks

**HOT-1 No unbounded blocking call per request.** Any call to a network,
database or sidecar from the request path has a deadline and a defined
fall-back (skip the extension, pass through).

**HOT-2 Once, not per request.** Regex compilation, JSON schema/config parsing,
transport and client construction happen at `New`, not per call.

**HOT-3 Stream, do not buffer.** Streaming bodies are never read fully. Request
bodies are read once, capped by `Limits.MaxRequestBody`, and shared.

**HOT-4 Parse once.** The request body is normalised once; hooks share the
`Call`. No second `json.Unmarshal` of the same body in the core.

**HOT-5 Allocation awareness per chunk.** Per-chunk code avoids allocating
when it can reuse a buffer; benchmarks cover the tee and the event parser.

**HOT-6 No global locks per call.** Per-call work does not take a process-wide
mutex; use sharding, `sync.Map` or per-tenant structures.

**HOT-7 Benchmarks with the change.** A change to per-call code adds or
updates a `Benchmark*` and states the before/after in the PR.
