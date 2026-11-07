#!/usr/bin/env bash
# Point git at .githooks. Run once after cloning.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
git config core.hooksPath .githooks
chmod +x .githooks/* scripts/*.sh .claude/hooks/*.sh
echo "git hooks installed: pre-commit (fmt vet docs), pre-push (scripts/run-checks.sh)"
