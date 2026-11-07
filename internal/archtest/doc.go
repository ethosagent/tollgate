// Package archtest enforces ARCHITECTURE.md's dependency law (rules D1-D7)
// and P1 (every package has a doc comment: the Go form of a package README)
// against every package in the module. It has no production code; the rules
// live in archtest_test.go and run as stage "arch" of scripts/run-checks.sh.
//
// This package is a protected path (.agents/risk.yaml, tier gates): an agent
// must not loosen a rule or add to the third-party allowlist without an ADR
// and a maintainer's approval.
package archtest
