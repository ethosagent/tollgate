package normalise

import (
	"bytes"
	"os"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func summarise(t *testing.T, w model.WireFormat, file string) model.StreamSummary {
	t.Helper()
	wire, err := ForWire(w)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testkit/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	z := NewSummarizer(wire)
	for f := range bytes.SplitSeq(raw, []byte("\n\n")) {
		if len(bytes.TrimSpace(f)) > 0 {
			ev, data := SplitFrame(f)
			z.Frame(ev, data)
		}
	}
	return z.Summary()
}

func TestSummarizer_TextStreams(t *testing.T) {
	for _, tc := range []struct {
		wire   model.WireFormat
		file   string
		finish string
		usage  model.Usage
	}{
		{model.WireOpenAIChat, "openai/chat-stream-text.sse", "stop", model.Usage{InputTokens: 12, OutputTokens: 2}},
		{model.WireAnthropicMessages, "anthropic/messages-stream-text.sse", "end_turn", model.Usage{InputTokens: 12, OutputTokens: 2}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			s := summarise(t, tc.wire, tc.file)
			if s.FinishReason != tc.finish || s.Usage != tc.usage || s.Events == 0 || len(s.ToolCalls) != 0 {
				t.Fatalf("summary = %+v", s)
			}
		})
	}
}

func TestSummarizer_ToolCallsAssembled(t *testing.T) {
	for _, tc := range []struct {
		wire model.WireFormat
		file string
	}{
		{model.WireOpenAIChat, "openai/chat-stream-tools.sse"},
		{model.WireAnthropicMessages, "anthropic/messages-stream-tool-use.sse"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			s := summarise(t, tc.wire, tc.file)
			if len(s.ToolCalls) == 0 {
				t.Fatalf("no tool calls: %+v", s)
			}
			tc0 := s.ToolCalls[0]
			if tc0.ID == "" || tc0.Name == "" || len(tc0.Arguments) == 0 {
				t.Fatalf("tool call = %+v (%s)", tc0, tc0.Arguments)
			}
		})
	}
}

func TestSummarizer_MalformedFrameIsSkipped(t *testing.T) {
	wire, _ := ForWire(model.WireOpenAIChat)
	z := NewSummarizer(wire)
	z.Frame("", []byte("{not json"))
	if s := z.Summary(); s.Events != 0 {
		t.Fatalf("summary = %+v", s)
	}
}
