#!/usr/bin/env bash
# Gate: every Go file is gofmt-formatted. Fix: gofmt -w .
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
bad="$(gofmt -l . | grep -v '^tools/' || true)"
if [ -n "$bad" ]; then
  echo "✗ not gofmt-formatted (fix: gofmt -w .):"
  echo "$bad"
  exit 1
fi
