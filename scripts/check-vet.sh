#!/usr/bin/env bash
# Gate: go vet on every package.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
go vet ./...
for m in $(nested_modules); do (cd "$m" && go vet ./...); done
