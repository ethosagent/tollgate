package fixtures

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNames_ListsBothProvidersSorted(t *testing.T) {
	names := Names()
	if !slices.IsSorted(names) {
		t.Fatalf("names are not sorted: %v", names)
	}
	for _, want := range []string{"openai/chat-stream-text", "anthropic/messages", "openai/request-chat-stream"} {
		if !slices.Contains(names, want) {
			t.Errorf("Names() lacks %q", want)
		}
	}
}

func TestChunks_JoinToTheFixture(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			raw, err := Bytes(name)
			if err != nil {
				t.Fatal(err)
			}
			chunks := Chunks(name)
			if !bytes.Equal(bytes.Join(chunks, nil), raw) {
				t.Fatal("chunks do not join back to the fixture")
			}
			if IsStream(name) && len(chunks) < 2 {
				t.Fatalf("stream fixture split into %d chunks", len(chunks))
			}
			if !IsStream(name) && len(chunks) != 1 {
				t.Fatalf("JSON fixture split into %d chunks", len(chunks))
			}
		})
	}
}

func TestUnknownFixture(t *testing.T) {
	if _, err := Bytes("openai/nope"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Bytes(unknown) err = %v, want ErrUnknown", err)
	}
	if Chunks("openai/nope") != nil || IsResponse("openai/nope") || IsStream("openai/nope") {
		t.Fatal("an unknown fixture reads as known")
	}
}

func TestDerivedProperties(t *testing.T) {
	tests := []struct {
		name, provider, path, contentType string
		status                            int
		response, stream                  bool
	}{
		{"openai/chat-stream-text", "openai", OpenAIChatPath, "text/event-stream", 200, true, true},
		{"openai/chat", "openai", OpenAIChatPath, "application/json", 200, true, false},
		{"openai/responses-stream-tools", "openai", OpenAIResponsesPath, "text/event-stream", 200, true, true},
		{"openai/responses", "openai", OpenAIResponsesPath, "application/json", 200, true, false},
		{"openai/request-responses-tools", "openai", OpenAIResponsesPath, "application/json", 200, false, false},
		{"anthropic/messages-stream-text", "anthropic", AnthropicMessagesPath, "text/event-stream", 200, true, true},
		{"anthropic/error-429", "anthropic", AnthropicMessagesPath, "application/json", 429, true, false},
		{"openai/request-chat-stream", "openai", OpenAIChatPath, "application/json", 200, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Provider(tt.name); got != tt.provider {
				t.Errorf("Provider = %q, want %q", got, tt.provider)
			}
			if got := PathFor(tt.name); got != tt.path {
				t.Errorf("PathFor = %q, want %q", got, tt.path)
			}
			if got := ContentType(tt.name); got != tt.contentType {
				t.Errorf("ContentType = %q, want %q", got, tt.contentType)
			}
			if got := Status(tt.name); got != tt.status {
				t.Errorf("Status = %d, want %d", got, tt.status)
			}
			if got := IsResponse(tt.name); got != tt.response {
				t.Errorf("IsResponse = %t, want %t", got, tt.response)
			}
			wantStream := `"stream":` + map[bool]string{true: "true", false: "false"}[tt.stream]
			if body := string(Request(tt.name)); !strings.Contains(body, wantStream) {
				t.Errorf("Request = %s, want %s", body, wantStream)
			}
		})
	}
}
