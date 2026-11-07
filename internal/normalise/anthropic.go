package normalise

import (
	"encoding/json"

	"github.com/ethosagent/tollgate/internal/model"
)

// anthropicWire normalises Anthropic messages traffic.
type anthropicWire struct{ decode decodeFunc }

type anUsage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
}

type anBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// anEvent covers both a stream event and a complete message body.
type anEvent struct {
	Type         string   `json:"type"`
	Index        int      `json:"index"`
	ContentBlock *anBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message *struct {
		Usage *anUsage `json:"usage"`
	} `json:"message"`
	Usage      *anUsage  `json:"usage"`
	Content    []anBlock `json:"content"`
	StopReason string    `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// NewEventMapper returns a mapper that tracks tool-call numbering and usage.
func (w anthropicWire) NewEventMapper() EventMapper {
	return &anthropicMapper{decode: w.decode, tools: map[int]int{}}
}

type anthropicMapper struct {
	decode decodeFunc
	tools  map[int]int // content block index -> tool call ordinal
	usage  model.Usage // running snapshot: message_start + message_delta
}

// Frame maps one messages stream event. The event name is ignored; the JSON
// "type" field is authoritative. Unknown types (ping, future events) are skipped.
func (m *anthropicMapper) Frame(_ string, data []byte) ([]model.Event, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var e anEvent
	if err := m.decode(data, &e); err != nil {
		return nil, err
	}
	switch e.Type {
	case "message_start":
		if e.Message != nil && e.Message.Usage != nil {
			return []model.Event{m.mergeUsage(e.Message.Usage)}, nil
		}
	case "content_block_start":
		return m.blockStart(&e), nil
	case "content_block_delta":
		return m.blockDelta(&e), nil
	case "message_delta":
		return m.messageDelta(&e), nil
	case "error":
		if e.Error != nil {
			return []model.Event{errorEvent(e.Error.Message)}, nil
		}
		return []model.Event{errorEvent("unknown error")}, nil
	}
	return nil, nil
}

func (m *anthropicMapper) mergeUsage(u *anUsage) model.Event {
	if u.InputTokens > 0 {
		m.usage.InputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		m.usage.OutputTokens = u.OutputTokens
	}
	if u.CacheReadInputTokens > 0 {
		m.usage.CachedInputTokens = u.CacheReadInputTokens
	}
	return usageEvent(m.usage)
}

func (m *anthropicMapper) blockStart(e *anEvent) []model.Event {
	b := e.ContentBlock
	if b == nil {
		return nil
	}
	switch b.Type {
	case "text":
		if b.Text != "" {
			return []model.Event{{Kind: model.EventTextDelta, Text: b.Text}}
		}
	case "thinking":
		if b.Thinking != "" {
			return []model.Event{{Kind: model.EventReasoningDelta, Text: b.Thinking}}
		}
	case "tool_use":
		ord := len(m.tools)
		m.tools[e.Index] = ord
		return []model.Event{{Kind: model.EventToolCallDelta,
			ToolCall: &model.ToolCallDelta{Index: ord, ID: b.ID, Name: b.Name}}}
	}
	return nil
}

func (m *anthropicMapper) blockDelta(e *anEvent) []model.Event {
	d := e.Delta
	if d == nil {
		return nil
	}
	switch d.Type {
	case "text_delta":
		return []model.Event{{Kind: model.EventTextDelta, Text: d.Text}}
	case "thinking_delta":
		return []model.Event{{Kind: model.EventReasoningDelta, Text: d.Thinking}}
	case "input_json_delta":
		if ord, ok := m.tools[e.Index]; ok {
			return []model.Event{{Kind: model.EventToolCallDelta,
				ToolCall: &model.ToolCallDelta{Index: ord, Arguments: d.PartialJSON}}}
		}
	}
	return nil // signature_delta, citations_delta and future deltas
}

func (m *anthropicMapper) messageDelta(e *anEvent) []model.Event {
	var out []model.Event
	if e.Delta != nil && e.Delta.StopReason != "" {
		out = append(out, model.Event{Kind: model.EventFinish, FinishReason: e.Delta.StopReason})
	}
	if e.Usage != nil {
		out = append(out, m.mergeUsage(e.Usage))
	}
	return out
}

// MapBody maps a complete messages response body.
func (w anthropicWire) MapBody(body []byte) ([]model.Event, error) {
	var e anEvent
	if err := w.decode(body, &e); err != nil {
		return nil, err
	}
	var out []model.Event
	if e.Type == "error" && e.Error != nil {
		return append(out, errorEvent(e.Error.Message)), nil
	}
	tools := 0
	for _, b := range e.Content {
		switch b.Type {
		case "text":
			out = append(out, model.Event{Kind: model.EventTextDelta, Text: b.Text})
		case "thinking":
			out = append(out, model.Event{Kind: model.EventReasoningDelta, Text: b.Thinking})
		case "tool_use":
			out = append(out, model.Event{Kind: model.EventToolCallDelta, ToolCall: &model.ToolCallDelta{
				Index: tools, ID: b.ID, Name: b.Name, Arguments: string(b.Input)}})
			tools++
		}
	}
	if e.StopReason != "" {
		out = append(out, model.Event{Kind: model.EventFinish, FinishReason: e.StopReason})
	}
	if e.Usage != nil {
		out = append(out, new(anthropicMapper).mergeUsage(e.Usage))
	}
	return out, nil
}
