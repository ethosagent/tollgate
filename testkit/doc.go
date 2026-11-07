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
// before a cut (Options.FailAfterChunks). PathFor gives the API path a
// fixture's provider serves.
//
// Run is the plugin harness. It drives an Authenticator, Hook, Finalizer or
// MetricSink through a real tollgate.New and a FakeUpstream, one call per
// fixture, and returns a Recording of every call the plugin received, in
// order, with copies of its arguments:
//
//	rec := testkit.Run(t, myHook{}, "openai/chat-stream-text")
//	rec.Of(testkit.StageBefore)[0].Call.Model // what Before was given
//
// RunWith takes a Config for several plugins, request headers and upstream
// faults. NewServer starts a fake upstream without a testing.TB, for tools
// such as cmd/tollgate-bench.
//
// Dependency law (internal/archtest, rule D6): standard library and the root
// tollgate package only. It is public API and follows semver.
package testkit
