# 0012. The session header reaches the SessionStrategy as a Call field

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review of milestone 007 (API-4, API-5)

## Context

The core strips the caller's session header (`HeaderNames.Session`) before
forwarding, so a `SessionStrategy` cannot read it from `Call.Header`. Milestone
007 passed it in `Call.Meta` under the key `"tollgate.session_header"`, a
constant in `internal/session`. A host strategy in `ext/` may not import
`internal/` (D5), so it had to hard-code an undocumented string, type-assert
an `any`, and race hooks that could see the key before the core deleted it.
Options: (a) export the Meta key from the root and document it on seam 3;
(b) a typed field on `Call` that the core sets before `Resolve`.

## Decision

(b). `Call.SessionHeader string` carries the trimmed header value, or "" when
absent. The core sets it before `SessionStrategy.Resolve` and never changes it;
the default strategy reads it, and so may any host strategy or hook. (a) was
rejected: a magic string in a map of `any` is neither discoverable nor
type-checked, and `Meta` is documented as extension scratch space.

## Consequences

- Adding a field is not a breaking change; no interface changes.
- Hooks can read the raw header value. It is the caller's own session id,
  already equal to `Session.ID` for header sessions, and never a credential.
- Enforced by `TestSession_HostStrategyReadsTheSessionHeader` (root) and the
  `internal/session` tests, which set only `Call.SessionHeader`.
