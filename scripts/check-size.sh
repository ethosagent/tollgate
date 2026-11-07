#!/usr/bin/env bash
# Gate: Context Budget Playbook size caps (table: size_limit in scripts/lib.sh).
# Hard caps fail; soft caps (Go files over 300 lines) only warn. Also caps the
# skill catalogue at MAX_SKILLS.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
fail=0
# one wc for every file, then pure-bash comparisons
while read -r n f; do
  [ "$f" = total ] && continue
  size_verdict "$f" "$n" || fail=1
done < <(list_files '*.go' '*.md' | tr '\n' '\0' | xargs -0 wc -l)

skills=(.agents/skills/*/SKILL.md)
[ "${#skills[@]}" -lt "$MAX_SKILLS" ] || { echo "✗ ${#skills[@]} project skills (cap < $MAX_SKILLS). Remove unused ones."; fail=1; }
exit "$fail"
