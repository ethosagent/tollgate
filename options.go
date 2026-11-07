package tollgate

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ethosagent/tollgate/internal/resolve"
)

// Options configures a Proxy. The zero value is valid: every nil extension
// gets its default and every zero limit gets the value from DefaultLimits.
type Options struct {
	// Auth resolves the tenant. An error becomes 401, or the error's own
	// 4xx/5xx status when it is a StatusError (ADR 0008). Default: NoAuth{}.
	Auth Authenticator
	// Admission gates each call. An error becomes 429 (or its StatusError
	// status) with a Retry-After header in seconds: from a RetryAfterError,
	// else 1. Default: Unlimited{}.
	Admission Admission
	// Providers picks the upstream. An error becomes 404 (or its StatusError
	// status). Whatever it returns, the core applies the upstream-override
	// header under Policy.AllowedUpstreams and checks the final target
	// (ADR 0007). Default: the path-prefix resolver (/openai, /anthropic,
	// /openrouter).
	Providers  ProviderResolver
	Sessions   SessionStrategy // default: header-or-predict (internal/session)
	Metrics    MetricSink      // default: DiscardSink{}
	Finalizers []Finalizer     // default: none (zero overhead)
	Transport  http.RoundTripper
	Logger     *slog.Logger // default: discards everything
	Limits     Limits
	Headers    HeaderNames
	// DisablePrivacyMode turns privacy mode off. Privacy mode (on by default)
	// keeps transcripts, reasoning, tool payloads and raw bytes out of logs.
	DisablePrivacyMode bool
}

// Limits bounds every call. A zero field takes its DefaultLimits value.
type Limits struct {
	MaxRequestBody      int64         // bytes, checked before parsing
	ConnectTimeout      time.Duration // upstream dial
	FirstByteTimeout    time.Duration // time to first upstream byte
	IdleStreamTimeout   time.Duration // no bytes on an open stream
	MaxCallDuration     time.Duration
	HookBeforeDeadline  time.Duration // per Hook.Before
	HookChainDeadline   time.Duration // all Hook.Before calls together
	FinalizerBudget     int           // recovery calls per finalizer per call
	RecoveryCeiling     int           // recovery calls per call, all finalizers
	EventBuffer         int           // events queued for OnEvent hooks
	MetricQueue         int           // metrics queued for the sink
	SessionTTL          time.Duration // quiet time before a session closes
	MaxSessions         int           // live sessions kept (LRU beyond)
	MaxConnsPerHost     int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// DefaultLimits returns the limits a zero Limits field falls back to.
func DefaultLimits() Limits {
	return Limits{
		MaxRequestBody:      16 << 20,
		ConnectTimeout:      10 * time.Second,
		FirstByteTimeout:    60 * time.Second,
		IdleStreamTimeout:   120 * time.Second,
		MaxCallDuration:     15 * time.Minute,
		HookBeforeDeadline:  50 * time.Millisecond,
		HookChainDeadline:   200 * time.Millisecond,
		FinalizerBudget:     2,
		RecoveryCeiling:     10,
		EventBuffer:         1024,
		MetricQueue:         10_000,
		SessionTTL:          5 * time.Minute,
		MaxSessions:         100_000,
		MaxConnsPerHost:     100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     30 * time.Second,
	}
}

// HeaderNames are the request headers tollgate reads. They are options, not
// constants, so the host picks its own. tollgate strips them before
// forwarding. An empty field takes its DefaultHeaderNames value.
type HeaderNames struct {
	TenantKey   string // credential the Authenticator reads
	Session     string // optional caller-supplied session id
	UpstreamURL string // optional upstream override
}

// DefaultHeaderNames returns the header names used when none are set.
func DefaultHeaderNames() HeaderNames {
	return HeaderNames{
		TenantKey:   "x-proxy-key",
		Session:     "x-session-id",
		UpstreamURL: "x-upstream-url",
	}
}

// withDefaults returns a copy with every zero value replaced by its default.
func (o Options) withDefaults() Options {
	if o.Auth == nil {
		o.Auth = NoAuth{}
	}
	if o.Admission == nil {
		o.Admission = Unlimited{}
	}
	if o.Metrics == nil {
		o.Metrics = DiscardSink{}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	o.Limits = o.Limits.withDefaults()
	o.Headers = o.Headers.withDefaults()
	if o.Providers == nil {
		o.Providers = resolve.New(nil)
	}
	return o
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	l.MaxRequestBody = cmp.Or(l.MaxRequestBody, d.MaxRequestBody)
	l.ConnectTimeout = cmp.Or(l.ConnectTimeout, d.ConnectTimeout)
	l.FirstByteTimeout = cmp.Or(l.FirstByteTimeout, d.FirstByteTimeout)
	l.IdleStreamTimeout = cmp.Or(l.IdleStreamTimeout, d.IdleStreamTimeout)
	l.MaxCallDuration = cmp.Or(l.MaxCallDuration, d.MaxCallDuration)
	l.HookBeforeDeadline = cmp.Or(l.HookBeforeDeadline, d.HookBeforeDeadline)
	l.HookChainDeadline = cmp.Or(l.HookChainDeadline, d.HookChainDeadline)
	l.FinalizerBudget = cmp.Or(l.FinalizerBudget, d.FinalizerBudget)
	l.RecoveryCeiling = cmp.Or(l.RecoveryCeiling, d.RecoveryCeiling)
	l.EventBuffer = cmp.Or(l.EventBuffer, d.EventBuffer)
	l.MetricQueue = cmp.Or(l.MetricQueue, d.MetricQueue)
	l.SessionTTL = cmp.Or(l.SessionTTL, d.SessionTTL)
	l.MaxSessions = cmp.Or(l.MaxSessions, d.MaxSessions)
	l.MaxConnsPerHost = cmp.Or(l.MaxConnsPerHost, d.MaxConnsPerHost)
	l.MaxIdleConnsPerHost = cmp.Or(l.MaxIdleConnsPerHost, d.MaxIdleConnsPerHost)
	l.IdleConnTimeout = cmp.Or(l.IdleConnTimeout, d.IdleConnTimeout)
	return l
}

func (h HeaderNames) withDefaults() HeaderNames {
	d := DefaultHeaderNames()
	h.TenantKey = cmp.Or(h.TenantKey, d.TenantKey)
	h.Session = cmp.Or(h.Session, d.Session)
	h.UpstreamURL = cmp.Or(h.UpstreamURL, d.UpstreamURL)
	return h
}

// Validate reports every problem with the options after defaults are
// applied. New calls it; hosts may call it to check config early.
func (o Options) Validate() error {
	o = o.withDefaults()
	errs := []error{o.Limits.validate(), o.Headers.validate()}
	if o.Limits.HookBeforeDeadline > o.Limits.HookChainDeadline {
		errs = append(errs, errors.New("limits: HookBeforeDeadline exceeds HookChainDeadline"))
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
		{"ConnectTimeout", int64(l.ConnectTimeout)},
		{"FirstByteTimeout", int64(l.FirstByteTimeout)},
		{"IdleStreamTimeout", int64(l.IdleStreamTimeout)},
		{"MaxCallDuration", int64(l.MaxCallDuration)},
		{"HookBeforeDeadline", int64(l.HookBeforeDeadline)},
		{"HookChainDeadline", int64(l.HookChainDeadline)},
		{"FinalizerBudget", int64(l.FinalizerBudget)},
		{"RecoveryCeiling", int64(l.RecoveryCeiling)},
		{"EventBuffer", int64(l.EventBuffer)},
		{"MetricQueue", int64(l.MetricQueue)},
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

// isReservedHeader reports headers tollgate must never claim, because it
// strips its own headers before forwarding and these must pass through.
func isReservedHeader(lower string) bool {
	switch lower {
	case "authorization", "x-api-key", "content-type", "content-length", "host", "connection":
		return true
	}
	return false
}
