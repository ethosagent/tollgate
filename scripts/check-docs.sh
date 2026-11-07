#!/usr/bin/env bash
# Gate: the agent-facing docs stay true and small (Context Budget Playbook).
#   1. CLAUDE.md and AGENTS.md are byte-identical.
#   2. (size caps live in check-size.sh)
#   3. Every skill has frontmatter: name == its directory, description <=250 chars.
#   4. .claude/skills is a symlink to ../.agents/skills (one copy of each skill).
#   5. Every ADR is listed in docs/decisions/README.md.
#   6. Relative markdown links in agent docs point at files that exist.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
fail=0
err() { echo "✗ $*"; fail=1; }

cmp -s CLAUDE.md AGENTS.md || err "CLAUDE.md and AGENTS.md differ. Fix: edit one, then cp CLAUDE.md AGENTS.md (or the reverse)."

for s in .agents/skills/*/SKILL.md; do
  d="${s%/SKILL.md}"; name="${d##*/}"
  # one pass over the frontmatter: prints "<first line>\t<name>\t<description>"
  IFS=$'\t' read -r first fm_name desc < <(awk 'NR==1{f=$0} /^---$/{n++; next}
    n==1 && /^name:/{sub(/^name:[ ]*/,""); nm=$0}
    n==1 && /^description:/{sub(/^description:[ ]*/,""); ds=$0}
    END{printf "%s\t%s\t%s\n", f, nm, ds}' "$s")
  [ "$first" = "---" ] || { err "$s: missing frontmatter"; continue; }
  [ "$fm_name" = "$name" ] || err "$s: frontmatter name '$fm_name' != directory '$name'"
  [ -n "$desc" ] || err "$s: empty description"
  [ "${#desc}" -le "$MAX_SKILL_DESC" ] || err "$s: description is ${#desc} chars (cap $MAX_SKILL_DESC)"
done

[ "$(readlink .claude/skills)" = "../.agents/skills" ] || err ".claude/skills must be a symlink to ../.agents/skills"

for adr in docs/decisions/[0-9][0-9][0-9][0-9]-*.md; do
  grep -qF "$(basename "$adr")" docs/decisions/README.md || err "$adr is not listed in docs/decisions/README.md"
done

while IFS= read -r f; do
  dir="${f%/*}"; [ "$dir" = "$f" ] && dir=.
  while IFS= read -r link; do
    target="${link%%#*}"
    [ -z "$target" ] && continue
    case "$target" in http://*|https://*|mailto:*) continue ;; esac
    [ -e "$dir/$target" ] || err "$f links to missing $target"
  done < <(grep -oE '\]\([^) ]+\)' "$f" | sed -E 's/^\]\(//; s/\)$//') # one grep+sed per file
done < <(list_files '*.md')

exit "$fail"
