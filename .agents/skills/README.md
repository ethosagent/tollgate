# Skills

Tool-agnostic home for this repo's agent skills. `.claude/skills` is a symlink
here, so there is one copy. Skills are for **recurring procedures** only;
one-off knowledge goes in `docs/`.

| Skill | Use it to |
|---|---|
| [`add-extension`](add-extension/SKILL.md) | add an extension under `ext/` (auth, hook, sink, admission, finalizer) |
| [`add-provider`](add-provider/SKILL.md) | add an upstream wire format to `internal/normalise` |
| [`add-option`](add-option/SKILL.md) | add an `Options` / `Limits` / `HeaderNames` field |
| [`write-adr`](write-adr/SKILL.md) | record a decision that moves a boundary |
| [`plan-task`](plan-task/SKILL.md) | write `plan/NNN-title.md` before coding a multi-file task |
| [`handoff`](handoff/SKILL.md) | write `HANDOFF.md` before a long session ends |

A `SKILL.md` starts with frontmatter: `name` equal to its directory and a
`description` of at most 250 characters (`scripts/check-docs.sh`). Skills defer
to `CLAUDE.md` for rules instead of restating them. Put the rules that must never be missed in
the first 60 lines, and ship mechanical steps as `scripts/*.sh` next to the `SKILL.md`; running
a script costs a few lines of output, reasoning through the same step costs thousands of tokens.
