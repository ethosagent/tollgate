# 0014. Fixtures in a leaf package; testkit's harness imports the root

- **Status:** Accepted
- **Date:** 2026-11-08
- **Source:** review and simplify of milestone 011 (testkit.Run, tollgate-bench)

## Context

Milestone 011 made `testkit` import the root package: `testkit.Run` must
build a real `tollgate.New`. The root imports `internal/*`, so an in-package
test of any `internal/*` package could no longer import `testkit` (an
import cycle), contradicting ARCHITECTURE.md §3, and `internal/normalise`
grew a hand-copied fixture reader over `../../testkit/testdata`. Separately,
`cmd/tollgate-bench` imports `testkit` in non-test code, while §3 said
production code never does, and archtest did not enforce that sentence.

Options: (a) move the fixtures into a standard-library-only leaf,
`internal/fixtures`, that both testkit and internal tests import; (b) a
`testkit/fixtures` sub-package (internal tests would import a testkit path);
(c) rewrite internal tests as external `_test` packages (they need
unexported helpers). For the bench: (i) let it import `internal/fixtures`
and own a fake server (needs a D5 carve-out into `internal/`); (ii) keep it
on `testkit`, the public API, and name it a developer tool.

## Decision

(a) and (ii). `internal/fixtures` embeds `testdata/` and owns names,
chunking, status, content type, paths and matching request bodies;
`testkit`'s fixture functions delegate to it, unchanged for callers. Layer
`fixtures` may import only the standard library; testkit may import it
(D6). `cmd/tollgate-bench` is layer `tool`: the ext/cmd law plus `testkit`.
Every other ext/cmd package is now forbidden to import `testkit` outside
tests (D5), which archtest did not check before.

## Consequences

- One copy of the fixture logic; internal tests read it without a path
  relative to another package.
- New fixtures go in `internal/fixtures/testdata/<provider>/`.
- The bench stays on the public API, so it measures what a host sees.
- Enforced by `internal/archtest` (`TestArchitecture`,
  `TestCheck_RejectsKnownBadEdges`).
- Amended 2026-11-08 (milestone 013 cleanup): a second standard-library-only
  leaf, `internal/methodset` (layer `leaf`), holds the "own method or
  promoted from NoopHook" rule that `internal/chain` and testkit's harness
  both need, so the two can no longer drift. testkit may import it (D6);
  it may import nothing but the standard library.
