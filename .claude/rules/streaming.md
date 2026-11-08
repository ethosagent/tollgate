---
paths:
  - "internal/forward/**"
  - "internal/chain/**"
  - "internal/normalise/**"
  - "internal/core/**"
---

# Streaming path rules

You are on the client's byte path. Full checklist: `.agents/reviews/streaming.md` (STR-*), ADR 0004.

- Write and flush each upstream chunk to the client **before** parsing it or handing it to a hook.
- OnEvent hooks read from a bounded channel in another goroutine. Never call a hook on the write path.
- Every channel/buffer has a size from `Limits`; on full, drop and count. Never block the client.
- Every extension call: `recover()` + a context deadline from `Limits`; on failure log metadata and skip.
- Client disconnect cancels the upstream request through the shared context. Record the `Finish` value.
- Every goroutine started for a call ends with the call. Test it.
- Never log bodies, reasoning, tool arguments or credential headers (PRIV-1, PRIV-2).
- Per-chunk code reuses buffers; add or update a `Benchmark*` for the changed path (HOT-5, HOT-7).
