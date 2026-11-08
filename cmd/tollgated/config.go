package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// fileConfig is the YAML document. Durations are strings ("30s") so that a
// bad one is reported with every other problem instead of stopping the decode.
// The sections map onto real tollgate.Options fields and extensions only.
type fileConfig struct {
	Listen          string                    `yaml:"listen"`
	AdminListen     string                    `yaml:"admin_listen"`
	ShutdownTimeout string                    `yaml:"shutdown_timeout"`
	Auth            *authConfig               `yaml:"auth"`
	Providers       map[string]providerConfig `yaml:"providers"`
	Sessions        *sessionsConfig           `yaml:"sessions"`
	Admission       *admissionConfig          `yaml:"admission"`
	Metrics         *metricsConfig            `yaml:"metrics"`
	Limits          map[string]any            `yaml:"limits"`
}

// authConfig is ext/auth/statickeys: tenant id -> keys.
type authConfig struct {
	Kind   string              `yaml:"kind"`
	Header string              `yaml:"header"`
	Keys   map[string][]string `yaml:"keys"`
}

// providerConfig is one entry of the path-prefix provider table.
type providerConfig struct {
	Upstream string `yaml:"upstream"`
	Wire     string `yaml:"wire"`
}

// sessionsConfig maps onto Limits.SessionTTL, Limits.MaxSessions and
// Headers.Session.
type sessionsConfig struct {
	TTL         string `yaml:"ttl"`
	MaxSessions int    `yaml:"max_sessions"`
	Header      string `yaml:"header"`
}

// admissionConfig is ext/admission/concurrency.
type admissionConfig struct {
	Kind         string         `yaml:"kind"`
	DefaultMax   int            `yaml:"default_max"`
	PerTenant    map[string]int `yaml:"per_tenant"`
	QueueTimeout string         `yaml:"queue_timeout"`
	RetryAfter   string         `yaml:"retry_after"`
}

// metricsConfig picks the MetricSink: "stdout" or "none".
type metricsConfig struct {
	Sink string `yaml:"sink"`
}

// collector gathers every config problem so they are reported at once.
type collector struct{ errs []error }

func (c *collector) addf(format string, a ...any) { c.errs = append(c.errs, fmt.Errorf(format, a...)) }

func (c *collector) add(errs ...error) { c.errs = append(c.errs, errs...) }

// readConfigFile decodes the YAML file at path.
func readConfigFile(path string) (*fileConfig, []error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return &fileConfig{}, []error{fmt.Errorf("config: %w", err)}
	}
	defer func() { _ = f.Close() }()
	return decodeConfig(f)
}

// decodeConfig decodes strictly: an unknown key is an error, and all of them
// are returned together. An empty document is a valid, empty config.
func decodeConfig(r io.Reader) (*fileConfig, []error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	cfg := &fileConfig{}
	err := dec.Decode(cfg)
	if err == nil {
		var extra yaml.Node
		if err2 := dec.Decode(&extra); !errors.Is(err2, io.EOF) {
			return cfg, []error{errors.New("config: more than one YAML document")}
		}
	}
	if err == nil || errors.Is(err, io.EOF) {
		return cfg, nil
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		errs := make([]error, 0, len(te.Errors))
		for _, msg := range te.Errors {
			errs = append(errs, fmt.Errorf("config: %s", msg))
		}
		return cfg, errs
	}
	return cfg, []error{fmt.Errorf("config: %w", err)}
}

// envRef matches ${VAR} and ${VAR:-default}.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// bareRef matches a shell-style $NAME that expand would leave literal. It is
// applied to the text outside ${...} so an env value is never inspected.
var bareRef = regexp.MustCompile(`\$[A-Za-z_]`)

// lookupFunc reads one environment variable (os.LookupEnv in production).
type lookupFunc func(string) (string, bool)

// expand replaces ${VAR} in the string field named field. An unset variable
// without a ":-default" is a config error that names the variable, never a
// value. Only secret-bearing and address-like fields go through expand.
func (c *collector) expand(field, s string, lookup lookupFunc) string {
	if bareRef.MatchString(envRef.ReplaceAllString(s, "")) {
		c.addf("%s: $NAME-style reference is not expanded; use ${NAME}", field)
		return ""
	}
	return envRef.ReplaceAllStringFunc(s, func(ref string) string {
		m := envRef.FindStringSubmatch(ref)
		v, ok := lookup(m[1])
		switch {
		case ok && v != "":
			return v
		case strings.Contains(ref, ":-"):
			return m[2]
		case ok:
			c.addf("%s: environment variable %s is empty", field, m[1])
		default:
			c.addf("%s: environment variable %s is not set", field, m[1])
		}
		return ""
	})
}

// duration parses a Go duration string ("30s"). Empty means unset (0).
func (c *collector) duration(field, s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		c.addf("%s: %q is not a valid non-negative duration (use e.g. \"30s\")", field, s)
		return 0
	}
	return d
}
