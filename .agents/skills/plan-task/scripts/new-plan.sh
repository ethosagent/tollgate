#!/usr/bin/env bash
# new-plan.sh "Short title" — create plan/NNN-short-title.md (next number) from
# the plan template and print its path. plan/ is git-ignored (local hand-off).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh
title="${1:?usage: new-plan.sh \"Short title\"}"
mkdir -p plan
num="$(next_num plan 3)"
slug="$(slugify "$title")"
path="plan/$num-$slug.md"
cat >"$path" <<PLAN
# $num. $title

- **Date:** $(date +%F)
- **Status:** draft (human reviews before implementation)

## Goal
<!-- one paragraph; link the issue -->

## Acceptance checks
<!-- each one testable; each becomes a row in the PR evidence table -->
- [ ]

## Files to touch
<!-- path — why. Aim for about 3 files; more means split the plan -->
-

## Steps
<!-- test first, then code, then scripts/run-checks.sh -->
1.

## Open decisions
-

## Progress
<!-- the implementer appends here; the next session starts from it -->
PLAN
echo "$path"
