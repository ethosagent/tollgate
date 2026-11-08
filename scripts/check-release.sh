#!/usr/bin/env bash
# Release guard (ADR 0017). Run it BEFORE pushing a tag; release.yml runs it
# again on the pushed tag. Not a run-checks.sh gate: between releases the
# nested modules may legitimately carry a dev `replace`.
#
#   scripts/check-release.sh <tag> [root-version]
#
#   <tag>           vX.Y.Z (the root module) or <dir>/vX.Y.Z for a nested
#                   module, e.g. cmd/tollgated/v0.1.0, ext/metrics/postgres/v0.1.0
#   [root-version]  the root release a nested module must require; default: the
#                   newest root tag (v*) reachable from HEAD
#
# Fails when the module being released has a replace directive, or (nested)
# requires a root version other than the released one, or does not build with
# GOWORK=off against that published root (what `go install ...@<tag>` and a
# library user resolve). The build step needs network access to the proxy.
# This file is a protected path (.agents/risk.yaml).
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
ROOT_MODULE=github.com/ethosagent/tollgate
SEMVER='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo "usage: scripts/check-release.sh <tag> [root-version]" >&2; exit 2; }
tag="$1" fail=0
err() { echo "✗ $*"; fail=1; }
die() { echo "✗ $*"; exit 1; }

case "$tag" in */*) dir="${tag%/*}" ver="${tag##*/}" ;; *) dir=. ver="$tag" ;; esac
[[ "$ver" =~ $SEMVER ]] || die "$tag: version $ver is not semver vX.Y.Z"
if [ "$dir" != . ] && ! nested_modules | grep -qx "$dir"; then
  die "$tag: $dir is not a nested module (known: $(nested_modules | tr '\n' ' '))"
fi

json="$(GOWORK=off go mod edit -json "$dir/go.mod")" || die "cannot read $dir/go.mod"
# One query over go mod edit -json. Line 1: module path; line 2: the version of
# $ROOT_MODULE it requires (empty if none); then one "old => new" per replace.
facts=(); while IFS= read -r line; do facts+=("$line"); done < <(python3 -I -c '
import json, sys
m = json.load(sys.stdin)
print(m["Module"]["Path"])
print(next((r["Version"] for r in m.get("Require") or [] if r["Path"] == sys.argv[1]), ""))
for r in m.get("Replace") or []:
    print(r["Old"]["Path"], "=>", r["New"]["Path"])
' "$ROOT_MODULE" <<<"$json")
[ "${#facts[@]}" -ge 2 ] || die "cannot parse $dir/go.mod"
path="${facts[0]-}" req="${facts[1]-}" replaces=""
[ "${#facts[@]}" -le 2 ] || replaces="$(printf '%s\n' "${facts[@]:2}")"

want_path="$ROOT_MODULE"; [ "$dir" = . ] || want_path="$ROOT_MODULE/$dir"
[ "$path" = "$want_path" ] || err "$dir/go.mod declares module $path; tag $tag needs $want_path"

[ -z "$replaces" ] || err "$dir/go.mod has replace directives; \`go install\` refuses them and library users ignore them. Drop them (go.work covers local dev):
$(sed 's/^/    /' <<<"$replaces")"

if [ "$dir" != . ]; then
  root_ver="${2:-$(git tag --merged HEAD --list 'v*' --sort=-v:refname | grep -E "$SEMVER" | head -n 1)}"
  [ -n "$root_ver" ] || err "no root release tag (v*) is reachable from HEAD: tag and push the root first (docs/runbooks/release.md)"
  if [ -z "$req" ]; then
    err "$dir/go.mod does not require $ROOT_MODULE"
  elif [ -n "$root_ver" ] && [ "$req" != "$root_ver" ]; then
    err "$dir/go.mod requires $ROOT_MODULE $req; the released root is $root_ver. Fix: (cd $dir && go get $ROOT_MODULE@$root_ver && go mod tidy)"
  fi
fi

if [ "$fail" -eq 0 ]; then
  # Build exactly what a consumer resolves: no workspace, published root.
  (cd "$dir" && GOWORK=off go build -o /dev/null ./...) ||
    err "$dir does not build with GOWORK=off against its required versions: it uses root code newer than the required release"
fi

[ "$fail" -eq 0 ] || die "$tag is not releasable (ADR 0017, docs/runbooks/release.md)"
echo "✓ $tag is releasable"
