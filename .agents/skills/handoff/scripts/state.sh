#!/usr/bin/env bash
# state.sh — print the facts a HANDOFF.md needs, compactly: branch, changed
# files, active plan, and the fast-gate summary. Paste the output into "State".
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh
echo "branch: $(git branch --show-current) @ $(git rev-parse --short HEAD 2>/dev/null || echo 'no commits')"
echo "changed files:"; git status --short | head -n 40 | sed 's/^/  /'
plan="$(active_plan)"
echo "active plan: ${plan:-none}"
echo "fast gates:"; scripts/run-checks.sh --fast 2>&1 | grep -E '^(✓|✗|!|---)' | sed 's/^/  /'
