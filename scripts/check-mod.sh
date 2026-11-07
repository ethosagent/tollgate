#!/usr/bin/env bash
# Gate: go.mod/go.sum are tidy (root and tools modules), and the library module
# has no requirements at all (ADR 0002: stdlib-only core).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
go mod tidy -diff || { echo "✗ root module not tidy (fix: go mod tidy)"; exit 1; }
(cd tools && go mod tidy -diff) || { echo "✗ tools module not tidy (fix: cd tools && go mod tidy)"; exit 1; }
if grep -qE '^(require|\s+[a-z0-9.-]+\.[a-z]+/)' go.mod; then
  echo "✗ go.mod has requirements; the library is stdlib-only (ADR 0002). A dependency needs a new ADR and an archtest allowlist entry."
  exit 1
fi
