# Streaming (`STR-*`)

Guards non-negotiables 1, 2 and 6 in `CLAUDE.md`. Read ADR 0004 first.

## Already covered by CI

- `go test -race` (data races, not ordering or latency).

## Checks

**STR-1 Write, flush, then observe.** Each upstream chunk reaches the client's
`ResponseWriter` and is flushed before any parsing, hook or metric work on it.

**STR-2 No hook on the client write path.** OnEvent runs in another goroutine
fed by a bounded channel. Nothing on the write path waits for a hook.

**STR-3 Bounded everything.** Every channel, buffer and queue between the
stream and background work has a size from `Limits`; overflow drops and
increments a counter.

**STR-4 Every extension call is guarded.** `recover()` plus a context deadline
from `Limits`. A recovered panic is logged with tenant and call id; the call
proceeds as if the extension were absent.

**STR-5 Cancellation flows both ways.** Client disconnect cancels the upstream
request (shared context). Upstream drop mid-stream writes a provider-shaped
error event, then closes. Both record the right `Finish`.

**STR-6 Terminal hold only with finalizers.** The terminal event is held only
when at least one Finalizer is registered. With none, there is zero added work.

**STR-7 Recovery is budgeted.** Every Finalizer recovery call counts against
the per-finalizer budget and the per-call ceiling.

**STR-8 stream:false is buffered with a cap.** Non-streaming bodies are read
up to a size cap with a defined error above it, before OnResponse.

**STR-9 Goroutines end.** Every goroutine started per call ends when the call
ends; test with a leak check or by counting.
