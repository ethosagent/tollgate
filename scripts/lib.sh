# Sourced by scripts/check-*.sh, .claude/hooks/*.sh and skill scripts.
# Shared, deterministic helpers. Caps here are the single source for
# docs/context-budget.md (Context Budget Playbook).
# shellcheck shell=bash

FAST_GATES=(fmt vet arch docs size) # seconds-cheap gates: Stop hook, handoff, --fast
MAX_SKILLS=40                       # project skills visible per session (exclusive)
MAX_SKILL_DESC=250                  # SKILL.md description, characters

# gotool runs a tool pinned in tools/go.mod (ADR 0005). CGO_ENABLED=0: every
# pinned tool is pure Go, so a broken host C toolchain cannot break the gate.
# Do not use it for `go test -race`, which needs cgo.
gotool() { CGO_ENABLED=0 go tool -modfile="$REPO_ROOT/tools/go.mod" "$@"; }

# REPO_ROOT is absolute so gotool also works from inside a nested module.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# nested_modules lists the repo-relative dirs under ext/ and cmd/ that have
# their own go.mod (ADR 0011: extensions with third-party deps; ADR 0010: the
# tollgated binary, which reads YAML). Directories named
# testdata or starting with "." or "_" are skipped, as the go command skips
# them; internal/archtest nestedModuleDirs uses the same rule. A failing find
# fails the caller: it never reads as "no nested modules".
nested_modules() {
  (cd "$REPO_ROOT" && find ext cmd \( -type d \( -name testdata -o -name '.?*' -o -name '_*' \) -prune \) -o -name go.mod -print | sed 's|/go.mod$||' | sort)
}

# each_module CMD... runs CMD in the root module, then in each nested module,
# with the module dir as cwd and MODULE set to its repo-relative path ("." for
# the root). The root `./...` skips nested modules, so every per-module gate
# goes through here. It stops at the first failure and returns non-zero.
each_module() {
  local mods m
  mods="$(set -o pipefail; nested_modules)" || { echo "✗ cannot list nested modules"; return 1; }
  for m in . $mods; do
    (cd "$REPO_ROOT/$m" && MODULE="$m" "$@") || { echo "✗ module $m: $* failed"; return 1; }
  done
}

# cover_gate runs the current module's tests with the race detector and a
# coverage profile, and fails below COVERAGE_MIN (percent). Profiles: root
# .logs/coverage.out, nested .logs/coverage-<slug>.out. Use via each_module.
cover_gate() {
  local prof="$REPO_ROOT/.logs/coverage.out" total
  [ "${MODULE:-.}" = . ] || prof="$REPO_ROOT/.logs/coverage-$(slugify "$MODULE").out"
  go test -race -count=1 -coverprofile="$prof" -covermode=atomic ./... || return 1
  total="$(go tool cover -func="$prof" | awk '/^total:/ {gsub("%","",$3); print $3}')" || return 1
  echo "coverage ${MODULE:-.}: ${total}% (min ${COVERAGE_MIN}%)"
  if [ -z "$total" ] || awk -v t="$total" -v m="$COVERAGE_MIN" 'BEGIN { exit !(t < m) }'; then
    echo "✗ ${MODULE:-.} coverage ${total:-unknown}% is below ${COVERAGE_MIN}%. Add tests; see $prof (go tool cover -func=$prof)."
    return 1
  fi
}

# list_files PATTERN... lists tracked and untracked-but-not-ignored files, so
# .gitignore (plan/, .logs/, reports/, ...) is the only exclusion list.
list_files() { git ls-files --cached --others --exclude-standard -- "$@" | sort -u; }

# size_limit PATH sets HARD and SOFT line caps for a repo-relative path
# (HARD empty = uncapped). First match wins.
size_limit() {
  HARD="" SOFT=""
  case "$1" in
    CLAUDE.md | AGENTS.md)                    HARD=150 ;;
    ARCHITECTURE.md)                          HARD=300 ;;
    docs/decisions/README.md)                 HARD=150 ;;
    docs/decisions/[0-9][0-9][0-9][0-9]-*.md) HARD=120 ;;
    .agents/skills/*/SKILL.md)                HARD=300 ;;
    .agents/skills/*/references/*.md)         HARD=200 ;;
    .claude/rules/*.md)                       HARD=60 ;;
    tools/*)                                  ;; # separate module, not ours to cap
    *_test.go)                                HARD=500 ;;
    *.go)                                     HARD=800 SOFT=300 ;;
    *.md)                                     HARD=200 ;;
  esac
  SOFT="${SOFT:-$HARD}"
}

# size_verdict PATH LINES prints a finding; returns 1 over the hard cap. A soft
# cap only warns ("! ..."), and run-checks.sh keeps warnings visible.
size_verdict() {
  size_limit "$1"
  [ -n "$HARD" ] || return 0
  if [ "$2" -gt "$HARD" ]; then
    echo "✗ $1 has $2 lines (cap $HARD). Split it by idea; see docs/context-budget.md."
    return 1
  fi
  [ "$2" -le "$SOFT" ] || echo "! $1 has $2 lines (soft cap $SOFT). Consider splitting before it grows."
}

# check_size PATH: size_verdict for one existing file (used per edit by the hook).
check_size() { [ -f "$1" ] || return 0; size_verdict "$1" "$(( $(wc -l <"$1") ))"; }

# slugify "Some Title" -> some-title
slugify() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g'; }

# next_num DIR WIDTH -> next zero-padded number after the highest NNN- prefix in DIR.
next_num() {
  local last
  last="$(ls "$1" 2>/dev/null | grep -Eo "^[0-9]{$2}" | sort -n | tail -1)"
  printf "%0${2}d" $((10#${last:-0} + 1))
}

# active_plan -> newest plan/NNN-*.md, or nothing.
active_plan() { ls -t plan/[0-9][0-9][0-9]-*.md 2>/dev/null | head -n 1; }
