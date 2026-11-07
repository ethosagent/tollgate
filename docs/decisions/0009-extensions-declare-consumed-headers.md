# 0009. Extensions declare the request headers they consume

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review of milestone 008 (PRIV-4)

## Context

The request filter stripped only `Options.Headers` (and hop-by-hop headers).
`statickeys.Config.Header` let a host move the tenant key to another header
without mirroring it into `Options.Headers.TenantKey`; the key then
authenticated fine and was forwarded to the upstream provider. Any
third-party Authenticator reading its own header had the same hole. Options
were (a) drop the knob and force every authenticator onto
`Options.Headers.TenantKey`, or (b) let an extension declare what it reads.

## Decision

(b). `internal/model` exports the optional `HeaderConsumer`
(`ConsumedHeaders() []string`), aliased in `model.go`. `New` type-asserts
`Auth`, `Admission`, `Providers` and `Sessions` once and adds the names to the
strip set built from `Options.Headers`. `Validate` rejects an empty name or a
reserved one (`Authorization`, `x-api-key`, `Content-Type`, `Content-Length`,
`Host`, `Connection`), so an extension can never strip upstream credentials.
`statickeys.Auth` implements it. Option (a) was rejected: it couples every
authenticator to core options it cannot see at construction time.

## Consequences

- A tenant credential is stripped wherever it lives, with no host mirroring.
- The names are read once in `New`; an extension that changes them later is
  not followed (documented on the interface).
- Enforced by `TestAuth_TenantKeyNeverReachesUpstream` (ext/auth/statickeys)
  and `TestValidate_ConsumedHeaders`.
