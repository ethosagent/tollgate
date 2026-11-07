---
name: write-adr
description: Record an architecture decision in docs/decisions. Use when a change moves a boundary, adds a dependency, adds or changes a seam, or reverses an earlier decision.
---

# Write an ADR

## When

- A new third-party dependency (archtest D7).
- A new seam, a change to an exported interface, or a new layer.
- Reversing or narrowing an accepted ADR (supersede it; never edit its decision).

## Steps

1. Create it: `.agents/skills/write-adr/scripts/new-adr.sh "Short title"`. It picks the next
   number, fills the date, and adds the index row (step 4 is then already done). It prints the path.
2. Open the printed file.
3. Fill it: **Context** (forces, options with their costs), **Decision** (one
   or two sentences), **Consequences** (what gets easier/harder, what is
   forbidden, and what enforces it: archtest rule, lint rule or test name).
   Keep it under 60 lines; the size gate caps ADRs at 120.
4. Check the row the script added to `docs/decisions/README.md` (the docs gate checks it exists).
5. If it supersedes an ADR, set the old one's status to `Superseded by NNNN`.
6. Change the enforcement (archtest, lint, test) and `ARCHITECTURE.md` in the
   same PR, so rule and check never disagree. Both are protected paths: the PR
   needs a maintainer.
