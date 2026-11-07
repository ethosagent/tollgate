package tollgate

import (
	"errors"
	"fmt"
	"strings"
)

// Validate reports every problem with the options after defaults are
// applied. New calls it; hosts may call it to check config early.
func (o Options) Validate() error {
	o = o.withDefaults()
	errs := []error{o.Limits.validate(), o.Headers.validate(), validateConsumed(o.consumedHeaders())}
	if o.Limits.HookBeforeDeadline > o.Limits.HookChainDeadline {
		errs = append(errs, errors.New("limits: HookBeforeDeadline exceeds HookChainDeadline"))
	}
	if o.Limits.FinalizerDeadline > o.Limits.FinalizerCallDeadline {
		errs = append(errs, errors.New("limits: FinalizerDeadline exceeds FinalizerCallDeadline"))
	}
	if o.Limits.FinalizerBudget > o.Limits.RecoveryCeiling {
		errs = append(errs, errors.New("limits: FinalizerBudget exceeds RecoveryCeiling"))
	}
	for i, f := range o.Finalizers {
		if f == nil {
			errs = append(errs, fmt.Errorf("finalizers[%d] is nil", i))
		}
	}
	return errors.Join(errs...) // nil entries are dropped
}

func (l Limits) validate() error {
	var errs []error
	for _, f := range []struct {
		name  string
		value int64
	}{
		{"MaxRequestBody", l.MaxRequestBody},
		{"MaxResponseCopy", l.MaxResponseCopy},
		{"ConnectTimeout", int64(l.ConnectTimeout)},
		{"FirstByteTimeout", int64(l.FirstByteTimeout)},
		{"IdleStreamTimeout", int64(l.IdleStreamTimeout)},
		{"MaxCallDuration", int64(l.MaxCallDuration)},
		{"HookBeforeDeadline", int64(l.HookBeforeDeadline)},
		{"HookChainDeadline", int64(l.HookChainDeadline)},
		{"ExtensionDeadline", int64(l.ExtensionDeadline)},
		{"AdmissionDeadline", int64(l.AdmissionDeadline)},
		{"FinalizerDeadline", int64(l.FinalizerDeadline)},
		{"FinalizerCallDeadline", int64(l.FinalizerCallDeadline)},
		{"FinalizerBudget", int64(l.FinalizerBudget)},
		{"RecoveryCeiling", int64(l.RecoveryCeiling)},
		{"EventBuffer", int64(l.EventBuffer)},
		{"MetricQueue", int64(l.MetricQueue)},
		{"MetricBatch", int64(l.MetricBatch)},
		{"MetricFlushInterval", int64(l.MetricFlushInterval)},
		{"MetricWriteTimeout", int64(l.MetricWriteTimeout)},
		{"MetricFailAfter", int64(l.MetricFailAfter)},
		{"SessionTTL", int64(l.SessionTTL)},
		{"MaxSessions", int64(l.MaxSessions)},
		{"MaxConnsPerHost", int64(l.MaxConnsPerHost)},
		{"MaxIdleConnsPerHost", int64(l.MaxIdleConnsPerHost)},
		{"IdleConnTimeout", int64(l.IdleConnTimeout)},
	} {
		if f.value < 0 {
			errs = append(errs, fmt.Errorf("limits: %s must not be negative", f.name))
		}
	}
	return errors.Join(errs...)
}

func (h HeaderNames) validate() error {
	var errs []error
	seen := map[string]string{}
	for _, f := range []struct{ field, value string }{
		{"TenantKey", h.TenantKey},
		{"Session", h.Session},
		{"UpstreamURL", h.UpstreamURL},
	} {
		key := strings.ToLower(f.value)
		if prev, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf("headers: %s and %s are both %q", prev, f.field, f.value))
		}
		seen[key] = f.field
		if isReservedHeader(key) {
			errs = append(errs, fmt.Errorf("headers: %s %q is reserved for upstream credentials or transport", f.field, f.value))
		}
	}
	return errors.Join(errs...)
}

// consumedHeaders returns the request headers the configured extensions
// claim through HeaderConsumer. The core strips them before forwarding
// (ADR 0009).
func (o Options) consumedHeaders() []string {
	var out []string
	for _, ext := range []any{o.Auth, o.Admission, o.Providers, o.Sessions} {
		if hc, ok := ext.(HeaderConsumer); ok {
			out = append(out, hc.ConsumedHeaders()...)
		}
	}
	return out
}

func validateConsumed(names []string) error {
	var errs []error
	for _, n := range names {
		if strings.TrimSpace(n) == "" || isReservedHeader(strings.ToLower(n)) {
			errs = append(errs, fmt.Errorf("headers: an extension consumes %q, which is empty or reserved", n))
		}
	}
	return errors.Join(errs...)
}

// isReservedHeader reports headers tollgate must never claim, because it
// strips its own headers before forwarding and these must pass through.
func isReservedHeader(lower string) bool {
	switch lower {
	case "authorization", "x-api-key", "content-type", "content-length", "host", "connection":
		return true
	}
	return false
}
