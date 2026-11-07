# 0005. Dev tools pinned in a separate module

- **Status:** Accepted
- **Date:** 2026-11-07
- **Source:** scaffolding

## Context

The gate needs golangci-lint and govulncheck at fixed versions so local, hook
and CI results match. Options: (a) global installs: drift; (b) `tool`
directives in the root `go.mod`: pins them, but adds ~200 modules to the
library's graph that every host would see; (c) `tool` directives in
`tools/go.mod`, run with `go tool -modfile=tools/go.mod`.

## Decision

(c). `tools/` is its own module. Scripts call `go tool -modfile=tools/go.mod`.

## Consequences

- Versions are checksum-pinned in `tools/go.sum`; Dependabot bumps them.
- The library's `go.mod` stays empty (ADR 0002).
- The first run builds the tools (about a minute); later runs use the build cache.
- `./...` from the root does not include `tools/` (nested module).
