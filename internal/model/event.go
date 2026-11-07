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
	Usage     Usage
	Start     time.Time
	TTFB      time.Duration
	Duration  time.Duration
}
