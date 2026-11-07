package resolve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestResolver_PrefixTable(t *testing.T) {
	r := New(nil)
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
	r := New(nil)
	for _, path := range []string{"/nope/v1/x", "/", "/openaix/v1"} {
		_, _, err := r.Resolve(context.Background(), model.Tenant{}, httptest.NewRequest(http.MethodGet, path, nil))
		if !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("%s: err = %v, want ErrUnknownProvider", path, err)
		}
	}
}

func TestResolver_IgnoresUpstreamHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/x", nil)
	req.Header.Set("x-upstream-url", "http://vllm.internal")
	tenant := model.Tenant{ID: "t", Policy: &model.Policy{AllowedUpstreams: []string{"vllm.internal"}}}
	p, _, err := New(nil).Resolve(context.Background(), tenant, req)
	if err != nil || p.BaseURL.Host != "api.openai.com" {
		t.Fatalf("got %v %v; the override belongs to the core", p.BaseURL, err)
	}
}

func TestWireForPath(t *testing.T) {
	for path, want := range map[string]model.WireFormat{
		"/openai/v1/chat/completions": model.WireOpenAIChat,
		"/anthropic/v1/messages":      model.WireAnthropicMessages,
		"/openrouter/v1/x":            model.WireOpenAIChat,
		"/mystery/v1/x":               model.WirePassthrough,
		"/":                           model.WirePassthrough,
		"":                            model.WirePassthrough,
	} {
		if got := WireForPath(path); got != want {
			t.Errorf("WireForPath(%q) = %q, want %q", path, got, want)
		}
	}
}
