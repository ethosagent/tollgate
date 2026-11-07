# 0008. Error-to-status contract for extensions

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review of milestone 002 (simplify-002 A2)

## Context

The proxy turned extension errors into statuses through three ad-hoc
anonymous interfaces: Auth honoured only 403 (a 429 or 503 from an auth
backend became 401), Admission always answered 429, and resolver errors were
matched against package sentinels. Extension authors had no named type to
implement.

## Decision

`internal/model` exports `StatusError` (`error` + `HTTPStatus() int`) and the
optional `RetryAfterError` (`error` + `RetryAfter() time.Duration`), aliased in
`model.go`. `model.StatusOf(err, fallback)` is the one classifier: the first
`StatusError` in the chain with a 4xx/5xx status wins, else the seam default
(Auth 401, Admission 429, resolver 404). Admission rejects always carry
`Retry-After` (from `RetryAfterError`, rounded up, at least 1 s). Core
sentinels (`internal/forward`: 400, 403, 413, 502, 504) implement the same
interface.

## Consequences

- A custom Auth can answer 403, 429 or 503; a status outside 400-599 is ignored.
- Adding a method to these interfaces later is forbidden (stable plugin API);
  new behaviour gets a new optional interface.
- Enforced by `TestStatusOf`, `TestProxy_ExtensionStatusErrors`,
  `TestProxy_AuthErrors`, `TestAliases_CoverModel`.
