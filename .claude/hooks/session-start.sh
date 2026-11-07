#!/usr/bin/env bash
# SessionStart: print a short orientation. stdout is added to the agent's context.
# On "compact" it also restates the non-negotiables, which compaction can drop.
set -uo pipefail
cd "${CLAUDE_PROJECT_DIR:-$(dirname "$0")/../..}" || exit 0

source_kind="startup"
if command -v jq >/dev/null 2>&1; then
  source_kind="$(jq -r '.source // "startup"' 2>/dev/null || echo startup)"
fi

branch="$(git branch --show-current 2>/dev/null)"; branch="${branch:-detached}"
dirty="$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
echo "tollgate session ($source_kind): branch=$branch, uncommitted files=$dirty"
echo "Map: CLAUDE.md. Rules: ARCHITECTURE.md. Gate: scripts/run-checks.sh (logs in .logs/)."

. scripts/lib.sh
plan="$(active_plan)"
[ -z "$plan" ] || echo "Active plan: $plan (read it before continuing the task)"

if [ -f HANDOFF.md ]; then
  echo
  echo "=== HANDOFF.md (from the previous session) ==="
  head -n 80 HANDOFF.md
fi

if [ "$source_kind" = "compact" ]; then
  echo
  awk '/^## (Non-negotiables|Protected paths)/{p=1; print; next} /^## /{p=0} p' CLAUDE.md
fi
exit 0
