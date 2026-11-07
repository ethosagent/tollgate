// Package fixtures holds the recorded, scrubbed provider responses and
// request bodies (OpenAI chat completions and Anthropic messages, streaming
// and JSON, plus error bodies) that every test replays, embedded from
// testdata/<provider>/<name>.sse|.json.
//
// It is a leaf: package testkit re-exports it as its public fixture API
// (testkit.Fixture, FixtureNames, Chunks, PathFor), and in-package tests of
// internal/* use it directly, since they cannot import testkit (testkit
// imports the root package, which imports them).
//
// Dependency law (internal/archtest, layer "fixtures"): standard library
// only.
package fixtures
