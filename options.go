package tollgate

import (
	"cmp"
	"log/slog"
	"net/http"
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
	// MeasureSessionPrediction turns on the default SessionStrategy's
	// accuracy harness: a call that carries a session header is also
	// predicted, and /statusz counts whether the prediction agreed
	// (sessions.predict_agree / predict_disagree). It costs a second hash
	// and session store per header call, so it is for evaluating the
	// predictor, not for production. Ignored with a host strategy.
	MeasureSessionPrediction bool
	// DisablePrivacyMode turns privacy mode off. Privacy mode (on by default)
	// keeps transcripts, reasoning, tool payloads and raw bytes out of logs.
	DisablePrivacyMode bool
}

// Limits bounds every call. A zero field takes its DefaultLimits value.
type Limits struct {
	MaxRequestBody int64 // bytes, checked before parsing
	// MaxResponseCopy caps the copy of a stream:false response kept to read
	// its usage when no OnResponse hook holds it (the client never waits for
	// the copy). A larger response has no usage in its Metric and is counted
	// (statusz hooks.usage_not_read).
	MaxResponseCopy    int64
	ConnectTimeout     time.Duration // upstream dial
	FirstByteTimeout   time.Duration // time to first upstream byte
	IdleStreamTimeout  time.Duration // no bytes on an open stream
	MaxCallDuration    time.Duration
	HookBeforeDeadline time.Duration // per Hook.Before
	HookChainDeadline  time.Duration // all Hook.Before calls together
	// ExtensionDeadline bounds each Authenticator and ProviderResolver call;
	// past it the call is answered 503 and the late result is dropped.
	ExtensionDeadline time.Duration
	// AdmissionDeadline bounds each Admission.Acquire call, queue wait
	// included, so keep it above the admission's own queue timeout. Past it
	// the call is answered 503 and a slot granted late is released at once.
	AdmissionDeadline time.Duration
	FinalizerDeadline time.Duration // per Finalizer.Decide
	// FinalizerCallDeadline bounds every Decide of one call together,
	// recovery rounds included: the longest a terminal event is held for
	// decisions. Once spent, the barrier releases without asking.
	FinalizerCallDeadline time.Duration
	FinalizerBudget       int           // recovery calls per finalizer per call
	RecoveryCeiling       int           // recovery calls per call, all finalizers
	EventBuffer           int           // events queued for OnEvent hooks
	MetricQueue           int           // metrics queued for the sink
	MetricBatch           int           // largest batch handed to MetricSink.Write (capped at MetricQueue)
	MetricFlushInterval   time.Duration // longest a metric waits before a partial batch is written
	MetricWriteTimeout    time.Duration // deadline of one MetricSink.Write; past it the write counts as failed
	// MetricFailAfter is how many consecutive failed sink writes (error,
	// panic or timeout) make /readyz answer 503; one success makes it ready.
	MetricFailAfter int
	SessionTTL      time.Duration // quiet time before a session closes
	MaxSessions     int           // live sessions kept (LRU beyond)
	MaxConnsPerHost int
	// MaxIdleConnsPerHost is how many idle upstream connections per host are
	// kept for reuse. Zero means MaxConnsPerHost: below it, every burst over
	// the idle cap closes connections it will have to dial (and TLS-handshake)
	// again on the next calls.
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

// DefaultLimits returns the limits a zero Limits field falls back to.
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
