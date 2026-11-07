# 0002. Standard-library-only core

- **Status:** Accepted
- **Date:** 2026-11-07
- **Source:** plan, "Rules that keep the library independent"

## Context

Every dependency of the core becomes a dependency of every host, with its
version conflicts and CVEs. Go's standard library covers HTTP/1.1, HTTP/2,
TLS, JSON, `slog` and `httptest`, which is all the core needs.

## Decision

The root package and `internal/*` import only the standard library. Extensions
in `ext/` may import third-party modules (e.g. a Postgres driver) when an ADR
adds them to the archtest allowlist for that layer.

## Consequences

- `go.mod` of the library has no `require` lines; `scripts/check-mod.sh` fails otherwise.
- Hosts that do not use an extension never compile its dependencies… until an
  `ext/` package adds one, because all packages share one module. When the
  first `ext/` dependency arrives, decide (new ADR) whether that extension
  becomes its own module.
- Enforced by archtest D4 and D7.
