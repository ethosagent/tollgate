# tollgate — agent map

> CLAUDE.md and AGENTS.md are byte-identical (`scripts/check-docs.sh` fails if
> not). Edit one, copy it over the other. Keep this file under 150 lines; put
> detail in `docs/` and link it.

## What this is

An embeddable **LLM proxy library** in Go, with a thin binary around it. The
core does five things: resolve the upstream, identify the session, normalise
the call, forward-and-tee the stream, emit a metric. Everything else (auth,
admission, monitors, routing, memory, billing) is an **extension** behind one
of nine interfaces. The library is the product; `cmd/tollgated` is a wrapper.

Design doc and milestone plans live in `plan/` (git-ignored, local only);
`plan/000-roadmap.md` is the index. The constitution is
[`ARCHITECTURE.md`](ARCHITECTURE.md). Decisions are in [`docs/decisions/`](docs/decisions/README.md).
Measured overhead and the bench method: [`docs/performance.md`](docs/performance.md).

## Golden path

```sh
scripts/install-hooks.sh          # once: pre-commit (fast) + pre-push (full gate)
scripts/run-checks.sh             # THE gate; CI runs exactly this
scripts/run-checks.sh --fast       # inner loop: fmt vet arch docs size (seconds)
scripts/quiet.sh go test -race ./internal/forward  # any command, failures-only output
go run ./cmd/tollgated serve -listen :8080
```

The gate prints only failures; full output is in `.logs/<gate>.log`. Gates:
`fmt vet mod arch lint test vuln docs size`. Fix findings in that order.

## Package map

| Path | Role |
|---|---|
| `tollgate.go`, `options.go`, `model.go` | Public API: `New`, `Use`, `Handler`, `Options`, `Limits`, aliases of the model |
| `internal/model/` | Neutral model (`Tenant`, `Session`, `Call`, `Event`, `Metric`) + extension interfaces + Noop defaults |
| `internal/forward/` | Upstream target check, transport, header filter, forward + relay, client-first SSE tee |
| `internal/chain/` | Ordered hook chain, panic guard, deadlines, Finalizer barrier |
| `internal/normalise/` | Wire format ⇄ neutral model (OpenAI chat, Anthropic messages), provider-shaped errors |
| `internal/resolve/` | Default `ProviderResolver` (path prefix → provider) |
| `internal/session/` | Default `SessionStrategy` (header-or-predict, TTL sweeper) |
| `internal/metrics/` | Bounded metric queue + batcher + drop counter |
| `internal/fixtures/` | Recorded provider fixtures (leaf, stdlib only); re-exported by `testkit` |
| `internal/methodset/` | Own-code vs promoted method detection (leaf); shared by `chain`, `testkit`, root |
| `internal/archtest/` | The dependency-law gate (protected) |
| `testkit/` | Public: fake upstream + plugin harness for extension authors |
| `ext/<area>/<name>/` | Built-in and example extensions (public API only) |
| `cmd/tollgated/` | The binary: flags → `Options` → serve → graceful shutdown |
| `tools/` | Separate Go module pinning dev tools (golangci-lint, govulncheck) |

## Dependency law (enforced by `internal/archtest`)

- **D1** every package belongs to a layer in `ARCHITECTURE.md`.
- **D2** `internal/model` → stdlib only.
- **D3** `internal/*` → stdlib + `internal/*`. Never root, `ext`, `testkit`, `cmd`. In-package tests use `internal/fixtures` (importing `testkit` would be a cycle).
- **D4** root `tollgate` → stdlib + `internal/*`. **No third-party deps in the core.**
- **D5** `ext/**`, `cmd/**` → public API only. Never `internal/`; `testkit` only in tests (except the `cmd/tollgate-bench` dev tool).
- **D6** `testkit` → stdlib + root + `internal/fixtures` + `internal/methodset`; those two leaves → stdlib only.
- **D7** third-party imports need an allowlist entry in archtest **and an ADR**.
- **P1** every package has a doc comment (`doc.go`): read it before the code.

Lint (`.golangci.yml`) bans `http.DefaultClient`/`http.Get`, `log.Print*`,
`fmt.Print*`, and `os.Getenv`/`os.Exit` outside `cmd/`. Functions ≤ 50 lines.

## Non-negotiables

1. **Client-first streaming.** Bytes reach the client before any hook sees them. No hook can stall, reorder or break the stream.
2. **Isolation.** Every extension call has `recover()` and a deadline (except Auth/Admission/resolver declaring `InlineSafe`, ADR 0015). A broken extension degrades to pass-through, never to an outage.
3. **Transparent.** Body, model and upstream credentials pass through unchanged unless an extension changes them.
4. **Never hold provider keys.** `Authorization` / `x-api-key` are forwarded verbatim, never stored, never logged.
5. **Privacy mode is on by default.** No transcripts, reasoning, tool payloads or raw bytes in logs.
6. **Never block on bookkeeping.** Metrics and events go through bounded queues; overflow drops and counts.
7. **Stable plugin API.** After v1, interfaces are frozen; new capability = new *optional* interface (type-assert).
8. **No globals, no `init()` magic, no `.so` plugins.** Wiring is code in `Options`.

## Where new code goes

| You are adding… | Put it in | Skill |
|---|---|---|
| An extension (auth, hook, sink, admission) | `ext/<area>/<name>/` | [`add-extension`](.agents/skills/add-extension/SKILL.md) |
| A provider wire format | `internal/normalise/<wire>.go` + `internal/fixtures/testdata` | [`add-provider`](.agents/skills/add-provider/SKILL.md) |
| An `Options`/`Limits` field | `options.go` + test + `ARCHITECTURE.md` §6 | [`add-option`](.agents/skills/add-option/SKILL.md) |
| A model type or interface | `internal/model/` + alias in `model.go` | ADR first (it is public API) |
| A boundary change | `docs/decisions/NNNN-*.md` | [`write-adr`](.agents/skills/write-adr/SKILL.md) |
| A multi-file task | `plan/NNN-title.md` (git-ignored) | [`plan-task`](.agents/skills/plan-task/SKILL.md) |

## How to work here

- **Test first.** Write the failing test, then the code. Table-driven tests. Use `httptest`, never the network.
- **Small diffs.** One concern per PR; fill `.github/pull_request_template.md`.
- **Read the ADR before changing its area.** Write a new ADR when a change moves a boundary; mark the old one superseded.
- **Review with the rubrics** in [`.agents/reviews/`](.agents/reviews/README.md) (`STR-*`, `PRIV-*`, `API-*`, `HOT-*`, `TEST-*`). The PR's risk tier (from `.agents/risk.yaml`) picks the lenses.
- **Path rules** in `.claude/rules/` load when you touch streaming code, the public API or tests.

## Context budget (playbook: [`docs/context-budget.md`](docs/context-budget.md))

- One task per session. Plan multi-file work to a file first; implement in a fresh session.
- Hooks enforce the mechanics: whole-file reads over 400 lines are blocked; raw `go test`/lint
  runs are blocked in favour of `scripts/quiet.sh`; edits are gofmt'd and the twin synced; the
  Stop hook runs `scripts/run-checks.sh --fast` before you finish. Fix what they report; never bypass them.
- Caps: Go file 300 soft / 800 hard, test file 500, function 50, docs 200, this file 150.
- Subagents return ≤ 2K tokens with `file:line` refs; longer output goes to a file.
- Hand off before ~200K tokens with the [`handoff`](.agents/skills/handoff/SKILL.md) skill.

## Protected paths — stop and ask

`scripts/**`, `.githooks/**`, `.github/**`, `.golangci.yml`, `.agents/**`,
`internal/archtest/**`, `tools/**`, `.claude/settings.json`, `.claude/hooks/**`,
`ARCHITECTURE.md`.
These decide what "green" means. **Never** make a gate pass by lowering
`COVERAGE_MIN`, skipping or deleting a test, adding `//nolint` or a lint
exclusion, removing a gate, or widening an archtest rule. Fix the code. If a
gate itself is wrong, stop and ask the maintainer.

## Do NOT

- Add a third-party dependency to the core (ADR 0002).
- Import `internal/` from `ext/`, `cmd/` or `testkit/`.
- Parse provider JSON in a hook; use the neutral `Call`/`Event` (or `Event.Raw` if you must).
- Buffer a streaming response, or do I/O on the client write path.
- Log headers that carry credentials, or any body.
- Add `pkg/`; use the layout above.

## Compact instructions

When compacting, keep: the current task and its acceptance criteria, files
changed so far, failing gates and their first error, decisions taken and why,
and the next step. Drop: full file contents, passing test output, exploration
dead ends.
