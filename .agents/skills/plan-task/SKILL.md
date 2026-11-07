---
name: plan-task
description: Turn a task into a plan file (plan/NNN-title.md) before coding, so implementation can run in a fresh session. Use for any task touching more than one package, a new seam, or work likely to exceed one session.
---

# Plan a task to a file

Rules first:
- The plan lives in a **file**, not in chat. It survives compaction and seeds the next session.
- A plan that touches more than ~3 files or ~300 lines is two plans.
- Research with subagents; each returns ≤ 2K tokens with `file:line` citations.
- A human reviews the plan before implementation starts.

## Steps

1. Create the file: `.agents/skills/plan-task/scripts/new-plan.sh "Short title"` (prints the path).
2. Research: read `CLAUDE.md`, the relevant ADRs and package `doc.go` files. Delegate wide reads to
   subagents; keep only their summaries.
3. Fill **Goal**, **Acceptance checks** (testable), **Files to touch** (path — why), **Steps**
   (test first), **Open decisions**. Name the skill each step uses (`add-option`, `add-provider`, …).
4. Stop and ask the user to review the plan.
5. Implementation happens in a fresh session: "implement plan/NNN-title.md". The implementer
   appends to **Progress** as it goes; the SessionStart hook prints the newest plan path.
6. Done: all acceptance checks have evidence, `scripts/run-checks.sh` is green, the plan's
   Progress says so. Then `/clear`.
