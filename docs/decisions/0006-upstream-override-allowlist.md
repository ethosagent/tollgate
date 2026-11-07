# 0006. Upstream override allowlist

- **Status:** Superseded by 0007
- **Date:** 2026-11-07
- **Source:** plan 002, "x-upstream-url" open decision

## Context

A request may carry an upstream override header (default `x-upstream-url`) so
a host can reach a self-hosted or regional endpoint. Honouring it blindly
turns tollgate into an open relay: any caller could make the proxy fetch
internal addresses (SSRF) or forward provider keys to a hostile server.
Options: (a) never honour it; (b) always honour it; (c) honour it only when
the tenant's policy allows the target.

## Decision

(c). `Policy.AllowedUpstreams` lists exact hosts (`api.internal.example`, with
or without a port) and CIDR ranges (`10.0.0.0/8`). The default resolver uses
the header only when the authenticated tenant has a non-empty list that
matches the header's host. Header present but no matching entry: 403. Header
present and no policy (or an empty list): the header is ignored and the
provider's default base URL is used. Only `http` and `https` URLs are accepted;
anything else is a 400. A host that supplies its own `ProviderResolver` owns
this check.

## Consequences

- Safe by default: a zero-config deployment cannot be steered by callers.
- Hostnames are matched literally; a CIDR entry matches only an IP literal in
  the header. tollgate does not resolve DNS, so DNS rebinding is out of scope;
  hosts needing it should pin IPs or supply a resolver.
- The check lives in `internal/resolve`. Enforced by tests
  `TestResolver_UpstreamOverride` and `TestProxy_UpstreamOverrideAllowlist`.
