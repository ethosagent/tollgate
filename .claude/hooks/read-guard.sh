#!/usr/bin/env bash
# PreToolUse(Read): block whole-file reads of large text files so one Read does
# not eat the context budget. Reads with an offset or limit always pass.
# Exit 2 blocks the call and shows stderr to the agent.
set -euo pipefail
MAX_LINES="${READ_GUARD_MAX_LINES:-400}"

input="$(cat)"
command -v jq >/dev/null 2>&1 || exit 0 # no jq: do not block work
IFS=$'\t' read -r file offset limit < <(jq -r '.tool_input | [.file_path // "", .offset // "", .limit // ""] | @tsv' <<<"$input")

[ -n "$file" ] && [ -f "$file" ] || exit 0
[ -z "$offset" ] && [ -z "$limit" ] || exit 0
case "$file" in *.png|*.jpg|*.jpeg|*.gif|*.webp|*.pdf|*.ipynb) exit 0 ;; esac

lines="$(wc -l <"$file" | tr -d ' ')"
if [ "$lines" -gt "$MAX_LINES" ]; then
  echo "read-guard: $file has $lines lines (limit $MAX_LINES for a whole-file read)." >&2
  echo "Use Grep to find the part you need, then Read with offset/limit." >&2
  exit 2
fi
exit 0
