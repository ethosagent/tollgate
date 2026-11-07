#!/usr/bin/env bash
# Gate: golangci-lint, version pinned in tools/go.mod, config in .golangci.yml,
# in every module (root and nested ext/) with the one root config.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
each_module gotool golangci-lint run --config "$REPO_ROOT/.golangci.yml" ./...
