#!/usr/bin/env bash
# Gate: go.mod/go.sum are tidy (root, tools and every nested ext/ module), and
# the library module has no requirements at all (ADR 0002: stdlib-only core;
# nested modules for third-party deps: ADR 0011).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
each_module go mod tidy -diff || { echo "✗ a module is not tidy (fix: cd <module> && go mod tidy)"; exit 1; }
(cd tools && go mod tidy -diff) || { echo "✗ tools module not tidy (fix: cd tools && go mod tidy)"; exit 1; }
if grep -qE '^(require|\s+[a-z0-9.-]+\.[a-z]+/)' go.mod; then
  echo "✗ go.mod has requirements; the library is stdlib-only (ADR 0002). A dependency needs a new ADR and an archtest allowlist entry."
  exit 1
fi
