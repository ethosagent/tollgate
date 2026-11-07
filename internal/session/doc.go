// Package session is the default SessionStrategy, header-or-predict: use
// the caller's session header when present, otherwise predict from a prefix
// hash (tenant, system prompt, first user message) plus continuation
// heuristics. A TTL sweeper closes quiet sessions and emits them on Closed;
// live sessions are capped (LRU) by Limits.MaxSessions.
//
// Dependency law (internal/archtest, rule D3): standard library and other
// internal packages only.
package session
