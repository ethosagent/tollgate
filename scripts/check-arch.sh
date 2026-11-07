#!/usr/bin/env bash
# Gate: dependency law D1-D7 (ARCHITECTURE.md §3), enforced by internal/archtest.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
go test -count=1 ./internal/archtest
