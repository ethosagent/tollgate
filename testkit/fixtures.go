package testkit

import (
	"github.com/ethosagent/tollgate/internal/fixtures"
)

// The fixtures themselves live in internal/fixtures, a standard-library-only
// leaf that in-package tests of internal/* can import too (they cannot import
// testkit, which imports the root package). This file is their public face.

// ErrUnknownFixture is returned by Fixture for a name with no file.
var ErrUnknownFixture = fixtures.ErrUnknown

// FixtureNames returns every fixture name, sorted. Names containing
// "/request-" are request bodies (inputs); the rest are upstream responses.
func FixtureNames() []string { return fixtures.Names() }

// Fixture returns the raw bytes of a fixture, exactly as replayed.
func Fixture(name string) ([]byte, error) { return fixtures.Bytes(name) }

// Chunks returns a fixture split the way FakeUpstream writes it: one chunk per
// SSE event (each ending in its blank line) for .sse fixtures, the whole body
// for .json fixtures. It returns nil for an unknown fixture.
func Chunks(name string) [][]byte { return fixtures.Chunks(name) }

// PathFor returns the upstream API path a fixture's provider serves:
// "/v1/messages" for "anthropic/..." fixtures, "/v1/chat/completions" for
// the rest. A proxy test prefixes it with the provider, e.g.
// "/anthropic" + PathFor(name).
func PathFor(fixture string) string { return fixtures.PathFor(fixture) }

// RequestFor returns the provider path prefix ("openai" or "anthropic") and a
// request body in that provider's wire format matching fixture: "stream":
// true for an event-stream fixture, false otherwise. Run sends exactly this;
// a tool replaying fixtures through a proxy can too:
//
//	provider, body := testkit.RequestFor(name)
//	url := proxyURL + "/" + provider + testkit.PathFor(name)
func RequestFor(fixture string) (provider string, body []byte) {
	return fixtures.Provider(fixture), fixtures.Request(fixture)
}
