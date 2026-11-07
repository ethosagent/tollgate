#!/usr/bin/env bash
# PreToolUse(Bash): keep tool output small (Context Budget Playbook §1, §7).
# Blocks raw test/lint/vuln runs whose full output would land in context, and
# names the quiet equivalent. Allowed: scripts/quiet.sh ..., scripts/run-checks.sh,
# and any command already piped through tail/head/grep.
# Best-effort: it reads the command text, so `bash -c`, `xargs` and similar can
# slip past. The env output caps in .claude/settings.json are the backstop.
set -uo pipefail
input="$(cat)"
case "$input" in *"go test"* | *"go vet"* | *golangci-lint* | *govulncheck*) ;; *) exit 0 ;; esac # fast path
cmd="$(jq -r '.tool_input.command // empty' <<<"$input" 2>/dev/null)" || exit 0

case "$cmd" in
  *scripts/quiet.sh* | *scripts/run-checks.sh* | *scripts/check-*.sh*) exit 0 ;;
esac
pipe_re='\|[[:space:]]*(tail|head|grep|wc|jq)([[:space:]]|$)'
noisy_re='(^|[;&|(][[:space:]]*|[[:space:]])(go[[:space:]]+(test|vet)|golangci-lint|govulncheck)([[:space:]]|$)'
[[ $cmd =~ $pipe_re ]] && exit 0

if [[ $cmd =~ $noisy_re ]]; then
  {
    echo "quiet-guard: run this through the failures-only wrapper so full output stays out of context:"
    echo "  scripts/quiet.sh $cmd"
    echo "or a gate: scripts/run-checks.sh <fmt|vet|arch|lint|test|vuln|docs|size>"
  } >&2
  exit 2
fi
exit 0
