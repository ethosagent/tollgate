package normalise

import (
	"encoding/json"
	"strconv"

	"github.com/ethosagent/tollgate/internal/model"
)

// anthropicWire normalises Anthropic messages traffic.
type anthropicWire struct{}

// optInt is a JSON number that remembers whether it was present, so a real
// 0 (for example output_tokens:0 in message_delta) overrides a stale value.
type optInt struct {
	v   int64
	set bool
}

func (o *optInt) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	v, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return err
	}
	o.v, o.set = v, true
	return nil
}

type anUsage struct {
	InputTokens          optInt `json:"input_tokens"`
	OutputTokens         optInt `json:"output_tokens"`
	CacheReadInputTokens optInt `json:"cache_read_input_tokens"`
}

// mergeUsage overlays the fields present in u onto base. Present zeros count.
func mergeUsage(base model.Usage, u *anUsage) model.Usage {
	if u.InputTokens.set {
		base.InputTokens = u.InputTokens.v
	}
	if u.OutputTokens.set {
		base.OutputTokens = u.OutputTokens.v
	}
	if u.CacheReadInputTokens.set {
		base.CachedInputTokens = u.CacheReadInputTokens.v
	}
	return base
}

type anBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

type anDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

// anEvent covers both a stream event and a complete message body.
type anEvent struct {
	Type         string  `json:"type"`
	Index        int     `json:"index"`
	ContentBlock anBlock `json:"content_block"`
	Delta        anDelta `json:"delta"`
	Message      struct {
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
func (w anthropicWire) NewEventMapper() EventMapper { return &anthropicMapper{} }

type anthropicMapper struct {
	ev    anEvent // decode target, reset per frame
	tools toolOrdinals
	usage model.Usage // running snapshot: message_start + message_delta
	state streamState
}

// Frame maps one messages stream event. The event name is ignored; the JSON
// "type" field is authoritative. Unknown types (ping, future events) are skipped.
func (m *anthropicMapper) Frame(_ string, data []byte) ([]model.Event, error) {
	if len(data) == 0 {
		return nil, nil
	}
	m.ev = anEvent{}
	if err := strictDecode(data, &m.ev); err != nil {
		return nil, err
	}
	return m.state.observe(m.dispatch()), nil
}

func (m *anthropicMapper) dispatch() []model.Event {
	e := &m.ev
	switch e.Type {
	case "message_start":
		if e.Message.Usage != nil {
			return m.mergeUsage(nil, e.Message.Usage)
		}
	case "content_block_start":
		return m.blockStart(e)
	case "content_block_delta":
		return m.blockDelta(e)
	case "message_delta":
		var out []model.Event
		if e.Delta.StopReason != "" {
			out = append(out, finishEv(e.Delta.StopReason))
		}
		if e.Usage != nil {
			out = m.mergeUsage(out, e.Usage)
		}
		return out
	case "error":
		if e.Error != nil {
			return []model.Event{errorEvent(e.Error.Message)}
		}
		return []model.Event{errorEvent("unknown error")}
	}
	return nil
}

// Finish reports a stream that ended without a finish reason.
func (m *anthropicMapper) Finish() []model.Event { return m.state.end() }

// Gap records dropped frames; see EventMapper.Gap.
func (m *anthropicMapper) Gap(n int64) { m.state.gap(n) }

func (m *anthropicMapper) mergeUsage(out []model.Event, u *anUsage) []model.Event {
	m.usage = mergeUsage(m.usage, u)
	return append(out, usageEvent(m.usage))
}

func (m *anthropicMapper) blockStart(e *anEvent) []model.Event {
	b := &e.ContentBlock
	switch b.Type {
	case "text":
		if b.Text != "" {
			return []model.Event{textEv(b.Text)}
		}
	case "thinking":
		if b.Thinking != "" {
			return []model.Event{reasoningEv(b.Thinking)}
		}
	case "tool_use":
		return []model.Event{toolEv(m.tools.assign(e.Index), b.ID, b.Name, "")}
	}
	return nil
}

func (m *anthropicMapper) blockDelta(e *anEvent) []model.Event {
	d := &e.Delta
	switch d.Type {
	case "text_delta":
		return []model.Event{textEv(d.Text)}
	case "thinking_delta":
		return []model.Event{reasoningEv(d.Thinking)}
	case "input_json_delta":
		if ord, ok := m.tools.lookup(e.Index); ok {
			return []model.Event{toolEv(ord, "", "", d.PartialJSON)}
		}
	}
	return nil // signature_delta, citations_delta and future deltas
}

// MapBody maps a complete messages response body.
func (w anthropicWire) MapBody(body []byte) ([]model.Event, error) {
	var e anEvent
	if err := strictDecode(body, &e); err != nil {
		return nil, err
	}
	if e.Type == "error" && e.Error != nil {
		return []model.Event{errorEvent(e.Error.Message)}, nil
	}
	var out []model.Event
	var tools toolOrdinals
	for i := range e.Content {
		b := &e.Content[i]
		switch b.Type {
		case "text":
			out = append(out, textEv(b.Text))
		case "thinking":
			out = append(out, reasoningEv(b.Thinking))
		case "tool_use":
			out = append(out, toolEv(tools.assign(i), b.ID, b.Name, string(b.Input)))
		}
	}
	if e.StopReason != "" {
		out = append(out, finishEv(e.StopReason))
	}
	if e.Usage != nil {
		out = append(out, usageEvent(mergeUsage(model.Usage{}, e.Usage)))
	}
	return out, nil
}
