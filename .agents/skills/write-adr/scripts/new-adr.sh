#!/usr/bin/env bash
# new-adr.sh "Short title" — create docs/decisions/NNNN-short-title.md from the
# template with the next number, today's date, and add its row to the index.
# Prints the new path. Deterministic: no judgement needed for this part.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
. scripts/lib.sh
title="${1:?usage: new-adr.sh \"Short title\"}"
dir=docs/decisions
num="$(next_num "$dir" 4)"
slug="$(slugify "$title")"
path="$dir/$num-$slug.md"
[ ! -e "$path" ] || { echo "exists: $path" >&2; exit 1; }
sed -e "s/^# NNNN\. Title/# $num. $title/" \
    -e "s/YYYY-MM-DD/$(date +%F)/" \
    -e "s/Proposed | Accepted | Superseded by NNNN/Proposed/" "$dir/template.md" >"$path"
# insert the index row after the last ADR row
awk -v row="| [$num]($num-$slug.md) | $title | Proposed |" '
  { lines[NR]=$0 } /^\| \[[0-9]{4}\]/ { last=NR }
  END { for (i=1;i<=NR;i++) { print lines[i]; if (i==last) print row } }' "$dir/README.md" >"$dir/README.md.tmp"
mv "$dir/README.md.tmp" "$dir/README.md"
echo "$path"
