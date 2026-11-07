#!/usr/bin/env bash
# Gate: all tests with the race detector, plus a total coverage floor.
#   COVERAGE_MIN  minimum total statement coverage, percent (default 75)
# Never lower COVERAGE_MIN to go green: add tests (protected path).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
COVERAGE_MIN="${COVERAGE_MIN:-75}"
mkdir -p .logs
go test -race -count=1 -coverprofile=.logs/coverage.out -covermode=atomic ./...
total="$(go tool cover -func=.logs/coverage.out | awk '/^total:/ {gsub("%","",$3); print $3}')"
echo "coverage: ${total}% (min ${COVERAGE_MIN}%)"
if awk -v t="$total" -v m="$COVERAGE_MIN" 'BEGIN { exit !(t < m) }'; then
  echo "✗ coverage ${total}% is below ${COVERAGE_MIN}%. Add tests; see .logs/coverage.out (go tool cover -func=.logs/coverage.out)."
  exit 1
fi

# Nested ext/ modules (ADR 0011): same race detector and the same floor, each
# with its own profile. Their integration tests read TOLLGATE_TEST_PG_DSN and
# fail under CI=true when it is unset.
. scripts/lib.sh
for m in $(nested_modules); do
  prof="$PWD/.logs/coverage-$(slugify "$m").out"
  (cd "$m" && go test -race -count=1 -coverprofile="$prof" -covermode=atomic ./...)
  total="$(cd "$m" && go tool cover -func="$prof" | awk '/^total:/ {gsub("%","",$3); print $3}')"
  echo "coverage $m: ${total}% (min ${COVERAGE_MIN}%)"
  if awk -v t="$total" -v m="$COVERAGE_MIN" 'BEGIN { exit !(t < m) }'; then
    echo "✗ $m coverage ${total}% is below ${COVERAGE_MIN}%. Add tests."
    exit 1
  fi
done
