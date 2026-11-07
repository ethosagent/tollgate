package statickeys

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate"
)

func newAuth(t *testing.T, cfg Config) *Auth {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func baseConfig() Config {
	return Config{Keys: map[string]Tenant{
		"key-a": {ID: "a", AllowedProviders: []string{"openai"}, AllowedModels: []string{"gpt-4o"}, AllowedUpstreams: []string{"127.0.0.0/8"}},
		"key-b": {ID: "b"},
		"key-c": {ID: "b"}, // second key for the same tenant (rotation)
	}}
}

func request(header, value string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://proxy/openai/v1/chat/completions", nil)
	if header != "" {
		r.Header.Set(header, value)
	}
	return r
}

func TestAuthenticate(t *testing.T) {
	custom := baseConfig()
	custom.Header = "X-Team-Key"
	tests := []struct {
		name       string
		cfg        Config
		header     string
		value      string
		wantID     string
		wantStatus int
	}{
		{"known key resolves tenant", baseConfig(), "x-proxy-key", "key-a", "a", 0},
		{"second key of same tenant", baseConfig(), "x-proxy-key", "key-c", "b", 0},
		{"custom header", custom, "x-team-key", "key-b", "b", 0},
		{"missing header is 401", baseConfig(), "", "", "", http.StatusUnauthorized},
		{"empty value is 401", baseConfig(), "x-proxy-key", "", "", http.StatusUnauthorized},
		{"unknown key is 403", baseConfig(), "x-proxy-key", "nope", "", http.StatusForbidden},
		{"prefix of a key is 403", baseConfig(), "x-proxy-key", "key-", "", http.StatusForbidden},
		{"default header ignored when custom set", custom, "x-proxy-key", "key-a", "", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAuth(t, tc.cfg)
			got, err := a.Authenticate(context.Background(), request(tc.header, tc.value))
			if tc.wantStatus == 0 {
				if err != nil || got.ID != tc.wantID {
					t.Fatalf("got (%q, %v), want tenant %q", got.ID, err, tc.wantID)
				}
				return
			}
			var se tollgate.StatusError
			if !errors.As(err, &se) || se.HTTPStatus() != tc.wantStatus {
				t.Fatalf("err = %v, want StatusError %d", err, tc.wantStatus)
			}
			if tc.value != "" && strings.Contains(err.Error(), tc.value) {
				t.Fatalf("error leaks the presented key: %v", err)
			}
		})
	}
}

func TestAuthenticate_AttachesPolicy(t *testing.T) {
	a := newAuth(t, baseConfig())
	got, err := a.Authenticate(context.Background(), request("x-proxy-key", "key-a"))
	if err != nil {
		t.Fatal(err)
	}
	p := got.Policy
	if p == nil || p.AllowedProviders[0] != "openai" || p.AllowedModels[0] != "gpt-4o" || p.AllowedUpstreams[0] != "127.0.0.0/8" {
		t.Fatalf("policy = %+v", p)
	}
	// A caller mutating the policy must not change what the next call sees.
	p.AllowedProviders[0] = "evil"
	again, _ := a.Authenticate(context.Background(), request("x-proxy-key", "key-a"))
	if again.Policy.AllowedProviders[0] != "openai" {
		t.Fatal("policy shared between calls")
	}
	if b, _ := a.Authenticate(context.Background(), request("x-proxy-key", "key-b")); b.Policy != nil {
		t.Fatalf("tenant without limits got policy %+v", b.Policy)
	}
}

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"no keys", Config{}, "no keys"},
		{"empty key", Config{Keys: map[string]Tenant{"": {ID: "a"}}}, "empty key"},
		{"empty tenant id", Config{Keys: map[string]Tenant{"k": {}}}, "empty tenant ID"},
		{"invalid header name", Config{Header: "bad header", Keys: map[string]Tenant{"k": {ID: "a"}}}, "header"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "k\"") {
				t.Fatalf("error leaks key: %v", err)
			}
		})
	}
}

func TestNew_DefaultHeaderFromCore(t *testing.T) {
	a := newAuth(t, baseConfig())
	if want := tollgate.DefaultHeaderNames().TenantKey; a.header != want {
		t.Fatalf("header = %q, want %q", a.header, want)
	}
}

func TestAuth_WorksWithTollgate(t *testing.T) {
	a := newAuth(t, baseConfig())
	if _, err := tollgate.New(tollgate.Options{Auth: a}); err != nil {
		t.Fatal(err)
	}
}
