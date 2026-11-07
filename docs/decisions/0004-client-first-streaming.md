# 0004. Client-first streaming tee

- **Status:** Accepted
- **Date:** 2026-11-07
- **Source:** plan, "Key design choices: client-first streaming"

## Context

Hundreds of long-lived SSE streams pass through one process. Hooks (monitors,
tracing) need parsed events, but a slow or panicking hook must not delay the
client. Options: (a) parse, run hooks, then write; (b) write first, then parse
a copy and hand events to hooks asynchronously.

## Decision

(b). Each upstream chunk is written to the client and flushed before a copy is
parsed. Parsed events go to a bounded channel (`Limits.EventBuffer`) consumed
in another goroutine; on overflow the event is dropped and counted. Only the
terminal event may be held, and only when a Finalizer is registered.

## Consequences

- OnEvent hooks observe; they cannot modify the stream. Stream rewriting needs
  a Finalizer or a dedicated future seam (new ADR).
- Under overload, hooks may miss events; they must tolerate gaps.
- Enforced by tests in `internal/forward` (the review lens `STR-*` until then).
