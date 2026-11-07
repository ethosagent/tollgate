# 0011. Extensions with third-party dependencies live in nested modules

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** plan 012 (Postgres metric sink)

## Context

The core is stdlib-only (ADR 0002), but some extensions need a driver, for
example the Postgres sink needs pgx. Options: (a) add pgx to the root
`go.mod`: every host downloads it and the root graph is no longer empty;
(b) hand-roll the Postgres wire protocol: unmaintainable and a security risk;
(c) give the extension its own `go.mod` under `ext/`, as `tools/` already does
for dev tools (ADR 0005).

## Decision

(c). An extension that needs a third-party module is a nested Go module at
`ext/<area>/<name>/` with `require github.com/ethosagent/tollgate` and a
development-only `replace github.com/ethosagent/tollgate => ../../..`. The root
`go.mod` stays free of requirements. Each such dependency needs an allowlist
entry in `internal/archtest` (D7) and a mention in this ADR's consequences.

## Consequences

- Hosts that do not import the extension never download its dependencies.
- Allowlisted today: `github.com/jackc/pgx/v5` for `ext/metrics/postgres`.
- `./...` from the root excludes nested modules. `scripts/lib.sh`
  (`nested_modules`) lists them and `run-checks.sh` gates (fmt, vet, mod, lint,
  test, vuln) run in each. `check-mod.sh` keeps the root requirement-free and
  tidies every nested module.
- `internal/archtest` `TestNestedModules` runs `go list` inside each nested
  module: only stdlib, the root public API, the module's own packages and its
  allowlisted deps are accepted. Importing `internal/` is also rejected by Go
  itself across module boundaries (the "use of internal package" error).
- The `replace` directive is for in-repo development and CI only. Go ignores
  `replace` in dependencies, so consumers resolve the tagged root release.
- Release tags are path-prefixed: `ext/metrics/postgres/vX.Y.Z` releases the
  nested module (`git tag ext/metrics/postgres/v0.1.0`); root releases stay
  `vX.Y.Z`. Before tagging, drop the `replace` or bump the `require` to a
  published root version.
- The sink's integration test needs `TOLLGATE_TEST_PG_DSN`; locally it skips
  loudly, with `CI=true` it fails. CI provides a `postgres` service container.
