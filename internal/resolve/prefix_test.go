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
		{"/openai/v1/responses", "openai", "/v1/responses", "https://api.openai.com", model.WireOpenAIResponses},
		{"/openai/v1/responses/", "openai", "/v1/responses/", "https://api.openai.com", model.WireOpenAIResponses},
		{"/openrouter/v1/responses", "openrouter", "/v1/responses", "https://openrouter.ai/api", model.WireOpenAIResponses},
		{"/openai/v1/responses/resp_1/cancel", "openai", "/v1/responses/resp_1/cancel", "https://api.openai.com", model.WireOpenAIChat},
		{"/anthropic/v1/responses", "anthropic", "/v1/responses", "https://api.anthropic.com", model.WireAnthropicMessages},
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
		"/openai/v1/responses":        model.WireOpenAIResponses,
		"/openrouter/v1/responses":    model.WireOpenAIResponses,
		"/anthropic/v1/responses":     model.WireAnthropicMessages,
		"/mystery/v1/responses":       model.WirePassthrough,
		"/mystery/v1/x":               model.WirePassthrough,
		"/":                           model.WirePassthrough,
		"":                            model.WirePassthrough,
	} {
		if got := WireForPath(path); got != want {
			t.Errorf("WireForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWireForRest(t *testing.T) {
	for _, tt := range []struct {
		name string
		wire model.WireFormat
		rest string
		want model.WireFormat
	}{
		{"chat provider on responses", model.WireOpenAIChat, "/v1/responses", model.WireOpenAIResponses},
		{"chat provider on chat", model.WireOpenAIChat, "/v1/chat/completions", model.WireOpenAIChat},
		{"already responses", model.WireOpenAIResponses, "/v1/responses", model.WireOpenAIResponses},
		{"passthrough is never upgraded", model.WirePassthrough, "/v1/responses", model.WirePassthrough},
		{"anthropic is never upgraded", model.WireAnthropicMessages, "/v1/responses", model.WireAnthropicMessages},
		{"lookalike path", model.WireOpenAIChat, "/v1/responses-x", model.WireOpenAIChat},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := WireForRest(tt.wire, tt.rest); got != tt.want {
				t.Fatalf("WireForRest(%q, %q) = %q, want %q", tt.wire, tt.rest, got, tt.want)
			}
		})
	}
}
