# Test quality (`TEST-*`)

## Already covered by CI

- `go test -race`, coverage floor (`scripts/check-test.sh`), archtest.

## Checks

**TEST-1 The test fails without the change.** For a fix, the test reproduces
the bug. For a feature, at least one assertion would fail on the old code.

**TEST-2 Behaviour at the boundary.** Assert HTTP status, body shape, emitted
metric or calls received by a hook, not private fields, unless they are the
contract.

**TEST-3 Failure paths are tested.** Every new error branch, timeout and
overflow path has a case. Streaming code tests mid-stream upstream error,
client cancel and slow consumer.

**TEST-4 No network, no real keys, no sleeps for ordering.** Use `httptest`
or `testkit`. Wait with a deadline-bounded poll, never a bare `time.Sleep`.

**TEST-5 Table-driven and named.** Cases read as behaviour; adding a case is one row.

**TEST-6 No weakened tests.** No deleted assertion, skipped test, loosened
tolerance or lowered coverage floor to make a change pass.

**TEST-7 Deterministic.** No dependence on map order, wall-clock time or
goroutine scheduling; inject clocks where time matters.
