package normalise

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func TestShapeForWire(t *testing.T) {
	for wire, want := range map[model.WireFormat]ErrorShape{
		model.WireOpenAIChat:        ShapeOpenAI,
		model.WireOpenAIResponses:   ShapeOpenAI,
		model.WireAnthropicMessages: ShapeAnthropic,
		model.WirePassthrough:       ShapeGeneric,
		"":                          ShapeGeneric,
	} {
		if got := ShapeForWire(wire); got != want {
			t.Errorf("ShapeForWire(%q) = %v, want %v", wire, got, want)
		}
	}
}

func TestWriteError_Shapes(t *testing.T) {
	tests := []struct {
		name    string
		shape   ErrorShape
		status  int
		wantTyp string
		check   func(t *testing.T, body map[string]any)
	}{
		{"openai 404", ShapeOpenAI, 404, "invalid_request_error", func(t *testing.T, b map[string]any) {
			e := b["error"].(map[string]any)
			if _, ok := e["param"]; !ok || e["message"] != "boom" {
				t.Errorf("openai body = %v", b)
			}
		}},
		{"anthropic 429", ShapeAnthropic, 429, "rate_limit_error", func(t *testing.T, b map[string]any) {
			if b["type"] != "error" {
				t.Errorf("anthropic body missing top-level type: %v", b)
			}
		}},
		{"anthropic 413", ShapeAnthropic, 413, "request_too_large", nil},
		{"anthropic 502", ShapeAnthropic, 502, "api_error", nil},
		{"generic 401", ShapeGeneric, 401, "authentication_error", nil},
		{"generic 504", ShapeGeneric, 504, "timeout_error", nil},
		{"openai 403", ShapeOpenAI, 403, "permission_error", nil},
		{"anthropic 403", ShapeAnthropic, 403, "permission_error", nil},
		{"openai 502", ShapeOpenAI, 502, "server_error", nil},
		{"anthropic 404", ShapeAnthropic, 404, "not_found_error", nil},
		{"anthropic 400", ShapeAnthropic, 400, "invalid_request_error", nil},
		{"anthropic 401", ShapeAnthropic, 401, "authentication_error", nil},
		{"anthropic 504", ShapeAnthropic, 504, "timeout_error", nil},
		{"openai 429", ShapeOpenAI, 429, "rate_limit_error", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteError(rec, tt.shape, tt.status, "boom")
			if rec.Code != tt.status || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("code=%d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("not JSON: %v", err)
			}
			e, _ := body["error"].(map[string]any)
			if e["type"] != tt.wantTyp {
				t.Fatalf("error.type = %v, want %s (%v)", e["type"], tt.wantTyp, body)
			}
			if tt.check != nil {
				tt.check(t, body)
			}
		})
	}
}

func TestStatusMessage_FixedPerStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{403, "permission denied"},
		{504, "upstream timed out"},
		{418, "i'm a teapot"},
		{599, "error"},
	}
	for _, tt := range tests {
		if got := StatusMessage(tt.status); got != tt.want {
			t.Errorf("StatusMessage(%d) = %q, want %q", tt.status, got, tt.want)
		}
	}
}
