package tollgate

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ethosagent/tollgate/internal/core"
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
	// Transport is the upstream round tripper. Default: a pooled transport
	// with compression disabled, so tollgate never adds Accept-Encoding or
	// decodes a body and the client's own Accept-Encoding passes through. A
	// custom *http.Transport must set DisableCompression: true to keep that;
	// otherwise Go adds "Accept-Encoding: gzip" upstream and strips the
	// Content-Encoding it decoded.
	Transport http.RoundTripper
	Logger    *slog.Logger // default: discards everything
	Limits    Limits
	Headers   HeaderNames
	// MeasureSessionPrediction turns on the default SessionStrategy's
	// accuracy harness: a call that carries a session header is also
	// predicted, and /statusz counts whether the prediction agreed
	// (sessions.predict_agree / predict_disagree). It costs a second hash
	// and session store per header call, so it is for evaluating the
	// predictor, not for production. Ignored with a host strategy.
	MeasureSessionPrediction bool
	// DisablePrivacyMode is reserved for turning privacy mode off. Privacy
	// mode keeps transcripts, reasoning, tool payloads and raw bytes out of
	// logs. Nothing reads this field yet: tollgate has no code path that logs
	// those, so privacy mode is always on and setting it has no effect.
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

// DefaultLimits returns the limits a zero Limits field falls back to:
//
//	MaxRequestBody 16 MiB, MaxResponseCopy 1 MiB
//	ConnectTimeout 10s, FirstByteTimeout 60s, IdleStreamTimeout 120s, MaxCallDuration 15m
//	HookBeforeDeadline 50ms, HookChainDeadline 200ms
//	ExtensionDeadline 5s, AdmissionDeadline 30s
//	FinalizerDeadline 200ms, FinalizerCallDeadline 1s, FinalizerBudget 2, RecoveryCeiling 10
//	EventBuffer 1024, MetricQueue 10000, MetricBatch 500, MetricFlushInterval 1s
//	MetricWriteTimeout 10s, MetricFailAfter 3
//	SessionTTL 5m, MaxSessions 100000
//	MaxConnsPerHost 100, MaxIdleConnsPerHost = MaxConnsPerHost, IdleConnTimeout 30s
//
// TestDefaultLimits_MatchDocumentedValues pins these values.
func DefaultLimits() Limits { return Limits(core.DefaultLimits()) }

// HeaderNames are the request headers tollgate reads. They are options, not
// constants, so the host picks its own. tollgate strips them before
// forwarding. An empty field takes its DefaultHeaderNames value.
type HeaderNames struct {
	TenantKey   string // credential the Authenticator reads
	Session     string // optional caller-supplied session id
	UpstreamURL string // optional upstream override
}

// DefaultHeaderNames returns the header names used when none are set.
func DefaultHeaderNames() HeaderNames { return HeaderNames(core.DefaultHeaderNames()) }

// Validate reports every problem with the options after defaults are
// applied. New calls it; hosts may call it to check config early.
func (o Options) Validate() error { return o.core().Validate() }

// core copies o into the engine's mirror of it (ADR 0016). Limits and
// HeaderNames convert directly: their field lists must match core's or this
// does not compile. TestOptions_MirrorCore checks no Options field is missed.
func (o Options) core() core.Options {
	return core.Options{
		Auth:                     o.Auth,
		Admission:                o.Admission,
		Providers:                o.Providers,
		Sessions:                 o.Sessions,
		Metrics:                  o.Metrics,
		Finalizers:               o.Finalizers,
		Transport:                o.Transport,
		Logger:                   o.Logger,
		Limits:                   core.Limits(o.Limits),
		Headers:                  core.HeaderNames(o.Headers),
		MeasureSessionPrediction: o.MeasureSessionPrediction,
		DisablePrivacyMode:       o.DisablePrivacyMode,
	}
}
