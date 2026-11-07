package normalise

import (
	"encoding/json"

	"github.com/ethosagent/tollgate/internal/model"
)

// openAIWire normalises OpenAI chat.completions (and compatible) traffic.
type openAIWire struct{}

type oaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// oaDelta is both a streamed delta and a complete message: same fields.
type oaDelta struct {
	Content          string       `json:"content"`
	ReasoningContent string       `json:"reasoning_content"`
	ToolCalls        []oaToolCall `json:"tool_calls"`
}

type oaUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type oaError struct {
	Message string `json:"message"`
}

// oaChunk is one chat.completion.chunk (choices carry a delta).
type oaChunk struct {
	Choices []struct {
		Index        int     `json:"index"`
		Delta        oaDelta `json:"delta"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Error *oaError `json:"error"`
}

// oaBody is a complete chat.completion (choices carry a message).
type oaBody struct {
	Choices []struct {
		Index        int     `json:"index"`
		Message      oaDelta `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage `json:"usage"`
	Error *oaError `json:"error"`
}

func (u *oaUsage) toModel() model.Usage {
	return model.Usage{
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
		ReasoningTokens:   u.CompletionTokensDetails.ReasoningTokens,
	}
}

// NewEventMapper returns a chunk mapper; it reuses one decode target per frame.
func (w openAIWire) NewEventMapper() EventMapper { return &openAIMapper{} }

type openAIMapper struct {
	chunk oaChunk
	tools toolOrdinals
	state streamState
}

// Frame maps one chat.completion.chunk frame; "[DONE]" and blank data yield nothing.
func (m *openAIMapper) Frame(_ string, data []byte) ([]model.Event, error) {
	if len(data) == 0 || string(data) == "[DONE]" {
		return nil, nil
	}
	choices := m.chunk.Choices
	clear(choices[:cap(choices)])
	m.chunk = oaChunk{Choices: choices[:0]}
	if err := strictDecode(data, &m.chunk); err != nil {
		return nil, err
	}
	out := make([]model.Event, 0, 2)
	if m.chunk.Error != nil {
		out = append(out, errorEvent(m.chunk.Error.Message))
	}
	for i := range m.chunk.Choices {
		c := &m.chunk.Choices[i]
		out = c.Delta.appendEvents(out, &m.tools, c.Index)
		if c.FinishReason != "" {
			out = append(out, finishEv(c.FinishReason))
		}
	}
	if m.chunk.Usage != nil {
		out = append(out, usageEvent(m.chunk.Usage.toModel()))
	}
	return m.state.observe(out), nil
}

// Finish reports a stream that ended without a finish reason.
func (m *openAIMapper) Finish() []model.Event { return m.state.end() }

// Gap records dropped frames; see EventMapper.Gap.
func (m *openAIMapper) Gap(n int64) { m.state.gap(n) }

// MapBody maps a complete chat.completion body. Bodies carry no tool-call
// index, so tool calls are keyed by their position in the message.
func (w openAIWire) MapBody(body []byte) ([]model.Event, error) {
	var r oaBody
	if err := strictDecode(body, &r); err != nil {
		return nil, err
	}
	var out []model.Event
	if r.Error != nil {
		out = append(out, errorEvent(r.Error.Message))
	}
	var tools toolOrdinals
	for i := range r.Choices {
		c := &r.Choices[i]
		for j := range c.Message.ToolCalls {
			c.Message.ToolCalls[j].Index = j
		}
		out = c.Message.appendEvents(out, &tools, i) // position: bodies may omit index
		if c.FinishReason != "" {
			out = append(out, finishEv(c.FinishReason))
		}
	}
	if r.Usage != nil {
		out = append(out, usageEvent(r.Usage.toModel()))
	}
	return out, nil
}

// appendEvents flattens a delta or message of one choice. Tool-call indexes
// go through tools keyed by (choice, tool index), so they are dense 0-based
// ordinals whatever the upstream numbering, and two choices (n>1) that both
// use tool index 0 stay two tool calls.
func (d *oaDelta) appendEvents(out []model.Event, tools *toolOrdinals, choice int) []model.Event {
	if d.ReasoningContent != "" {
		out = append(out, reasoningEv(d.ReasoningContent))
	}
	if d.Content != "" {
		out = append(out, textEv(d.Content))
	}
	for i := range d.ToolCalls {
		tc := &d.ToolCalls[i]
		out = append(out, toolEv(tools.assign(choiceToolKey(choice, tc.Index)), tc.ID, tc.Function.Name, tc.Function.Arguments))
	}
	return out
}

// choiceToolKey packs a choice index and a per-choice tool index into one
// ordinal key (16 bits each, so it fits a 32-bit int).
func choiceToolKey(choice, tool int) int { return choice<<16 | tool&0xffff }

// rawArguments turns a provider arguments string into RawMessage (nil if empty).
func rawArguments(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}
