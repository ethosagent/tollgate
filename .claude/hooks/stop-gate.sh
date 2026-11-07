#!/usr/bin/env bash
# Stop: the session-close architecture check (Context Budget Playbook §4, §9).
# When the working tree has changes, run the fast deterministic gates; on failure
# block the stop and hand the findings back (exit 2, stderr). Slow gates (lint,
# test, vuln) stay in pre-push and CI.
set -uo pipefail
root="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root" || exit 0

input="$(cat)"
if command -v jq >/dev/null 2>&1 && [ "$(jq -r '.stop_hook_active // false' <<<"$input")" = "true" ]; then
  exit 0 # already blocked once this turn; do not loop
fi
[ -n "$(git status --porcelain 2>/dev/null)" ] || exit 0

if ! out="$(scripts/run-checks.sh --fast 2>&1)"; then
  {
    echo "stop-gate: fast gates failed on the current changes. Fix them before finishing:"
    echo "$out" | grep -v '^✓' | head -n 60
  } >&2
  exit 2
fi
exit 0
