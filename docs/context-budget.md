# Context budget in this repo

This repo follows Mitesh's **Context Budget Playbook**
([artifact](https://claude.ai/artifact/NQR91pERevfwVEhqZ3pdpc)): a coding agent reasons well only
while its context is small. This file maps each rule to **what enforces it here**. Prefer a
deterministic check (a gate or a hook) over prose: a rule only the agent remembers is lost at the
first compaction.

## Size caps (source of truth: `size_limit`, `MAX_SKILLS`, `MAX_SKILL_DESC` in `scripts/lib.sh`)

| File | Cap (lines) | Enforced by |
|---|---|---|
| `CLAUDE.md` = `AGENTS.md` | 150 | `size` gate, post-edit hook, Stop hook |
| `ARCHITECTURE.md` | 300 | same |
| Any doc in `docs/`, `.agents/`, root `*.md` | 200 | same |
| ADR / ADR index | 120 / 150 | same |
| `SKILL.md` / skill `references/*.md` | 300 / 200 | same; description ≤ 250 chars: `docs` gate |
| `.claude/rules/*.md` | 60 | same |
| Go source file | 300 soft (warn) · 800 hard | same; whole-file Read blocked over 400 |
| Go test file | 500 | same |
| Function | 50 lines, cognitive complexity 20 | `lint` gate (`funlen`, `gocognit`) |
| Project skills | < 40 | `size` gate |

## Hooks (`.claude/settings.json`, scripts in `.claude/hooks/`)

| Hook | What it does deterministically |
|---|---|
| SessionStart | Prints branch, dirty count, the newest `plan/NNN-*.md`, `HANDOFF.md`; after compaction, restates the non-negotiables and protected paths |
| PreToolUse Read | Blocks whole-file reads over 400 lines; asks for a ranged read |
| PreToolUse Bash | Best-effort: blocks raw `go test`/`go vet`/`golangci-lint`/`govulncheck` and names `scripts/quiet.sh`; env output caps are the backstop |
| PostToolUse Edit/Write | `gofmt -w` the edited Go file (syntax errors reported back), syncs `CLAUDE.md`↔`AGENTS.md`, reports a file over its cap |
| Stop | Session-close check: when the tree is dirty, runs `run-checks.sh --fast` (`FAST_GATES`); a failure blocks finishing and returns the findings |

Output caps and the compaction backstop are in the `env` block of `.claude/settings.json`
(400K auto-compact window, 15K Bash / file-read / MCP output).

## Quiet tooling

- `scripts/run-checks.sh` prints failures only; full logs in `.logs/<gate>.log`; `--json` adds a summary line.
- `scripts/quiet.sh <cmd>`: any command, one line on pass, filtered failures plus the log path on fail.
- Deny-read rules keep `go.sum`, coverage profiles and binaries out of context.

## Session habits (not enforceable; keep them)

- One task per session. Multi-file work starts with the `plan-task` skill; implement it in a fresh session.
- Subagents return ≤ 2K tokens with `file:line` citations; longer output goes to a file.
- Hand off before ~200K tokens (`handoff` skill); never rely on auto-compaction.
- Pick model and effort once per session; switching mid-session breaks the prompt cache.
- Package docs are `doc.go` (archtest P1), the Go form of the playbook's package README.
