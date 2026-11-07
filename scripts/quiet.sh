#!/usr/bin/env bash
# Run any command with failures-only output (Context Budget Playbook: "tooling
# returns signal, not logs"). Full output goes to .logs/quiet.log.
#
#   scripts/quiet.sh go test -race ./internal/forward
#   scripts/quiet.sh go test -run TestValidate .
#
# Pass: one line. Fail: the failing tests, panics and file:line errors (max 60
# lines), then the log path. The exit code is the command's.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
[ "$#" -gt 0 ] || { echo "usage: scripts/quiet.sh <command> [args...]" >&2; exit 2; }
mkdir -p .logs
log=.logs/quiet.log
t0=$(date +%s)
"$@" >"$log" 2>&1
rc=$?
secs=$(( $(date +%s) - t0 ))
if [ "$rc" -eq 0 ]; then
  echo "✓ $* (${secs}s) — log: $log"
  exit 0
fi
echo "✗ $* (${secs}s, exit $rc)"
grep -E -- '--- FAIL|^FAIL|^panic:|\.go:[0-9]+(:[0-9]+)?:|^(ok|FAIL)\s|Error|error:' "$log" | head -n 60 | sed 's/^/  /'
echo "  full log: $log ($(wc -l <"$log" | tr -d ' ') lines)"
exit "$rc"
