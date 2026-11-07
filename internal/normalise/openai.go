package normalise

import (
	"encoding/json"

	"github.com/ethosagent/tollgate/internal/model"
)

// openAIWire normalises OpenAI chat.completions (and compatible) traffic.
type openAIWire struct{ decode decodeFunc }

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

type oaChoice struct {
	Delta        oaDelta `json:"delta"`
	Message      oaDelta `json:"message"`
	FinishReason string  `json:"finish_reason"`
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

type oaResponse struct {
	Choices []oaChoice `json:"choices"`
	Usage   *oaUsage   `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (u *oaUsage) toModel() model.Usage {
	return model.Usage{
		InputTokens:       u.PromptTokens,
		OutputTokens:      u.CompletionTokens,
		CachedInputTokens: u.PromptTokensDetails.CachedTokens,
		ReasoningTokens:   u.CompletionTokensDetails.ReasoningTokens,
	}
}

// NewEventMapper returns the (stateless) chat.completions chunk mapper.
func (w openAIWire) NewEventMapper() EventMapper { return openAIMapper(w) }

type openAIMapper struct{ decode decodeFunc }

// Frame maps one chat.completion.chunk frame; "[DONE]" and blank data yield nothing.
func (m openAIMapper) Frame(_ string, data []byte) ([]model.Event, error) {
	if len(data) == 0 || string(data) == "[DONE]" {
		return nil, nil
	}
	var r oaResponse
	if err := m.decode(data, &r); err != nil {
		return nil, err
	}
	return r.events(false), nil
}

// MapBody maps a complete chat.completion body.
func (w openAIWire) MapBody(body []byte) ([]model.Event, error) {
	var r oaResponse
	if err := w.decode(body, &r); err != nil {
		return nil, err
	}
	return r.events(true), nil
}

// events flattens a chunk (whole=false, uses delta) or a complete body
// (whole=true, uses message and numbers tool calls by position).
func (r *oaResponse) events(whole bool) []model.Event {
	var out []model.Event
	if r.Error != nil {
		out = append(out, errorEvent(r.Error.Message))
	}
	for _, c := range r.Choices {
		d := c.Delta
		if whole {
			d = c.Message
		}
		out = d.appendEvents(out, whole)
		if c.FinishReason != "" {
			out = append(out, model.Event{Kind: model.EventFinish, FinishReason: c.FinishReason})
		}
	}
	if r.Usage != nil {
		out = append(out, usageEvent(r.Usage.toModel()))
	}
	return out
}

func (d oaDelta) appendEvents(out []model.Event, positional bool) []model.Event {
	if d.ReasoningContent != "" {
		out = append(out, model.Event{Kind: model.EventReasoningDelta, Text: d.ReasoningContent})
	}
	if d.Content != "" {
		out = append(out, model.Event{Kind: model.EventTextDelta, Text: d.Content})
	}
	for i, tc := range d.ToolCalls {
		if positional {
			tc.Index = i
		}
		out = append(out, model.Event{Kind: model.EventToolCallDelta, ToolCall: &model.ToolCallDelta{
			Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}})
	}
	return out
}

// rawArguments turns a provider arguments string into RawMessage (nil if empty).
func rawArguments(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}
