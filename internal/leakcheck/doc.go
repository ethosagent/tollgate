// Package leakcheck is a stdlib goroutine-leak check for TestMain, used
// instead of a third-party tool to keep the core dependency-free (ADR 0002).
// It proves the rule "every goroutine started for a call ends with the call"
// (STR-9): after the tests run, no goroutine whose stack mentions the
// module may remain.
//
// It is imported only by _test.go files.
//
// Dependency law (internal/archtest, rule D3): standard library only.
package leakcheck
