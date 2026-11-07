---
name: add-provider
description: Add an upstream wire format (request normaliser, stream event mapper, error shape, fixtures). Use when tollgate must understand a new provider API beyond pass-through.
---

# Add a provider wire format

Rules: `CLAUDE.md`. Lenses: `STR-*`, `HOT-*`, `API-*`, `TEST-*`.

A provider that only needs pass-through needs **no code**: the resolver maps
its path prefix to `WirePassthrough`. Do this skill only to normalise calls.

## Steps

1. **Record fixtures first.** Capture real request bodies and SSE streams
   (scrub keys and content) into `testkit/testdata/<wire>/`: a plain text
   stream, a tool-call stream, a reasoning stream, usage, an error mid-stream,
   and a `stream:false` body.
2. **Add the wire constant** in `internal/model/types.go` (`Wire<Name>`) and its
   alias in `model.go` (`TestAliases_CoverModel` enforces this). This is public
   API: note it in the PR (`API-*`).
3. **Test first** in `internal/normalise/<wire>_test.go`: each fixture → the
   expected `Call` fields and `[]Event` sequence; malformed input → error, not
   panic.
4. **Implement** `internal/normalise/<wire>.go`: request → `Call` (model,
   stream, messages, tool calls, reasoning), chunk → `Event`s, and a
   provider-shaped error writer. Parse each body once (HOT-4). Per-chunk code
   reuses buffers (HOT-5).
5. **Register** the path prefix in `internal/resolve`.
6. **Benchmark** the event mapper on the longest fixture (`HOT-7`).
7. Add a fuzz target for the chunk parser (`Fuzz<Wire>Events`) seeded from the
   fixtures.
8. Run `scripts/run-checks.sh`. Tier: `critical` (normalise is on the stream path).
