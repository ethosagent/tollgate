# 0017. A committed go.work builds nested modules; releases carry no replace

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** first release (v0.1.0); refines the release notes of ADR 0010 and ADR 0011

## Context

`cmd/tollgated` and `ext/metrics/postgres` are nested modules that require the
root module. Until now each had `require github.com/ethosagent/tollgate v0.0.0`
plus a dev `replace => ../..`. That cannot ship:

- `go install github.com/ethosagent/tollgate/cmd/tollgated@vX` refuses a module
  whose go.mod has a replace directive ("must not contain directives that would
  cause it to be interpreted differently than if it were the main module",
  `go help install`). Verified with a file-based GOPROXY and Go 1.27.3.
- Go ignores replace in dependencies, so a library user of the postgres sink
  would resolve root `v0.0.0`, which does not exist. Verified the same way.

Options: (a) keep replace and strip it in a release-only commit each time: the
tagged tree differs from what CI built, and it is easy to forget; (b) require a
published root version and no replace, and let a committed `go.work` point
local dev and CI at the checkout; (c) require pseudo-versions of the root:
churn on every root commit, and still needs a push before the bump.

## Decision

(b). A committed `go.work` (`go 1.26`) uses `.`, `./cmd/tollgated` and
`./ext/metrics/postgres` (not `tools/`). A nested module requires a published
root tag and has no replace directive. Until root `v0.1.0` exists the replace
stays (bootstrap); the release bump removes it for good.

## Consequences

- Every `go build/vet/test/list/run` inside a nested module uses the checkout's
  root code (workspace mode), so CI never tests against a stale release.
  `go -C cmd/tollgated run . serve` works unchanged. Root `./...` still excludes
  nested modules in workspace mode, so per-module gates are unchanged.
- `go mod tidy` ignores go.work: it resolves the required root release from the
  proxy and records its go.sum lines. A nested module may use root API newer
  than the release it requires; `go.work` hides that, the release guard below
  catches it.
- `go tool -modfile` is refused in workspace mode, so `scripts/lib.sh gotool`
  builds tools with `GOWORK=off` and runs the cached binary (`go tool -n`) with
  the workspace on, so golangci-lint and govulncheck type-check nested modules
  against the checkout.
- Release guard: `scripts/check-release.sh <tag>` fails when the module has a
  replace directive, when a nested module requires a root version other than
  the newest root tag reachable from HEAD (or the one passed), or when it does
  not build with `GOWORK=off`. `release.yml` runs it before the gate.
- Release order (runbook `docs/runbooks/release.md`): tag root `vX.Y.Z` and
  push; one commit bumps every nested require to it and drops replace; tag
  `cmd/tollgated/vX.Y.Z` and `ext/metrics/postgres/vX.Y.Z`.
- Enforced by: archtest `TestWorkspace` (go.work uses exactly root plus every
  nested module), `check-release.sh`, and the `compat` CI job, which builds and
  race-tests every module on the minimum Go.
- If Go ever writes a `go.work.sum`, commit it next to `go.work`.
- Users who build a nested module from a checkout without the workspace
  (`GOWORK=off`) get the published root, as a consumer would.
