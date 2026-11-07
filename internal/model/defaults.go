package model

import (
	"cmp"
	"context"
	"net/http"
)

// NoopHook implements every Hook method as a no-op. Embed it in a hook and
// override what you need. Its Name is "noop"; override Name too.
type NoopHook struct{}

// Name implements Hook.
func (NoopHook) Name() string { return "noop" }

// Before implements Hook.
func (NoopHook) Before(context.Context, *Call) (*Response, error) { return nil, nil }

// OnEvent implements Hook.
func (NoopHook) OnEvent(context.Context, *Call, Event) {}

// OnResponse implements Hook.
func (NoopHook) OnResponse(_ context.Context, _ *Call, r *Response) (*Response, error) {
	return r, nil
}

// After implements Hook.
func (NoopHook) After(context.Context, *Call, Metric) {}

// OnSessionClosed implements Hook.
func (NoopHook) OnSessionClosed(context.Context, Session) {}

// DefaultTenantID is the tenant NoAuth assigns when its TenantID is empty.
const DefaultTenantID = "default"

// NoAuth admits every request as a single tenant.
type NoAuth struct {
	TenantID string // empty means DefaultTenantID
}

// Authenticate implements Authenticator.
func (a NoAuth) Authenticate(context.Context, *http.Request) (Tenant, error) {
	return Tenant{ID: cmp.Or(a.TenantID, DefaultTenantID)}, nil
}

// InlineSafe implements InlineSafe: Authenticate is a field read.
func (NoAuth) InlineSafe() {}

// Unlimited admits every call immediately.
type Unlimited struct{}

// Acquire implements Admission.
func (Unlimited) Acquire(context.Context, Tenant, *Call) (func(), error) {
	return func() {}, nil
}

// InlineSafe implements InlineSafe: Acquire returns at once.
func (Unlimited) InlineSafe() {}

// DiscardSink drops every metric.
type DiscardSink struct{}

// Write implements MetricSink.
func (DiscardSink) Write(context.Context, []Metric) error { return nil }

// Compile-time interface checks.
var (
	_ Hook          = NoopHook{}
	_ Authenticator = NoAuth{}
	_ Admission     = Unlimited{}
	_ InlineSafe    = NoAuth{}
	_ InlineSafe    = Unlimited{}
	_ MetricSink    = DiscardSink{}
)
