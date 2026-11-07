// Package testkit lets anyone test a tollgate extension without a network or
// real keys: a FakeUpstream (httptest server replaying recorded SSE fixtures
// per provider, with knobs for delay, slow chunks and mid-stream errors) and
// a harness that drives a plugin through Auth, Before, OnEvent, Finalizer,
// After and OnSessionClosed and returns what it was called with.
//
// Dependency law (internal/archtest, rule D6): standard library and the root
// tollgate package only. It is public API and follows semver.
package testkit
