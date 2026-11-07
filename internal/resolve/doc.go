// Package resolve is the default ProviderResolver: a path prefix
// (/openai, /anthropic, /openrouter) picks the provider and wire format, and
// an optional upstream-URL header overrides the base URL for self-hosted
// upstreams, subject to tenant policy.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package resolve
