package normalise

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func equalFolded(t *testing.T, got, want folded) {
	t.Helper()
	if got.Text != want.Text || got.Reasoning != want.Reasoning || got.Usage != want.Usage {
		t.Fatalf("text/reasoning/usage differ:\n got %+v\nwant %+v", got, want)
	}
	if len(got.Tools) != len(want.Tools) {
		t.Fatalf("tool count: got %d want %d", len(got.Tools), len(want.Tools))
	}
	for i, wt := range want.Tools {
		gt := got.Tools[i]
		if gt == nil || gt.ID != wt.ID || gt.Name != wt.Name ||
			compactJSON(t, gt.Arguments) != compactJSON(t, wt.Arguments) {
			t.Fatalf("tool %d: got %+v want %+v", i, gt, wt)
		}
	}
	if got.Finish != want.Finish {
		t.Fatalf("finish: got %q want %q", got.Finish, want.Finish)
	}
}

func TestMapBody_MatchesStreamedEquivalent(t *testing.T) {
	const openaiToolBody = `{"choices":[{"index":0,"message":{"role":"assistant","content":null,` +
		`"tool_calls":[{"id":"call_fixture001","type":"function","function":{"name":"get_weather",` +
		`"arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":40,"completion_tokens":11,"total_tokens":51}}`
	const openaiReasonBody = `{"choices":[{"index":0,"message":{"role":"assistant",` +
		`"reasoning_content":"The user wants a greeting.","content":"Hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":12,"completion_tokens":9,"total_tokens":21,` +
		`"completion_tokens_details":{"reasoning_tokens":6}}}`
	const anthropicToolBody = `{"type":"message","role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_fixture001","name":"get_weather","input":{"city":"Paris"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":40,"output_tokens":11}}`
	const anthropicThinkBody = `{"type":"message","role":"assistant","content":[` +
		`{"type":"thinking","thinking":"The user wants a greeting.","signature":"s"},` +
		`{"type":"text","text":"Hello"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":12,"output_tokens":9}}`
	tests := []struct {
		name   string
		wire   model.WireFormat
		stream string // response fixture name
		body   func(t *testing.T) []byte
	}{
		{"openai text", model.WireOpenAIChat, "openai/chat-stream-text",
			func(t *testing.T) []byte { return readFixture(t, "openai/chat") }},
		{"openai tools", model.WireOpenAIChat, "openai/chat-stream-tools",
			func(*testing.T) []byte { return []byte(openaiToolBody) }},
		{"openai reasoning", model.WireOpenAIChat, "openai/chat-stream-reasoning",
			func(*testing.T) []byte { return []byte(openaiReasonBody) }},
		{"anthropic text", model.WireAnthropicMessages, "anthropic/messages-stream-text",
			func(t *testing.T) []byte { return readFixture(t, "anthropic/messages") }},
		{"anthropic tools", model.WireAnthropicMessages, "anthropic/messages-stream-tool-use",
			func(*testing.T) []byte { return []byte(anthropicToolBody) }},
		{"anthropic thinking", model.WireAnthropicMessages, "anthropic/messages-stream-thinking",
			func(*testing.T) []byte { return []byte(anthropicThinkBody) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(tc.wire)
			evs, err := w.MapBody(tc.body(t))
			if err != nil {
				t.Fatal(err)
			}
			streamed := fold(eventsOf(t, tc.stream))
			equalFolded(t, fold(evs), streamed)
			if streamed.Finish == "" || fold(evs).Finish == "" {
				t.Fatal("both forms must report a finish reason")
			}
		})
	}
}

func TestEvents_ContentAcrossFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    folded
	}{
		{"openai/chat-stream-text", folded{Text: "Hello world", Finish: "stop", Usage: model.Usage{InputTokens: 12, OutputTokens: 2}}},
		{"openai/chat-stream-reasoning", folded{Text: "Hello", Reasoning: "The user wants a greeting.", Finish: "stop",
			Usage: model.Usage{InputTokens: 12, OutputTokens: 9, ReasoningTokens: 6}}},
		{"anthropic/messages-stream-text", folded{Text: "Hello world", Finish: "end_turn", Usage: model.Usage{InputTokens: 12, OutputTokens: 2}}},
		{"anthropic/messages-stream-thinking", folded{Text: "Hello", Reasoning: "The user wants a greeting.", Finish: "end_turn",
			Usage: model.Usage{InputTokens: 12, OutputTokens: 9}}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			got := fold(eventsOf(t, tc.fixture))
			if got.Text != tc.want.Text || got.Reasoning != tc.want.Reasoning ||
				got.Finish != tc.want.Finish || got.Usage != tc.want.Usage {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
	for _, f := range []string{"openai/chat-stream-tools", "anthropic/messages-stream-tool-use"} {
		t.Run(f, func(t *testing.T) {
			got := fold(eventsOf(t, f))
			tc := got.Tools[0]
			if len(got.Tools) != 1 || tc.Name != "get_weather" || tc.ID == "" ||
				tc.Arguments != `{"city":"Paris"}` || got.Usage.OutputTokens != 11 || got.Usage.InputTokens != 40 {
				t.Fatalf("got %+v tool %+v", got, tc)
			}
		})
	}
}
