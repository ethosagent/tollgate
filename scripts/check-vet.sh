#!/usr/bin/env bash
# Gate: go vet on every package.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
go vet ./...
