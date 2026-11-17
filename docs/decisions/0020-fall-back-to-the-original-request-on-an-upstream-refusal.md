# 0020. Fall back to the original request on an upstream refusal

- **Status:** Proposed
- **Date:** 2026-11-17
- **Source:** plan 001 (a host router's rewritten body refused with 400 by the upstream)

## Context

A Before hook may rewrite the body: a router swaps in a cheaper model, a trimmer shrinks
tool output. The upstream may refuse that rewritten request with an error the client's own
request would not get (for example 400 "function tools need reasoning off"). The client
then gets the error because of an extension, against axiom 4 (a wrong extension degrades
to pass-through). Options:

- (a) Model knowledge in tollgate (which model takes which tools). Rejected: the core
  stays small and dumb (axiom 2) and would chase every provider's rules.
- (b) A new seam or hook result ("retry with this body"). Rejected: new public interface,
  and the hook already decided once; a second guess needs I/O on the request path.
- (c) Config only: on a host-listed status, resend the client's **original** body, as
  received before any hook changed it. tollgate needs no reason; the original is by
  definition what the client asked for. Cost: one more upstream round trip on a refusal.

Name: `Options.FallbackStatuses []int`, not `Limits.FallbackStatuses`. `Limits` holds
bounds whose zero means "default"; a status list is behaviour whose nil means "off", and a
slice would make `Limits` incomparable, breaking `==` in host code and in
`TestDefaults_AreTheCoreDefaults`.

## Decision

(c). With `Options.FallbackStatuses` set (each 400-599; nil, the default, is off), when a
Before hook changed the body (`!bytes.Equal`) and the upstream answers a listed status,
`forward.Forward` drains (at most 64 KiB) and closes the refused response, unseen and
unlogged, and sends the original body once to the same target with the same headers.
The client gets the second response, whatever it is: there is never a second fallback.

## Consequences

- **Client-first (ADR 0004) holds:** the decision is made after RoundTrip returns status and
  headers and before `startResponse`, so no byte of the refused response reaches the client.
  The first-byte and call deadlines cover both attempts.
- Hooks do not run again; OnEvent, Finalizer and OnResponse see the resend's response as
  they would any response. Credentials pass through verbatim (non-negotiable 4).
- Host signals as in ADR 0019: `Metric.Fallback` and `Metric.FallbackFromStatus` (the
  refused status). `Status`, `Finish` and `Usage` describe the response the client got.
- Off costs one length check per hooked call; the unhooked fast path is unchanged.
- Additive: one `Options` field and two `Metric` fields; a minor release.
- The host chooses the statuses: tollgate takes no position on 429 or 5xx.
- Enforced by `TestForward_FallbackToTheOriginalBody`, `TestForward_FallbackClosesTheRefusedBody`,
  `TestForward_FallbackTransportFailure`, `TestProxy_FallbackToTheOriginalRequest`,
  `TestProxy_FallbackResendsTheOriginalRequest`, `TestValidate`, `TestOptions_FallbackOffByDefault`.
