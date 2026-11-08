package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sort"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/ext/admission/concurrency"
	"github.com/ethosagent/tollgate/ext/auth/statickeys"
	"github.com/ethosagent/tollgate/ext/metrics/stdout"
)

// summary is what the startup log prints about the config. It holds names and
// counts only: never a key, DSN or other secret.
type summary struct {
	Tenants   []string // "team-a (2 keys)"
	Providers []string
	Admission string
	Metrics   string
}

// buildOptions maps the config onto tollgate.Options, recording every problem
// in c. out receives the stdout metric sink.
func buildOptions(c *collector, cfg *fileConfig, lookup lookupFunc, out io.Writer) (tollgate.Options, summary) {
	var (
		opts tollgate.Options
		sum  = summary{Admission: "unlimited", Metrics: "none"}
	)
	if cfg.Limits != nil {
		applyLimits(c, cfg.Limits, &opts.Limits)
	}
	applySessions(c, cfg.Sessions, &opts)
	if cfg.Auth != nil {
		opts.Auth, sum.Tenants = buildAuth(c, cfg.Auth, lookup)
	}
	if cfg.Admission != nil {
		opts.Admission, sum.Admission = buildAdmission(c, cfg.Admission)
	}
	opts.Providers = buildProviders(c, cfg.Providers, lookup)
	for name := range cfg.Providers {
		sum.Providers = append(sum.Providers, name)
	}
	sort.Strings(sum.Providers)
	if cfg.Metrics != nil {
		opts.Metrics, sum.Metrics = buildMetrics(c, cfg.Metrics, out)
	}
	return opts, sum
}

func applySessions(c *collector, s *sessionsConfig, opts *tollgate.Options) {
	if s == nil {
		return
	}
	if ttl := c.duration("sessions.ttl", s.TTL); ttl != 0 {
		if opts.Limits.SessionTTL != 0 {
			c.addf("sessions.ttl and limits.session_ttl are both set; use one")
		}
		opts.Limits.SessionTTL = ttl
	}
	if s.MaxSessions != 0 {
		if opts.Limits.MaxSessions != 0 {
			c.addf("sessions.max_sessions and limits.max_sessions are both set; use one")
		}
		opts.Limits.MaxSessions = s.MaxSessions
	}
	opts.Headers.Session = s.Header
}

// buildAuth returns the static-keys authenticator and its redacted summary.
func buildAuth(c *collector, a *authConfig, lookup lookupFunc) (tollgate.Authenticator, []string) {
	if a.Kind != "" && a.Kind != "static-keys" {
		c.addf("auth.kind: %q is not supported (only static-keys)", a.Kind)
		return nil, nil
	}
	ids := make([]string, 0, len(a.Keys))
	for id := range a.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	keys := make(map[string]statickeys.Tenant)
	var sum []string
	for _, id := range ids {
		for i, raw := range a.Keys[id] {
			field := fmt.Sprintf("auth.keys.%s[%d]", id, i)
			k := c.expand(field, raw, lookup)
			switch prev, dup := keys[k]; {
			case k == "":
				c.addf("%s: key is empty", field)
			case dup:
				c.addf("%s: same key as tenant %q", field, prev.ID)
			default:
				keys[k] = statickeys.Tenant{ID: id}
			}
		}
		sum = append(sum, fmt.Sprintf("%s: %d key(s)", id, len(a.Keys[id])))
	}
	auth, err := statickeys.New(statickeys.Config{Header: a.Header, Keys: keys})
	if err != nil {
		c.add(fmt.Errorf("auth: %w", err))
		return nil, sum
	}
	return auth, sum
}

func buildAdmission(c *collector, a *admissionConfig) (tollgate.Admission, string) {
	if a.Kind != "" && a.Kind != "concurrency" {
		c.addf("admission.kind: %q is not supported (only concurrency)", a.Kind)
		return nil, "unlimited"
	}
	lim, err := concurrency.New(concurrency.Config{
		DefaultMax:   a.DefaultMax,
		PerTenant:    a.PerTenant,
		QueueTimeout: c.duration("admission.queue_timeout", a.QueueTimeout),
		RetryAfter:   c.duration("admission.retry_after", a.RetryAfter),
	})
	if err != nil {
		c.add(fmt.Errorf("admission: %w", err))
		return nil, "unlimited"
	}
	return lim, fmt.Sprintf("concurrency (default %d, %d per-tenant overrides)", a.DefaultMax, len(a.PerTenant))
}

func buildMetrics(c *collector, m *metricsConfig, out io.Writer) (tollgate.MetricSink, string) {
	switch m.Sink {
	case "", "none":
		return nil, "none"
	case "stdout":
		return stdout.New(out), "stdout"
	case "postgres":
		c.addf("metrics.sink: postgres needs the ext/metrics/postgres module, which tollgated does not link; use stdout or none, or build your own binary from the library")
	default:
		c.addf("metrics.sink: %q is not supported (stdout or none)", m.Sink)
	}
	return nil, "none"
}

// flatten splits an errors.Join result into its parts.
func flatten(err error) []error {
	if err == nil {
		return nil
	}
	var j interface{ Unwrap() []error }
	if errors.As(err, &j) {
		return j.Unwrap()
	}
	return []error{err}
}

// validAddr reports whether addr is a host:port the listener can parse.
func validAddr(addr string) error {
	_, _, err := net.SplitHostPort(addr)
	return err
}
