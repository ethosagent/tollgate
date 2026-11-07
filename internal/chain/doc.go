// Package chain runs the ordered Hook chain and the Finalizer barrier. Every
// call goes through a panic guard and a context deadline (Limits.Hook*); a
// failing hook is logged with tenant and call id and skipped, and the call
// proceeds untouched. OnEvent hooks consume a bounded channel off the client
// path; overflow drops and counts, it never blocks the stream.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package chain
