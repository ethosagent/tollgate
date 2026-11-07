package resolve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func tenantAllowing(entries ...string) model.Tenant {
	return model.Tenant{ID: "t", Policy: &model.Policy{AllowedUpstreams: entries}}
}

func TestResolver_PrefixTable(t *testing.T) {
	r := New(nil, "x-upstream-url")
	tests := []struct {
		path, wantName, wantRest, wantBase string
		wantWire                           model.WireFormat
	}{
		{"/openai/v1/chat/completions", "openai", "/v1/chat/completions", "https://api.openai.com", model.WireOpenAIChat},
		{"/anthropic/v1/messages", "anthropic", "/v1/messages", "https://api.anthropic.com", model.WireAnthropicMessages},
		{"/openrouter/v1/models", "openrouter", "/v1/models", "https://openrouter.ai/api", model.WireOpenAIChat},
		{"/openai", "openai", "/", "https://api.openai.com", model.WireOpenAIChat},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			p, rest, err := r.Resolve(context.Background(), model.Tenant{}, httptest.NewRequest(http.MethodPost, tt.path, nil))
			if err != nil || p.Name != tt.wantName || rest != tt.wantRest || p.BaseURL.String() != tt.wantBase || p.Wire != tt.wantWire {
				t.Fatalf("got %+v %q %v", p, rest, err)
			}
		})
	}
}

func TestResolver_UnknownProvider(t *testing.T) {
	r := New(nil, "x-upstream-url")
	for _, path := range []string{"/nope/v1/x", "/", "/openaix/v1"} {
		_, _, err := r.Resolve(context.Background(), model.Tenant{}, httptest.NewRequest(http.MethodGet, path, nil))
		if !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("%s: err = %v, want ErrUnknownProvider", path, err)
		}
	}
}

func TestResolver_UpstreamOverride(t *testing.T) {
	tests := []struct {
		name     string
		tenant   model.Tenant
		header   string
		wantBase string
		wantErr  error
	}{
		{"no policy ignores header", model.Tenant{}, "http://evil.example", "https://api.openai.com", nil},
		{"empty allowlist ignores header", tenantAllowing(), "http://evil.example", "https://api.openai.com", nil},
		{"no header uses default", tenantAllowing("vllm.internal"), "", "https://api.openai.com", nil},
		{"exact host", tenantAllowing("vllm.internal"), "http://vllm.internal/base", "http://vllm.internal/base", nil},
		{"exact host case-insensitive", tenantAllowing("VLLM.internal"), "https://vllm.INTERNAL", "https://vllm.INTERNAL", nil},
		{"exact host:port", tenantAllowing("vllm.internal:8000"), "http://vllm.internal:8000", "http://vllm.internal:8000", nil},
		{"port mismatch with host:port entry", tenantAllowing("vllm.internal:8000"), "http://vllm.internal:9", "", ErrUpstreamForbidden},
		{"bare host allows any port", tenantAllowing("vllm.internal"), "http://vllm.internal:9", "http://vllm.internal:9", nil},
		{"cidr match", tenantAllowing("10.0.0.0/8"), "http://10.1.2.3:8000", "http://10.1.2.3:8000", nil},
		{"cidr miss", tenantAllowing("10.0.0.0/8"), "http://11.1.2.3", "", ErrUpstreamForbidden},
		{"cidr does not match hostnames", tenantAllowing("10.0.0.0/8"), "http://vllm.internal", "", ErrUpstreamForbidden},
		{"host mismatch", tenantAllowing("vllm.internal"), "http://evil.example", "", ErrUpstreamForbidden},
		{"suffix is not a match", tenantAllowing("internal"), "http://vllm.internal", "", ErrUpstreamForbidden},
		{"bad scheme", tenantAllowing("vllm.internal"), "file://vllm.internal", "", ErrBadUpstream},
		{"credentials in URL", tenantAllowing("vllm.internal"), "http://u:p@vllm.internal", "", ErrBadUpstream},
		{"garbage", tenantAllowing("vllm.internal"), "::not a url", "", ErrBadUpstream},
	}
	r := New(nil, "x-upstream-url")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
			if tt.header != "" {
				req.Header.Set("x-upstream-url", tt.header)
			}
			p, _, err := r.Resolve(context.Background(), tt.tenant, req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && p.BaseURL.String() != tt.wantBase {
				t.Fatalf("base = %s, want %s", p.BaseURL, tt.wantBase)
			}
		})
	}
}

func TestResolver_HeaderDisabled(t *testing.T) {
	r := New(nil, "")
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/x", nil)
	req.Header.Set("x-upstream-url", "http://vllm.internal")
	p, _, err := r.Resolve(context.Background(), tenantAllowing("vllm.internal"), req)
	if err != nil || p.BaseURL.Host != "api.openai.com" {
		t.Fatalf("got %v %v", p.BaseURL, err)
	}
}
