// Package chain runs the ordered Hook chain and guards every extension call.
// A Chain is immutable: Use builds a new one, and it detects once which Hook
// methods each hook overrides (detect.go), so a stage no hook implements costs
// nothing. One stage runner (stage.go) applies the same policy everywhere:
// recover() plus a context deadline (Limits), a private deep copy of what a
// hook may write (clone.go), and a failing hook logged with tenant and call id
// (metadata only) and skipped. OnEvent hooks consume a bounded channel off the
// client path; overflow drops and counts, it never blocks the stream.
// Within/WithinLate also bound the Auth, Admission and resolver seams.
// Finalizers (finalize.go) are asked in order under the same guard, with
// per-finalizer and per-call recovery budgets (a Barrier per call).
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package chain
