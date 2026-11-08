package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
)

func envOf(m map[string]string) lookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func prep(t *testing.T, yaml string, env map[string]string) (*prepared, []error) {
	t.Helper()
	pr, errs := prepare(writeConfig(t, yaml), overrides{}, envOf(env), io.Discard, slog.New(slog.DiscardHandler))
	if pr != nil {
		t.Cleanup(func() { _ = pr.proxy.Close(context.Background()) })
	}
	return pr, errs
}

func prepareExample(env map[string]string) (*prepared, []error) {
	return prepare(exampleConfig, overrides{}, envOf(env), io.Discard, slog.New(slog.DiscardHandler))
}

func joined(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString(e.Error() + "\n")
	}
	return b.String()
}

func TestExampleConfig_LoadsAndValidates(t *testing.T) {
	env := map[string]string{"TOLLGATE_KEY_TEAM_A": "ka", "TOLLGATE_KEY_TEAM_B": "kb"}
	pr, errs := prepareExample(env)
	if len(errs) > 0 {
		t.Fatalf("example config invalid: %s", joined(errs))
	}
	t.Cleanup(func() { _ = pr.proxy.Close(context.Background()) })
	if pr.listen != ":8080" || pr.adminListen != "127.0.0.1:9090" || pr.shutdown != 30*time.Second {
		t.Errorf("addresses = %q %q %v", pr.listen, pr.adminListen, pr.shutdown)
	}
	if got := strings.Join(pr.summary.Providers, ","); got != "anthropic,openai" {
		t.Errorf("providers = %s", got)
	}
}

func TestExampleConfig_NeedsItsEnvVars(t *testing.T) {
	_, errs := prepareExample(nil)
	got := joined(errs)
	for _, v := range []string{"TOLLGATE_KEY_TEAM_A", "TOLLGATE_KEY_TEAM_B"} {
		if !strings.Contains(got, v+" is not set") {
			t.Errorf("errors lack unset %s:\n%s", v, got)
		}
	}
	if strings.Contains(got, "OPENAI_UPSTREAM") {
		t.Errorf("a ${VAR:-default} reference must not fail when unset:\n%s", got)
	}
}

func TestPrepare_ConfigErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
		env              map[string]string
	}{
		{"unknown top-level key", "wat: 1\n", "field wat not found", nil},
		{"unknown nested key", "metrics: {sink: stdout, extra: 1}\n", "field extra not found", nil},
		{"wrong type", "listen: [1]\n", "config:", nil},
		{"bad shutdown duration", "shutdown_timeout: 5\nlisten: x\n", "", nil},
		{"negative duration", "sessions: {ttl: -1s}\n", `sessions.ttl: "-1s"`, nil},
		{"empty env value", "auth: {keys: {a: ['${K}']}}\n", "K is empty", map[string]string{"K": ""}},
		{"env default used", "providers: {x: {upstream: '${U:-http://h:1}'}}\n", "", nil},
		{"duplicate key", "auth: {keys: {a: [k], b: [k]}}\n", `same key as tenant "a"`, nil},
		{"empty key", "auth: {keys: {a: ['']}}\n", "key is empty", nil},
		{"unsupported auth", "auth: {kind: jwt}\n", `auth.kind: "jwt"`, nil},
		{"no tenants", "auth: {kind: static-keys}\n", "auth:", nil},
		{"admission missing max", "admission: {kind: concurrency}\n", "admission: concurrency: DefaultMax", nil},
		{"admission kind", "admission: {kind: queue}\n", `admission.kind: "queue"`, nil},
		{"bad queue timeout", "admission: {default_max: 1, queue_timeout: x}\n", `admission.queue_timeout: "x"`, nil},
		{"unknown sink", "metrics: {sink: kafka}\n", `metrics.sink: "kafka"`, nil},
		{"bad upstream", "providers: {x: {upstream: 'ftp://h'}}\n", "providers.x.upstream: must be an http(s) URL", nil},
		{"bad wire", "providers: {x: {upstream: 'http://h', wire: grpc}}\n", `providers.x.wire: "grpc"`, nil},
		{"bad provider name", "providers: {'a/b': {upstream: 'http://h'}}\n", "single non-empty path segment", nil},
		{"negative limit", "limits: {max_sessions: -1}\n", "limits.max_sessions: want a non-negative integer", nil},
		{"session ttl twice", "sessions: {ttl: 1m}\nlimits: {session_ttl: 2m}\n", "both set", nil},
		{"zero shutdown flag-less", "shutdown_timeout: 0s\nlisten: nonsense\n", "listen: \"nonsense\" is not a host:port", nil},
		{"headers collide", "sessions: {header: x-proxy-key}\n", "headers:", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := prep(t, tc.yaml, tc.env)
			if tc.want == "" {
				return // only must not panic
			}
			if !strings.Contains(joined(errs), tc.want) {
				t.Fatalf("errors = %q, want one containing %q", joined(errs), tc.want)
			}
		})
	}
}

func TestPrepare_ValidConfigMapsOntoOptions(t *testing.T) {
	pr, errs := prep(t, `
shutdown_timeout: 7s
auth: {keys: {a: ["${K1}", "${K2}"], b: [kb]}}
sessions: {ttl: 2m, max_sessions: 50, header: x-sess}
admission: {default_max: 3, per_tenant: {a: 1}, queue_timeout: 1s}
limits: {metric_queue: 20, metric_batch: 10, first_byte_timeout: 3s}
`, map[string]string{"K1": "one", "K2": "two"})
	if len(errs) > 0 {
		t.Fatal(joined(errs))
	}
	if pr.shutdown != 7*time.Second {
		t.Errorf("shutdown = %v", pr.shutdown)
	}
	if got := strings.Join(pr.summary.Tenants, ","); got != "a: 2 key(s),b: 1 key(s)" {
		t.Errorf("tenants = %q", got)
	}
	if !strings.HasPrefix(pr.summary.Admission, "concurrency (default 3, 1 per-tenant") {
		t.Errorf("admission = %q", pr.summary.Admission)
	}
}

func TestPathResolver(t *testing.T) {
	c := &collector{}
	r := buildProviders(c, map[string]providerConfig{
		"anthropic": {Upstream: "https://up.example/base"},
		"local":     {Upstream: "http://127.0.0.1:9", Wire: "openai.chat"},
		"other":     {Upstream: "http://127.0.0.1:8"},
	}, envOf(nil))
	if len(c.errs) > 0 {
		t.Fatal(joined(c.errs))
	}
	tests := []struct{ path, name, wire, rest string }{
		{"/anthropic/v1/messages", "anthropic", "anthropic.messages", "/v1/messages"},
		{"/local/v1/x", "local", "openai.chat", "/v1/x"},
		{"/other/a/b", "other", "passthrough", "/a/b"},
		{"/other", "other", "passthrough", "/"},
		{"/other/", "other", "passthrough", "/"},
		{"/other/a/b/c/", "other", "passthrough", "/a/b/c/"},
	}
	for _, tc := range tests {
		p, rest, err := r.Resolve(context.Background(), tollgate.Tenant{}, httptest.NewRequest(http.MethodPost, tc.path, nil))
		if err != nil || p.Name != tc.name || string(p.Wire) != tc.wire || rest != tc.rest {
			t.Errorf("%s: got %+v %q %v", tc.path, p, rest, err)
		}
	}
	for _, path := range []string{"/nope/x", "/otherx/a", "/", "/nope"} {
		if _, _, err := r.Resolve(context.Background(), tollgate.Tenant{}, httptest.NewRequest(http.MethodGet, path, nil)); err == nil {
			t.Errorf("%s: unknown provider must error", path)
		}
	}
	if buildProviders(&collector{}, nil, envOf(nil)) != nil {
		t.Error("no providers section must leave the built-in resolver")
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"MaxRequestBody": "max_request_body", "SessionTTL": "session_ttl", "MetricQueue": "metric_queue",
		"MaxIdleConnsPerHost": "max_idle_conns_per_host", "A": "a",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLimits_EveryFieldIsConfigurable(t *testing.T) {
	var l tollgate.Limits
	v := reflect.ValueOf(&l).Elem()
	raw := map[string]any{"min_nope": 1}
	for i := range v.NumField() {
		if v.Field(i).Type() == reflect.TypeFor[time.Duration]() {
			raw[snake(v.Type().Field(i).Name)] = "1s"
		} else {
			raw[snake(v.Type().Field(i).Name)] = 7
		}
	}
	c := &collector{}
	applyLimits(c, raw, &l)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("limits.%s was not applied", snake(v.Type().Field(i).Name))
		}
	}
	if got := joined(c.errs); got != "limits.min_nope: unknown limit\n" {
		t.Errorf("errs = %q, want only the unknown key", got)
	}
}
