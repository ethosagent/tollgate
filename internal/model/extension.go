package model

import (
	"context"
	"net/http"
)

// Authenticator resolves who is calling. An error maps to 401/403 with a
// provider-shaped body. It never sees or stores upstream credentials.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Tenant, error)
}

// Admission decides whether a call may proceed now (429, queue, priority).
// It runs right after Auth. release must be called exactly once.
type Admission interface {
	Acquire(ctx context.Context, t Tenant, c *Call) (release func(), err error)
}

// ProviderResolver picks the upstream for a request and returns the path
// left after the provider prefix.
type ProviderResolver interface {
	Resolve(ctx context.Context, t Tenant, r *http.Request) (p Provider, restPath string, err error)
}

// SessionStrategy attributes a call to a Session and reports sessions that
// went quiet on Closed.
type SessionStrategy interface {
	Resolve(ctx context.Context, t Tenant, c *Call) Session
	Closed() <-chan Session
}

// Hook is an ordered call extension. Every method runs under a panic guard
// and a deadline; a failing hook is logged and skipped. Embed NoopHook and
// override only the methods you need.
type Hook interface {
	// Name identifies the hook in logs and metrics.
	Name() string
	// Before may mutate the call or short-circuit it with a Response.
	Before(ctx context.Context, c *Call) (*Response, error)
	// OnEvent observes the stream off the client path. It cannot stall it.
	OnEvent(ctx context.Context, c *Call, e Event)
	// OnResponse inspects or replaces a buffered stream:false response.
	OnResponse(ctx context.Context, c *Call, r *Response) (*Response, error)
	// After is fire-and-forget once the call is complete.
	After(ctx context.Context, c *Call, m Metric)
	// OnSessionClosed runs when the SessionStrategy closes a session.
	OnSessionClosed(ctx context.Context, s Session)
}

// MetricSink receives batches of metrics. The core batches on a bounded
// queue and never blocks a request on the sink.
type MetricSink interface {
	Write(ctx context.Context, batch []Metric) error
}

// DecisionAction is what a Finalizer wants done with a finished stream.
type DecisionAction int

// Decision actions.
const (
	DecisionRelease DecisionAction = iota // release the terminal event as-is
	DecisionRecover                       // make a recovery call with Body
	DecisionReplace                       // send Response instead
)

// Decision is a Finalizer's verdict.
type Decision struct {
	Action   DecisionAction
	Body     []byte    // DecisionRecover: the new upstream request body
	Response *Response // DecisionReplace
}

// StreamSummary is what a Finalizer sees before the terminal event.
type StreamSummary struct {
	FinishReason string
	Usage        Usage
	Events       int
	ToolCalls    []ToolCall
}

// Finalizer holds the terminal event of a stream and decides whether to
// release it, recover with a new upstream call, or replace the response.
// Bounded by a per-finalizer budget and a global ceiling per call.
type Finalizer interface {
	Name() string
	Decide(ctx context.Context, c *Call, s StreamSummary) Decision
}
