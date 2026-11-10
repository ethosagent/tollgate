package model

import "time"

// EventKind classifies one parsed streaming chunk.
type EventKind int

// Event kinds.
const (
	EventTextDelta EventKind = iota + 1
	EventReasoningDelta
	EventToolCallDelta
	EventUsage
	EventFinish
	EventError
)

// ToolCallDelta is an incremental piece of a streamed tool call.
type ToolCallDelta struct {
	// Index is a dense, 0-based ordinal of the tool call within one response,
	// in order of first appearance, identical on every wire. It is not the
	// provider's own index (OpenAI tool_calls index, Anthropic block index).
	Index     int
	ID        string
	Name      string
	Arguments string // partial JSON fragment
}

// Usage is token accounting for a call.
type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ReasoningTokens   int64
	// CacheWriteInputTokens counts prompt tokens written to the provider's
	// prompt cache (Anthropic cache_creation_input_tokens). Anthropic bills
	// these at a premium and excludes them from InputTokens. Zero on the
	// OpenAI wires, which do not report cache writes.
	CacheWriteInputTokens int64 `json:",omitempty"`
}

// Event is one parsed streaming chunk in the neutral model.
type Event struct {
	Kind         EventKind
	Text         string         // EventTextDelta, EventReasoningDelta
	ToolCall     *ToolCallDelta // EventToolCallDelta
	Usage        *Usage         // EventUsage
	FinishReason string         // EventFinish
	Err          string         // EventError
	Raw          []byte         // the provider bytes; nil unless requested
}

// Finish is how a call ended, as recorded on its Metric.
type Finish string

// Finish values.
const (
	FinishOK            Finish = "ok"
	FinishUpstreamError Finish = "upstream_error"
	FinishClientCancel  Finish = "client_cancel"
	FinishRejected      Finish = "rejected"
	FinishTimeout       Finish = "timeout"
)

// Metric is the one record emitted per completed call.
type Metric struct {
	CallID    string
	TenantID  string
	SessionID string
	Provider  string
	Model     string
	Stream    bool
	Status    int
	Finish    Finish
	// Dispatched is true once the proxy attempted the upstream request, so a
	// transport failure, timeout or client cancel mid-flight still counts.
	// It is false for a refusal before that point: authentication, admission,
	// the provider resolver, the upstream target check, an unreadable or
	// oversized request body, and a Before hook's short-circuit Response.
	// Finish is unchanged; FinishRejected can be either (a Before hook may
	// also reject), so hosts that must not bill a refusal test Dispatched.
	Dispatched bool
	// ShortCircuited is true when a Before hook answered the call with a
	// Response, and ShortCircuitedBy is that hook's Name. Status and Finish
	// are as before (Finish is FinishRejected when the Response status is
	// 400 or more); the upstream was not called, so Dispatched is false.
	ShortCircuited   bool
	ShortCircuitedBy string
	Usage            Usage
	Start            time.Time
	TTFB             time.Duration
	Duration         time.Duration
}
