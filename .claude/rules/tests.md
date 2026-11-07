---
paths:
  - "**/*_test.go"
---

# Test rules

Checklist: `.agents/reviews/test-quality.md` (TEST-*).

- Write the test first and see it fail for the right reason.
- Table-driven with `t.Run`; case names read as behaviour.
- No network, no real keys: `httptest` or `testkit`. No `time.Sleep` for ordering; poll with a deadline.
- Assert at the boundary: status, body shape, metric emitted, hook calls received.
- Cover failure paths: errors, timeouts, overflow, client cancel, mid-stream upstream error.
- Never weaken a test, skip it, or lower `COVERAGE_MIN` to go green.
- Test files stay under 500 lines (size gate); put fixtures in `testdata/`, not inline blobs over 50 lines.
- Run tests quietly: `scripts/quiet.sh go test -race ./pkg` or `scripts/run-checks.sh test`.
