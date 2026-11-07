// Package testkit lets anyone test tollgate, or an extension for it, without
// a network or real keys.
//
// FakeUpstream is an httptest server that replays recorded, scrubbed provider
// responses (OpenAI chat completions and Anthropic messages, streaming and
// JSON, plus error bodies) byte for byte, with knobs for latency and failure:
//
//	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 10 * time.Millisecond})
//	req.Header.Set(testkit.FixtureHeader, "anthropic/messages-stream-tool-use")
//	// ... send req to up.URL, then inspect up.Requests()
//
// FixtureNames lists every fixture; Fixture returns its bytes and Chunks splits
// it the way the fake writes it, so tests can assert exactly what a client saw
// before a cut (Options.FailAfterChunks).
//
// Planned (plan/011): Run, a harness that drives a plugin through every
// lifecycle stage and records its calls.
//
// Dependency law (internal/archtest, rule D6): standard library and the root
// tollgate package only. It is public API and follows semver.
package testkit
