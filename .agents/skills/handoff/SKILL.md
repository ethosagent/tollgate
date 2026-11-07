---
name: handoff
description: Write HANDOFF.md so a fresh session can continue the work. Use when context is large (about 200K tokens), before stopping mid-task, or when the user asks to hand off.
---

# Handoff

Write `HANDOFF.md` at the repo root (git-ignored). The next session's
SessionStart hook prints it. Keep it under 80 lines.

Rules first: write it before the slow zone (about 200K tokens), or when two corrections on the
same issue have failed. If a plan file exists, append to its **Progress** section instead of
duplicating it here, and point at it.

## Template

```markdown
# Handoff — <date> — <branch>

## Goal
<the task and its acceptance criteria, verbatim if from an issue>

## State
- Done: <what is finished and verified, with the command that verified it>
- In progress: <file:line and what is half-done>
- Gate: <output of `scripts/run-checks.sh` summary line, failing gates + first error>

## Decisions
- <decision> — <why> (ADR/link if any)

## Next steps
1. <the very next concrete action>
2. ...

## Traps
- <dead ends already tried, so they are not tried again>
```

## Steps

1. Run `.agents/skills/handoff/scripts/state.sh`; put its output in **State**.
2. Fill every section. No full file contents; point at `file:line`.
3. Tell the user the file is written and what the next step is.
4. A new session that finishes the work deletes `HANDOFF.md`.
