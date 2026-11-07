#!/usr/bin/env bash
# Gate: all tests with the race detector, plus a total coverage floor, in every
# module (root and nested ext/), each with its own profile and the same floor.
#   COVERAGE_MIN  minimum total statement coverage, percent (default 75)
# Never lower COVERAGE_MIN to go green: add tests (protected path).
# Nested modules' integration tests read TOLLGATE_TEST_PG_DSN and fail under
# CI=true when it is unset.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/lib.sh
COVERAGE_MIN="${COVERAGE_MIN:-75}"
mkdir -p .logs
each_module cover_gate
