#!/usr/bin/env bash
# Gate: go vet on every package of every module (root and nested ext/).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
each_module go vet ./...
