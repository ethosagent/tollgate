// Package methodset answers, by reflection, where a type's method comes
// from: the type's own code, or a method the compiler promoted from an
// embedded field. internal/chain uses it to offer a Hook only the stages it
// implements itself (not those promoted from NoopHook), testkit uses it so
// its harness offers exactly the same stages, and the root package uses it
// to honour the InlineSafe marker only on the type that declares it
// (ADR 0015).
//
// It runs when an extension is wired (New, Use), never per request.
//
// Dependency law (internal/archtest, layer "leaf", rule D6): standard
// library only, so testkit may import it.
package methodset
