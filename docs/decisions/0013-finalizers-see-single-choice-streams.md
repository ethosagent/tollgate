# 0013. Finalizers see single-choice streams only

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review of milestone 009 (TEST-3)

## Context

The barrier holds a stream's terminal region: from the first frame that
carries a finish reason to the end. With several choices (OpenAI `n > 1`)
the first finish reason belongs to one choice while the others still
stream, so their content would wait behind Decide, a Replace would drop it,
and the recovery splice (which drops a recovery's role-only opening delta
and continues `choices[0]`) would merge two multi-choice streams wrongly.
Options: (a) open the region only once every choice has finished, which
needs the choice count and a JSON parse of every frame on the byte path;
(b) detect multi-choice frames by bytes, which is fragile (`"index"` keys
also appear in tool calls); (c) decide at the request, whose body the core
already decodes once.

## Decision

(c). The OpenAI request parse records `n` in a new neutral field,
`Call.Choices` (0 means the provider default, one). A call with
`Choices > 1` is relayed without the barrier: unchanged, and no Finalizer
is asked. The `Finalizer` interface documents the precondition.

## Consequences

- Multi-choice streams cannot be finalized; a finalizer needing them is a
  future, explicit design (a new ADR).
- `Call.Choices` is also available to hooks and strategies. Adding a field
  breaks no caller.
- Enforced by `TestFinalizer_SeveralChoicesAreNotHeld` and
  `TestParseRequest_Choices`.
