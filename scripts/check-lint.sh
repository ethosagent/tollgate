#!/usr/bin/env bash
# Gate: golangci-lint, version pinned in tools/go.mod, config in .golangci.yml.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
gotool golangci-lint run ./...
