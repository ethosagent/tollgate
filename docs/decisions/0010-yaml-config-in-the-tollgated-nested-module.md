# 0010. YAML config lives in the tollgated nested module

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** plan 010 (tollgated: YAML config and operations)

## Context

`tollgated` must read a YAML config file (listen address, auth keys,
providers, sessions, admission, metrics, limits). The standard library has no
YAML decoder. Options: (a) add `go.yaml.in/yaml/v3` to the root `go.mod`: the
root module is requirement-free by rule (ADR 0002, `check-mod.sh`), and every
library host would download it; (b) hand-write a YAML subset parser: a
maintenance and correctness risk for a file that carries secrets; (c) make
`cmd/tollgated` a nested module, as ADR 0011 does for extensions with
third-party dependencies.

## Decision

(c). `cmd/tollgated` has its own `go.mod`
(`github.com/ethosagent/tollgate/cmd/tollgated`) that requires the root module
with a development-only `replace => ../..` and `go.yaml.in/yaml/v3`. The root
`go.mod` stays free of requirements. The binary uses the public API and
`ext/**` only; `testkit` only from its tests. `cmd/tollgate-bench` stays in the
root module (stdlib plus `testkit`).

## Consequences

- Library hosts never download a YAML module.
- `internal/archtest` `nestedThirdParty` allows `go.yaml.in/yaml/v3` for
  `cmd/tollgated` only; `thirdParty["cmd"]` (root-module cmd packages) stays
  empty. `TestNestedModules` now discovers nested modules under `ext/` and
  `cmd/`, and checks that a nested module's non-test files never import
  `testkit` (`TestNestedViolations_CoverTestImports`, "testkit in code").
- `scripts/lib.sh` `nested_modules` searches `ext` and `cmd`, so
  fmt/vet/mod/lint/test/vuln gates run in `cmd/tollgated` through `each_module`.
- Build and run from the module: `go -C cmd/tollgated run . serve`; the
  Makefile does this. `go run ./cmd/tollgated` from the root no longer works.
- Releases of the binary use a path-prefixed tag, `cmd/tollgated/vX.Y.Z`.
  Before tagging, drop the `replace` or bump the `require` to a published
  root version (ADR 0011).
- Config decoding is strict (`KnownFields(true)`); secrets come from `${VAR}`
  and are never logged (`TestServe_NeverLogsSecrets`).
