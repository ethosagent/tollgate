// Package normalise turns provider wire formats into the neutral model: a
// request body into a Call (model, stream flag, messages, tool calls,
// reasoning) and stream chunks into Events, one EventMapper per WireFormat
// (OpenAI chat completions, OpenAI Responses, Anthropic messages). It also writes
// provider-shaped error bodies. Adding a provider means one file here plus
// fixtures in internal/fixtures/testdata.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package normalise
