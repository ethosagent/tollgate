package fixtures

import (
	"context"
	"testing"
	"time"
)

func TestDefault_PicksFixtureFromPathAndStreamFlag(t *testing.T) {
	tests := []struct {
		name, path, body, want string
	}{
		{"openai buffered", "/openai" + OpenAIChatPath, `{}`, "openai/chat"},
		{"openai streamed", "/openai" + OpenAIChatPath, `{"stream":true}`, "openai/chat-stream-text"},
		{"responses buffered", "/openai" + OpenAIResponsesPath, `{}`, "openai/responses"},
		{"responses streamed", "/openai" + OpenAIResponsesPath, `{"stream":true}`, "openai/responses-stream-text"},
		{"anthropic buffered", AnthropicMessagesPath, `{"stream":false}`, "anthropic/messages"},
		{"anthropic streamed", AnthropicMessagesPath, `{"stream":true}`, "anthropic/messages-stream-text"},
		{"body not JSON counts as buffered", OpenAIChatPath, `not json`, "openai/chat"},
		{"unknown path", "/v1/embeddings", `{"stream":true}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Default(tt.path, []byte(tt.body)); got != tt.want {
				t.Fatalf("Default(%q, %s) = %q, want %q", tt.path, tt.body, got, tt.want)
			}
			if tt.want != "" && len(Chunks(tt.want)) == 0 {
				t.Fatalf("Default returned %q, which is not a fixture", tt.want)
			}
		})
	}
}

func TestWait(t *testing.T) {
	live := context.Background()
	done, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name string
		ctx  context.Context
		d    time.Duration
		want bool
	}{
		{"zero delay on a live context", live, 0, true},
		{"zero delay on a done context", done, 0, false},
		{"short delay elapses", live, time.Millisecond, true},
		{"done context cuts a long delay short", done, time.Hour, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Wait(tt.ctx, tt.d); got != tt.want {
				t.Fatalf("Wait(%v) = %v, want %v", tt.d, got, tt.want)
			}
		})
	}
}
