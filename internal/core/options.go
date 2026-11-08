package core

import (
	"cmp"
	"log/slog"
	"net/http"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
	"github.com/ethosagent/tollgate/internal/resolve"
)

// The configuration types below mirror tollgate.Options, tollgate.Limits and
// tollgate.HeaderNames field for field; their documentation lives on the
// root types, which hosts read. The root converts Limits and HeaderNames
// with a type conversion (so differing field lists do not compile) and
// copies Options field by field (TestOptions_MirrorCore in the root checks
// that none is missed). ADR 0016.

// Options is the core's copy of tollgate.Options. The zero value is valid.
type Options struct {
	Auth                     model.Authenticator
	Admission                model.Admission
	Providers                model.ProviderResolver
	Sessions                 model.SessionStrategy
	Metrics                  model.MetricSink
	Finalizers               []model.Finalizer
	Transport                http.RoundTripper
	Logger                   *slog.Logger
	Limits                   Limits
	Headers                  HeaderNames
	MeasureSessionPrediction bool
	DisablePrivacyMode       bool
}

// Limits is the core's copy of tollgate.Limits: same fields, same order.
type Limits struct {
	MaxRequestBody        int64
	MaxResponseCopy       int64
	ConnectTimeout        time.Duration
	FirstByteTimeout      time.Duration
	IdleStreamTimeout     time.Duration
	MaxCallDuration       time.Duration
	HookBeforeDeadline    time.Duration
	HookChainDeadline     time.Duration
	ExtensionDeadline     time.Duration
	AdmissionDeadline     time.Duration
	FinalizerDeadline     time.Duration
	FinalizerCallDeadline time.Duration
	FinalizerBudget       int
	RecoveryCeiling       int
	EventBuffer           int
	MetricQueue           int
	MetricBatch           int
	MetricFlushInterval   time.Duration
	MetricWriteTimeout    time.Duration
	MetricFailAfter       int
	SessionTTL            time.Duration
	MaxSessions           int
	MaxConnsPerHost       int
	MaxIdleConnsPerHost   int
	IdleConnTimeout       time.Duration
}

// DefaultLimits returns the limits a zero Limits field falls back to. These
// values are the documented defaults: the root tollgate.DefaultLimits returns
// them and TestDefaultLimits_MatchDocumentedValues pins them.
func DefaultLimits() Limits {
	return Limits{
		MaxRequestBody:        16 << 20,
		MaxResponseCopy:       1 << 20,
		ConnectTimeout:        10 * time.Second,
		FirstByteTimeout:      60 * time.Second,
		IdleStreamTimeout:     120 * time.Second,
		MaxCallDuration:       15 * time.Minute,
		HookBeforeDeadline:    50 * time.Millisecond,
		HookChainDeadline:     200 * time.Millisecond,
		ExtensionDeadline:     5 * time.Second,
		AdmissionDeadline:     30 * time.Second,
		FinalizerDeadline:     200 * time.Millisecond,
		FinalizerCallDeadline: time.Second,
		FinalizerBudget:       2,
		RecoveryCeiling:       10,
		EventBuffer:           1024,
		MetricQueue:           10_000,
		MetricBatch:           500,
		MetricFlushInterval:   time.Second,
		MetricWriteTimeout:    10 * time.Second,
		MetricFailAfter:       3,
		SessionTTL:            5 * time.Minute,
		MaxSessions:           100_000,
		MaxConnsPerHost:       100,
		MaxIdleConnsPerHost:   100, // = MaxConnsPerHost: no churn below the connection cap
		IdleConnTimeout:       30 * time.Second,
	}
}

// HeaderNames is the core's copy of tollgate.HeaderNames.
type HeaderNames struct {
	TenantKey   string
	Session     string
	UpstreamURL string
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
		o.Auth = model.NoAuth{}
	}
	if o.Admission == nil {
		o.Admission = model.Unlimited{}
	}
	if o.Metrics == nil {
		o.Metrics = model.DiscardSink{}
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
	l.MaxResponseCopy = cmp.Or(l.MaxResponseCopy, d.MaxResponseCopy)
	l.ConnectTimeout = cmp.Or(l.ConnectTimeout, d.ConnectTimeout)
	l.FirstByteTimeout = cmp.Or(l.FirstByteTimeout, d.FirstByteTimeout)
	l.IdleStreamTimeout = cmp.Or(l.IdleStreamTimeout, d.IdleStreamTimeout)
	l.MaxCallDuration = cmp.Or(l.MaxCallDuration, d.MaxCallDuration)
	l.HookBeforeDeadline = cmp.Or(l.HookBeforeDeadline, d.HookBeforeDeadline)
	l.HookChainDeadline = cmp.Or(l.HookChainDeadline, d.HookChainDeadline)
	l.ExtensionDeadline = cmp.Or(l.ExtensionDeadline, d.ExtensionDeadline)
	l.AdmissionDeadline = cmp.Or(l.AdmissionDeadline, d.AdmissionDeadline)
	l.FinalizerDeadline = cmp.Or(l.FinalizerDeadline, d.FinalizerDeadline)
	l.FinalizerCallDeadline = cmp.Or(l.FinalizerCallDeadline, d.FinalizerCallDeadline)
	l.FinalizerBudget = cmp.Or(l.FinalizerBudget, d.FinalizerBudget)
	l.RecoveryCeiling = cmp.Or(l.RecoveryCeiling, d.RecoveryCeiling)
	l.EventBuffer = cmp.Or(l.EventBuffer, d.EventBuffer)
	l.MetricQueue = cmp.Or(l.MetricQueue, d.MetricQueue)
	l.MetricBatch = cmp.Or(l.MetricBatch, d.MetricBatch)
	l.MetricFlushInterval = cmp.Or(l.MetricFlushInterval, d.MetricFlushInterval)
	l.MetricWriteTimeout = cmp.Or(l.MetricWriteTimeout, d.MetricWriteTimeout)
	l.MetricFailAfter = cmp.Or(l.MetricFailAfter, d.MetricFailAfter)
	l.SessionTTL = cmp.Or(l.SessionTTL, d.SessionTTL)
	l.MaxSessions = cmp.Or(l.MaxSessions, d.MaxSessions)
	l.MaxConnsPerHost = cmp.Or(l.MaxConnsPerHost, d.MaxConnsPerHost)
	l.MaxIdleConnsPerHost = cmp.Or(l.MaxIdleConnsPerHost, l.MaxConnsPerHost)
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
