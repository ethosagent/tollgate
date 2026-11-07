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
parsed. Parsed events go to a bounded queue (`Limits.EventBuffer`; see the
amendment) consumed in another goroutine; on overflow the event is dropped
and counted. Only the
terminal event may be held, and only when a Finalizer is registered.

## Consequences

- OnEvent hooks observe; they cannot modify the stream. Stream rewriting needs
  a Finalizer or a dedicated future seam (new ADR).
- Under overload, hooks may miss events; they must tolerate gaps.
- Enforced by tests in `internal/forward` (the review lens `STR-*` until then).
- Amended 2026-11-08 (perf-v1): the "bounded channel" is now
  `forward.FrameQueue`, a mutex-guarded ring bounded twice: `EventBuffer`
  frames and `EventBuffer` × 4 KiB bytes (never below `MaxFrameBytes`). It
  grows with the backlog, not the cap. Overflow still drops and counts; the
  next delivered frame carries the gap and the queue's end carries the
  drops after the last frame, so consumers learn of gaps. The tee never
  waits on it longer than one push or pop.
