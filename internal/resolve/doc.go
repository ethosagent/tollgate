// Package resolve is the default ProviderResolver: a path prefix
// (/openai, /anthropic, /openrouter) picks the provider and wire format.
// The upstream-override header is applied by the core for every resolver
// (internal/forward.Target, docs/decisions/0007), not here.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package resolve
