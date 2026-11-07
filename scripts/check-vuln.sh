#!/usr/bin/env bash
# Gate: govulncheck (pinned in tools/go.mod) against each module's call graph.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
each_module gotool govulncheck ./...
