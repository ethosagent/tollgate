# 0015. InlineSafe extensions run without a deadline goroutine

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** perf-v1 (plan/013), simplify-013 altitude 1

## Context

Non-negotiable 2 says every extension call has `recover()` and a deadline.
The deadline is `chain.Within`: a goroutine, a timer and two scheduler
hand-offs per call, per seam. Auth, Admission and the ProviderResolver run
on every request, and profiles of the in-memory pipeline put about 60 % of
CPU in scheduler wake-ups from those per-call goroutines. Commit `dae01ef`
ran the three built-ins (`NoAuth`, `Unlimited`, `*resolve.Resolver`) inline
by matching their exact types. That worked, but treated identical contracts
unequally: a host's in-memory key lookup paid for a deadline it could never
hit while `NoAuth` did not, and the type switch would silently stop matching
if a built-in were ever wrapped.

Options: (a) keep the exact-type switch; (b) an unexported marker method,
so only tollgate's own types qualify; (c) an exported optional marker
interface that built-ins and hosts may declare; (d) drop deadlines for every
extension (breaks non-negotiable 2).

## Decision

(c). `InlineSafe` (`internal/model`, aliased in the root) is a marker with one
method, `InlineSafe()`. New type-asserts it once per seam for `Auth`,
`Admission` and `Providers`; a marked extension runs on the handler's
goroutine under `chain.Guard` (panics still recovered and mapped as before),
with no deadline. The marker counts only when the extension's own type (or
its pointer) declares the method; a type that inherits it by embedding keeps
its deadline (`internal/methodset.Declares`).

The trust tier's rules, for anyone declaring it: the seam method never does
I/O, never waits on a channel, lock, timer or the context, and its CPU is
bounded by configuration (a map lookup, a fixed list). Anything that can
wait (a database, a remote IdP, a concurrency queue such as
`ext/admission/concurrency`) must not declare it.

## Consequences

- Non-negotiable 2 now reads: every extension call has `recover()`; every
  extension that has not declared `InlineSafe` also has a deadline.
- Built-ins declaring it: `NoAuth`, `Unlimited`, `*resolve.Resolver`,
  `ext/auth/statickeys.Auth`. Hooks, Sessions, Finalizers and sinks are not
  covered; their deadlines are unchanged.
- A wrongly marked extension that blocks holds its request until it returns
  or the client goes away; it cannot block other requests. The cost of the
  mistake falls on the host that made the claim.
- Adding the marker later to an existing extension is a compatible change;
  removing it is too (the extension just gets its deadline back).
- Enforced by tests: `TestInlineSeams_OnlyDeclaredInlineSafeRunsInline`,
  `TestGuarded_InlineRecoversAndHostKeepsItsDeadline`,
  `TestProxy_HostAdmissionStillAnsweredAtItsDeadline`,
  `internal/methodset` `TestDeclares`.
