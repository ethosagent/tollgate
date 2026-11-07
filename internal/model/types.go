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
	AllowedProviders []string       // empty means all
	AllowedModels    []string       // empty means all
	Overrides        map[string]any // body fields forced by policy (e.g. "temperature")
	Capture          bool           // opt-in request/response capture
	// AllowedUpstreams lists the hosts (exact, with or without a port) and CIDR
	// ranges the upstream-override header may target. Empty ignores the header.
	AllowedUpstreams []string
	// PrivacyMode overrides the proxy-wide setting for this tenant. nil inherits.
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
	// WirePassthrough is any other path: forwarded untouched, metrics only.
	WirePassthrough WireFormat = "passthrough"
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
	ID       string
	Tenant   Tenant
	Session  Session
	Provider Provider
	// Path is the inbound request path, provider prefix included, e.g.
	// "/openai/v1/chat/completions". Admission sees it before the provider is
	// resolved; the upstream path is derived later and is not stored here.
	Path     string
	Model    string
	Stream   bool
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
