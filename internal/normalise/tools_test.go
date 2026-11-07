package normalise

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

// sseData builds SSE frames from JSON payloads.
func sseData(payloads ...string) [][]byte {
	out := make([][]byte, len(payloads))
	for i, p := range payloads {
		out[i] = []byte("data: " + p + "\n\n")
	}
	return out
}

// toolIndexes lists the Index of every tool-call event in order.
func toolIndexes(evs []model.Event) []int {
	var idx []int
	for _, e := range evs {
		if e.Kind == model.EventToolCallDelta {
			idx = append(idx, e.ToolCall.Index)
		}
	}
	return idx
}

func assertTools(t *testing.T, evs []model.Event, wantIdx []int, want map[int]model.ToolCallDelta) {
	t.Helper()
	if got := toolIndexes(evs); !reflect.DeepEqual(got, wantIdx) {
		t.Fatalf("tool event indexes: got %v want %v", got, wantIdx)
	}
	got := fold(evs).Tools
	if len(got) != len(want) {
		t.Fatalf("folded %d tools, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g == nil || g.ID != w.ID || g.Name != w.Name || g.Arguments != w.Arguments {
			t.Fatalf("tool %d: got %+v want %+v", i, g, w)
		}
	}
}

var twoTools = map[int]model.ToolCallDelta{
	0: {ID: "a", Name: "f", Arguments: `{"x":1}`},
	1: {ID: "b", Name: "g", Arguments: `{"y":2}`},
}

func TestMapFrame_MultiToolIndexesAreDense(t *testing.T) {
	tests := []struct {
		name    string
		wire    model.WireFormat
		frames  [][]byte
		wantIdx []int
	}{
		{"openai from 0", model.WireOpenAIChat, sseData(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"g","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"y\":2}"}}]}}]}`,
		), []int{0, 0, 1, 1}},
		{"openai upstream indexes 3 and 7 renumbered", model.WireOpenAIChat, sseData(
			`{"choices":[{"delta":{"tool_calls":[{"index":3,"id":"a","function":{"name":"f","arguments":"{\"x\":1}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":7,"id":"b","function":{"name":"g","arguments":"{\"y\":2}"}}]}}]}`,
		), []int{0, 1}},
		{"anthropic text block before tools", model.WireAnthropicMessages, sseData(
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"a","name":"f"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"b","name":"g"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"y\":2}"}}`,
		), []int{0, 0, 1, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(tc.wire)
			assertTools(t, feed(t, w.NewEventMapper(), tc.frames), tc.wantIdx, twoTools)
		})
	}
}

func TestMapBody_MultiToolIndexesAreDense(t *testing.T) {
	tests := []struct {
		name string
		wire model.WireFormat
		body string
	}{
		{"openai", model.WireOpenAIChat, `{"choices":[{"message":{"role":"assistant","tool_calls":[` +
			`{"id":"a","function":{"name":"f","arguments":"{\"x\":1}"}},` +
			`{"id":"b","function":{"name":"g","arguments":"{\"y\":2}"}}]},"finish_reason":"tool_calls"}]}`},
		{"anthropic with leading text", model.WireAnthropicMessages, `{"type":"message","content":[` +
			`{"type":"text","text":"calling"},` +
			`{"type":"tool_use","id":"a","name":"f","input":{"x":1}},` +
			`{"type":"tool_use","id":"b","name":"g","input":{"y":2}}],"stop_reason":"tool_use"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(tc.wire)
			evs, err := w.MapBody([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			assertTools(t, evs, []int{0, 1}, twoTools)
		})
	}
}

func TestAnthropicUsage_ZeroOutputTokensOverridesStart(t *testing.T) {
	w, _ := ForWire(model.WireAnthropicMessages)
	evs := feed(t, w.NewEventMapper(), sseData(
		`{"type":"message_start","message":{"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":4}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
	))
	want := model.Usage{InputTokens: 25, OutputTokens: 0, CachedInputTokens: 4}
	if got := fold(evs).Usage; got != want {
		t.Fatalf("final usage: got %+v want %+v", got, want)
	}
	body, err := w.MapBody([]byte(`{"type":"message","content":[],"usage":{"input_tokens":3,"output_tokens":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := fold(body).Usage; got != (model.Usage{InputTokens: 3}) {
		t.Fatalf("body usage: got %+v", got)
	}
}

func TestEventMapper_FinishSignalsTruncation(t *testing.T) {
	for _, name := range []string{"openai/chat-stream-text", "anthropic/messages-stream-text"} {
		w, _ := ForWire(wireOf(name))
		frames := fixtures.Chunks(name)
		t.Run(name+"/complete stream", func(t *testing.T) {
			m := w.NewEventMapper()
			feed(t, m, frames)
			if got := m.Finish(); len(got) != 0 {
				t.Fatalf("complete stream: Finish returned %+v", got)
			}
		})
		t.Run(name+"/truncated stream", func(t *testing.T) {
			m := w.NewEventMapper()
			feed(t, m, frames[:2])
			got := m.Finish()
			if len(got) != 1 || got[0].Kind != model.EventError || !strings.Contains(got[0].Err, "finish") {
				t.Fatalf("truncated stream: Finish returned %+v", got)
			}
			if again := m.Finish(); len(again) != 0 {
				t.Fatalf("second Finish must be a no-op, got %+v", again)
			}
		})
		t.Run(name+"/empty stream", func(t *testing.T) {
			if got := w.NewEventMapper().Finish(); len(got) != 0 {
				t.Fatalf("empty stream: Finish returned %+v", got)
			}
		})
	}
}

func TestMapFrame_ErrorEventCountsAsTerminal(t *testing.T) {
	for wire, frame := range map[model.WireFormat]string{
		model.WireOpenAIChat:        `{"error":{"message":"boom"}}`,
		model.WireAnthropicMessages: `{"type":"error","error":{"message":"boom"}}`,
	} {
		w, _ := ForWire(wire)
		m := w.NewEventMapper()
		evs := feed(t, m, sseData(frame))
		if len(evs) != 1 || evs[0].Kind != model.EventError || evs[0].Err != "boom" {
			t.Fatalf("%s: got %+v", wire, evs)
		}
		if got := m.Finish(); len(got) != 0 {
			t.Fatalf("%s: error already ended the stream, Finish returned %+v", wire, got)
		}
	}
}
