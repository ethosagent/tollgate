module github.com/ethosagent/tollgate/cmd/tollgated

go 1.26

require github.com/ethosagent/tollgate v0.0.0

// Development only: build against the checkout. Go ignores replace in
// dependencies, so consumers resolve a published root release (ADR 0011).
replace github.com/ethosagent/tollgate => ../..
