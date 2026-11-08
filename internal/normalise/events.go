package normalise

import "github.com/ethosagent/tollgate/internal/model"

func textEv(s string) model.Event      { return model.Event{Kind: model.EventTextDelta, Text: s} }
func reasoningEv(s string) model.Event { return model.Event{Kind: model.EventReasoningDelta, Text: s} }

func finishEv(reason string) model.Event {
	return model.Event{Kind: model.EventFinish, FinishReason: reason}
}

func toolEv(index int, id, name, args string) model.Event {
	return model.Event{Kind: model.EventToolCallDelta,
		ToolCall: &model.ToolCallDelta{Index: index, ID: id, Name: name, Arguments: args}}
}

// usageEvent wraps a usage snapshot as an event.
func usageEvent(u model.Usage) model.Event {
	return model.Event{Kind: model.EventUsage, Usage: &u}
}

func errorEvent(msg string) model.Event {
	return model.Event{Kind: model.EventError, Err: msg}
}

// toolOrdinals numbers the tool calls of one response densely from 0, in the
// order each provider key (OpenAI tool_calls index, Anthropic content block
// index, body position) is first seen. Both wires use it, so
// model.ToolCallDelta.Index means the same on either. The table holds at
// most maxToolCalls keys, so a malformed stream of ever-new indexes cannot
// grow it without bound.
type toolOrdinals struct{ keys []int }

// maxToolCalls bounds the distinct tool calls numbered per response. Past it,
// every new key gets the ordinal maxToolCalls and is not remembered.
const maxToolCalls = 1024

// assign returns the ordinal for key, allocating the next one on first sight.
func (t *toolOrdinals) assign(key int) int {
	if ord, ok := t.lookup(key); ok {
		return ord
	}
	if len(t.keys) >= maxToolCalls {
		return maxToolCalls
	}
	t.keys = append(t.keys, key)
	return len(t.keys) - 1
}

// lookup returns the ordinal of an already-seen key.
func (t *toolOrdinals) lookup(key int) (int, bool) {
	for i, k := range t.keys {
		if k == key {
			return i, true
		}
	}
	return 0, false
}

// streamState is what Finish needs: did frames produce events, and did a
// finish reason arrive.
type streamState struct {
	seen, finished, closed, gapped bool
}

// gap records that frames were dropped (EventMapper.Gap).
func (s *streamState) gap(n int64) {
	if n > 0 {
		s.gapped = true
	}
}

func (s *streamState) observe(evs []model.Event) []model.Event {
	for i := range evs {
		s.seen = true
		if k := evs[i].Kind; k == model.EventFinish || k == model.EventError {
			s.finished = true
		}
	}
	return evs
}

// end implements EventMapper.Finish for both wires.
func (s *streamState) end() []model.Event {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.seen && !s.finished && !s.gapped {
		return []model.Event{errorEvent("stream ended before a finish reason")}
	}
	return nil
}
