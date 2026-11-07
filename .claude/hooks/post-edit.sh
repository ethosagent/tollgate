#!/usr/bin/env bash
# PostToolUse(Edit|Write|MultiEdit): deterministic fix-ups right after an edit,
# so the agent never spends a turn on them and the gate never fails for them.
#   *.go                 gofmt -w; a syntax error is reported back (exit 2)
#   CLAUDE.md/AGENTS.md  copy to the twin (they must stay byte-identical)
#   any capped file      over its hard size cap -> reported back (exit 2)
set -uo pipefail
root="${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"
cd "$root" || exit 0
file="$(jq -r '.tool_input.file_path // empty' 2>/dev/null)" || exit 0
case "$file" in *.go | *.md) ;; *) exit 0 ;; esac # nothing to do for other files
[ -f "$file" ] || exit 0
rel="${file#"$root"/}"
. scripts/lib.sh

case "$rel" in
  *.go)
    if ! out="$(gofmt -l -w "$rel" 2>&1)"; then
      echo "post-edit: gofmt could not parse $rel:" >&2
      echo "$out" | head -n 20 >&2
      exit 2
    fi ;;
  CLAUDE.md) cp CLAUDE.md AGENTS.md ;;
  AGENTS.md) cp AGENTS.md CLAUDE.md ;;
esac

if ! msg="$(check_size "$rel")"; then
  echo "post-edit: $msg" >&2
  exit 2
fi
[ -z "$msg" ] || echo "post-edit: $msg" >&2 # soft-cap warning, does not block
exit 0
