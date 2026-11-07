# 0007. Upstream target checked in the core for every resolver

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review of milestone 002 (simplify-002 A1, gosec G704); supersedes 0006

## Context

ADR 0006 put the upstream-override allowlist inside the default
`ProviderResolver` and left hosts with their own resolver to "own this check".
So the SSRF guard covered only one resolver, the proxy built its outbound
`http.Request` by hand to avoid a gosec G704 finding, and a custom resolver
could return any URL (a `file:` scheme, embedded credentials) unchecked.
Options: (a) keep the check per resolver; (b) wrap resolvers; (c) apply the
override and validate the final target once in the core.

## Decision

(c). The resolver only picks the provider and its base URL. The core then
calls `internal/forward.Target`, the single check every upstream URL passes:
the target is the resolver's base, or the override header when the tenant's
`Policy.AllowedUpstreams` matches it (rules unchanged from 0006: exact host or
host:port, IP or CIDR for IP literals, no DNS; empty list ignores the header;
mismatch 403; unusable override 400). Both must be `http`/`https` with a host
and no userinfo; a bad resolver base is 502. The request is built with
`http.NewRequestWithContext` from that target.

## Consequences

- Custom resolvers get the safe override and the target check for free; a
  custom resolver that implemented its own override is subject to the tenant
  policy as well.
- `resolve.New` no longer takes the header name.
- Enforced by `TestTarget_UpstreamOverride`, `TestTarget_RejectsBadResolverBase`,
  `TestProxy_TargetValidatedForEveryResolver`, `TestProxy_UpstreamOverrideAllowlist`.
