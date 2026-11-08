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

case "$tag" in */*) dir="${tag%/*}" ver="${tag##*/}" ;; *) dir=. ver="$tag" ;; esac
[[ "$ver" =~ $SEMVER ]] || { echo "✗ $tag: version $ver is not semver vX.Y.Z"; exit 1; }
if [ "$dir" != . ] && ! nested_modules | grep -qx "$dir"; then
  echo "✗ $tag: $dir is not a nested module (known: $(nested_modules | tr '\n' ' '))"; exit 1
fi

json="$(GOWORK=off go mod edit -json "$dir/go.mod")" || { echo "✗ cannot read $dir/go.mod"; exit 1; }
field() { python3 -I -c "$1" <<<"$json"; } # tiny JSON queries over go mod edit -json

want_path="$ROOT_MODULE"; [ "$dir" = . ] || want_path="$ROOT_MODULE/$dir"
path="$(field 'import json,sys; print(json.load(sys.stdin)["Module"]["Path"])')"
[ "$path" = "$want_path" ] || err "$dir/go.mod declares module $path; tag $tag needs $want_path"

replaces="$(field 'import json,sys; [print(r["Old"]["Path"], "=>", r["New"]["Path"]) for r in json.load(sys.stdin).get("Replace") or []]')"
[ -z "$replaces" ] || err "$dir/go.mod has replace directives; \`go install\` refuses them and library users ignore them. Drop them (go.work covers local dev):
$(sed 's/^/    /' <<<"$replaces")"

if [ "$dir" != . ]; then
  root_ver="${2:-$(git tag --merged HEAD --list 'v*' --sort=-v:refname | grep -E "$SEMVER" | head -n 1)}"
  [ -n "$root_ver" ] || err "no root release tag (v*) is reachable from HEAD: tag and push the root first (docs/runbooks/release.md)"
  req="$(field "import json,sys; print(next((r['Version'] for r in json.load(sys.stdin).get('Require') or [] if r['Path']=='$ROOT_MODULE'), ''))")"
  if [ -z "$req" ]; then
    err "$dir/go.mod does not require $ROOT_MODULE"
  elif [ -n "$root_ver" ] && [ "$req" != "$root_ver" ]; then
    err "$dir/go.mod requires $ROOT_MODULE $req; the released root is $root_ver. Fix: (cd $dir && go get $ROOT_MODULE@$root_ver && go mod tidy)"
  fi
fi

if [ "$fail" -eq 0 ]; then
  # Build exactly what a consumer resolves: no workspace, published root.
  (cd "$dir" && GOWORK=off go build ./...) ||
    err "$dir does not build with GOWORK=off against its required versions: it uses root code newer than the required release"
fi

[ "$fail" -eq 0 ] || { echo "✗ $tag is not releasable (ADR 0017, docs/runbooks/release.md)"; exit 1; }
echo "✓ $tag is releasable"
