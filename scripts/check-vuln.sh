#!/usr/bin/env bash
# Gate: govulncheck (pinned in tools/go.mod) against the module's call graph.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
gotool govulncheck ./...
for m in $(nested_modules); do (cd "$m" && gotool govulncheck ./...); done
