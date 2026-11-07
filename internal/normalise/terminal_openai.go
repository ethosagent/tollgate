package normalise

import (
	"bytes"
	"encoding/json"
)

// openAITerminal: the region opens at the chunk with a finish_reason (so the
// optional usage chunk and [DONE] are held with it), or at [DONE] when a
// compatible server sends no finish_reason.
type openAITerminal struct{}

var doneData = []byte("[DONE]")

func (openAITerminal) Opens(_ string, data []byte) bool {
	return bytes.Equal(data, doneData) || hasFinishReason(data)
}

func (openAITerminal) NewSplicer() Splicer { return &openAISplicer{} }

// openAISplicer passes recovery frames through unchanged except the first
// frame of each recovery stream when it is a role-only delta (the stream's
// opening "assistant" announcement), which the client already has. The
// recovery keeps its own chunk id; clients key on choices[0].index, which
// is 0 on both streams.
type openAISplicer struct{ first bool }

func (s *openAISplicer) Sent(string, []byte) {}
func (s *openAISplicer) Begin()              { s.first = true }

func (s *openAISplicer) Recovery(raw []byte, _ string, data []byte) ([]byte, bool) {
	if !s.first || len(data) == 0 {
		return raw, true
	}
	s.first = false
	return raw, !roleOnly(data)
}

// roleOnly reports whether a chunk's only content is a role announcement.
func roleOnly(data []byte) bool {
	var c struct {
		Choices []struct {
			Delta struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &c) != nil || len(c.Choices) == 0 {
		return false
	}
	for _, ch := range c.Choices {
		d := ch.Delta
		if d.Role == "" || d.Content != "" || len(d.ToolCalls) > 0 || ch.FinishReason != nil {
			return false
		}
	}
	return true
}
