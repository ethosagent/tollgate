package model

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Tenant is who is calling. The Authenticator resolves it; it partitions
// sessions, admission and metrics. The core treats it as opaque.
type Tenant struct {
	ID     string
	Attrs  map[string]string
	Policy *Policy // nil means no per-tenant policy
}

// Policy is per-tenant data that the host decides and the core and
// extensions read. It is data, not code.
type Policy struct {
	// AllowedProviders and AllowedModels are carried for extensions to
	// enforce (empty means all). The core does not check them yet: a Hook or
	// Admission that should restrict a tenant must compare Call.Provider and
	// Call.Model itself.
	AllowedProviders []string
	AllowedModels    []string
	// Overrides is reserved for body fields forced by policy (e.g.
	// "temperature"). The core does not apply it yet; a Hook's Before may.
	Overrides map[string]any
	// Capture is reserved for opt-in request/response capture. Nothing
	// reads it yet: setting it has no effect.
	Capture bool
	// AllowedUpstreams lists the hosts (exact, with or without a port) and CIDR
	// ranges the upstream-override header may target. Empty ignores the header.
	AllowedUpstreams []string
	// PrivacyMode is reserved for a per-tenant override of privacy mode (nil
	// inherits). Nothing reads it yet: tollgate never logs bodies, reasoning
	// or tool payloads for any tenant, so setting it has no effect.
	PrivacyMode *bool
}

// SessionSource says how a call's Session was resolved.
type SessionSource int

// Session sources.
const (
	SessionFromHeader SessionSource = iota + 1
	SessionPredicted
)

// Session is one multi-call task (an agent run).
type Session struct {
	ID        string
	TenantID  string
	Source    SessionSource
	StartedAt time.Time
	LastSeen  time.Time
	Calls     int
}

// WireFormat names an upstream wire protocol.
type WireFormat string

// Wire formats the core normalises.
const (
	WireOpenAIChat        WireFormat = "openai.chat"
	WireAnthropicMessages WireFormat = "anthropic.messages"
	// WireOpenAIResponses is the OpenAI Responses API (POST /v1/responses),
	// the wire Codex and OpenCode speak. A provider keeps WireOpenAIChat; the
	// core selects this wire per request from the upstream path
	// (docs/decisions/0018).
	WireOpenAIResponses WireFormat = "openai.responses"
	// WirePassthrough is any other path: forwarded untouched, metrics only.
	WirePassthrough WireFormat = "passthrough"
)

// Call.Meta keys the Responses wire fills from the request body. Values are
// strings. A key is absent when the request did not set the field.
const (
	// MetaPreviousResponseID is the request's previous_response_id.
	MetaPreviousResponseID = "openai.previous_response_id"
	// MetaReasoningEffort is the request's reasoning.effort.
	MetaReasoningEffort = "openai.reasoning_effort"
	// MetaReasoningSummary is the request's reasoning.summary.
	MetaReasoningSummary = "openai.reasoning_summary"
)

// Provider is an upstream API family, its wire format and base URL.
type Provider struct {
	Name    string
	Wire    WireFormat
	BaseURL *url.URL
}

// Role is a message author in the neutral model.
type Role string

// Roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Message is one turn of the conversation, provider-independent.
type Message struct {
	Role       Role
	Text       string
	Reasoning  string
	ToolCalls  []ToolCall
	ToolCallID string // set on RoleTool messages
}

// Call is one LLM request, normalised. Hooks may mutate Body in Before.
type Call struct {
	ID      string
	Tenant  Tenant
	Session Session
	// SessionHeader is the caller's session header (HeaderNames.Session),
	// trimmed, or "" when absent. The core strips the header itself before
	// forwarding, so this is where a SessionStrategy reads it (ADR 0012).
	// The core sets it before SessionStrategy.Resolve and never changes it.
	SessionHeader string
	Provider      Provider
	// Path is the inbound request path, provider prefix included, e.g.
	// "/openai/v1/chat/completions". Admission sees it before the provider is
	// resolved; the upstream path is derived later and is not stored here.
	Path   string
	Model  string
	Stream bool
	// Choices is how many alternative completions the request asks for
	// (OpenAI "n"); 0 means the provider's default, one. A Finalizer only
	// sees single-choice streams (ADR 0013).
	Choices  int
	Messages []Message
	// Header is the inbound header set with tollgate's own and hop-by-hop
	// headers removed. Upstream credentials are present but must never be logged.
	Header http.Header
	Body   []byte         // raw provider JSON; mutable
	Meta   map[string]any // scratch space shared by extensions for this call
}

// Response is a complete (non-streaming) response, or a short-circuit
// response returned from Hook.Before.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}
