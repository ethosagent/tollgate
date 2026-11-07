# docs

Start with [`../CLAUDE.md`](../CLAUDE.md) (the map), then
[`../ARCHITECTURE.md`](../ARCHITECTURE.md) (the rules).

| Folder / file | What is in it |
|---|---|
| [`orientation.md`](orientation.md) | Repo tour and how one call flows end to end |
| [`context-budget.md`](context-budget.md) | Context Budget Playbook: each rule and what enforces it |
| [`conventions.md`](conventions.md) | Go style, errors, logging, tests, naming |
| [`decisions/`](decisions/README.md) | Architecture decision records (ADRs) |
| [`runbooks/local-dev.md`](runbooks/local-dev.md) | Set up, run, debug locally |

Every doc here is capped at 200 lines (`scripts/check-size.sh`). If a doc grows
past that, split it by topic.
