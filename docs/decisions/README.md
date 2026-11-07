# Architecture decision records

Read the ADR for an area before you change it. Write a new one when a change
moves a boundary (skill: [`write-adr`](../../.agents/skills/write-adr/SKILL.md)).
Never edit an accepted ADR's decision; supersede it with a new one.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-library-first.md) | Library first, binary as a thin wrapper | Accepted |
| [0002](0002-stdlib-only-core.md) | Standard-library-only core | Accepted |
| [0003](0003-neutral-model-in-internal-model.md) | Neutral model in `internal/model`, aliased from the root | Accepted |
| [0004](0004-client-first-streaming.md) | Client-first streaming tee | Accepted |
| [0005](0005-dev-tools-in-separate-module.md) | Dev tools pinned in a separate module | Accepted |
| [0006](0006-upstream-override-allowlist.md) | Upstream override allowlist | Accepted |

Template: [`template.md`](template.md).
