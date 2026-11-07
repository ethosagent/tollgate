#!/usr/bin/env bash
# Gate: govulncheck (pinned in tools/go.mod) against the module's call graph.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
gotool govulncheck ./...
