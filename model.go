package tollgate

import "github.com/ethosagent/tollgate/internal/model"

// The neutral model and extension interfaces are defined in internal/model
// (so the internal engine can use them without an import cycle) and
// re-exported here as aliases. Extension authors import only this package.
// See docs/decisions/0003. Keep this list in step with internal/model:
// TestAliases_CoverModel fails if an exported model name is missing here.

// Neutral model types.
type (
	Tenant        = model.Tenant
	Policy        = model.Policy
	Session       = model.Session
	SessionSource = model.SessionSource
	WireFormat    = model.WireFormat
	Provider      = model.Provider
	Role          = model.Role
	ToolCall      = model.ToolCall
	Message       = model.Message
	Call          = model.Call
	Response      = model.Response
	EventKind     = model.EventKind
	ToolCallDelta = model.ToolCallDelta
	Usage         = model.Usage
	Event         = model.Event
	Finish        = model.Finish
	Metric        = model.Metric
)

// Extension interfaces and decision types.
type (
	Authenticator    = model.Authenticator
	Admission        = model.Admission
	ProviderResolver = model.ProviderResolver
	SessionStrategy  = model.SessionStrategy
	Hook             = model.Hook
	MetricSink       = model.MetricSink
	Finalizer        = model.Finalizer
	Decision         = model.Decision
	DecisionAction   = model.DecisionAction
	StreamSummary    = model.StreamSummary
	StatusError      = model.StatusError
	RetryAfterError  = model.RetryAfterError
	HeaderConsumer   = model.HeaderConsumer
	InlineSafe       = model.InlineSafe
)

// Default implementations.
type (
	NoopHook    = model.NoopHook
	NoAuth      = model.NoAuth
	Unlimited   = model.Unlimited
	DiscardSink = model.DiscardSink
)

// Constants.
const (
	SessionFromHeader = model.SessionFromHeader
	SessionPredicted  = model.SessionPredicted

	WireOpenAIChat        = model.WireOpenAIChat
	WireAnthropicMessages = model.WireAnthropicMessages
	WirePassthrough       = model.WirePassthrough

	RoleSystem    = model.RoleSystem
	RoleUser      = model.RoleUser
	RoleAssistant = model.RoleAssistant
	RoleTool      = model.RoleTool

	EventTextDelta      = model.EventTextDelta
	EventReasoningDelta = model.EventReasoningDelta
	EventToolCallDelta  = model.EventToolCallDelta
	EventUsage          = model.EventUsage
	EventFinish         = model.EventFinish
	EventError          = model.EventError

	FinishOK            = model.FinishOK
	FinishUpstreamError = model.FinishUpstreamError
	FinishClientCancel  = model.FinishClientCancel
	FinishRejected      = model.FinishRejected
	FinishTimeout       = model.FinishTimeout

	DecisionRelease = model.DecisionRelease
	DecisionRecover = model.DecisionRecover
	DecisionReplace = model.DecisionReplace

	DefaultTenantID = model.DefaultTenantID
)
