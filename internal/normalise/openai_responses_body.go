package normalise

import (
	"encoding/json"

	"github.com/ethosagent/tollgate/internal/model"
)

// The stream:false side of the Responses wire: a complete Response object
// mapped to the events its stream would have produced.

// orOutputItem is one item of a complete response's output array.
type orOutputItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Action    json.RawMessage `json:"action"`
	Summary   json.RawMessage `json:"summary"`
	Content   json.RawMessage `json:"content"`
}

type orBody struct {
	orResponse
	Output []orOutputItem `json:"output"`
}

// MapBody maps a complete Response object to the events its stream would
// have produced: reasoning, text and tool calls in output order, then the
// finish reason and usage. Tool calls are numbered by position among the
// output items.
func (w responsesWire) MapBody(body []byte) ([]model.Event, error) {
	var r orBody
	if err := strictDecode(body, &r); err != nil {
		return nil, err
	}
	var out []model.Event
	var tools toolOrdinals
	for i := range r.Output {
		out = r.Output[i].appendEvents(out, &tools, i)
	}
	return r.closing(out, ""), nil
}

func (it *orOutputItem) appendEvents(out []model.Event, tools *toolOrdinals, pos int) []model.Event {
	switch it.Type {
	case "reasoning":
		out = appendParts(out, it.Summary, "", reasoningEv)
		out = appendParts(out, it.Content, "reasoning_text", reasoningEv) // raw reasoning, open-weight models
	case "message":
		out = appendParts(out, it.Content, "output_text", textEv)
	case "function_call":
		out = append(out, toolEv(tools.assign(pos), it.CallID, it.Name, string(argumentsOf(it.Arguments))))
	case "custom_tool_call":
		out = append(out, toolEv(tools.assign(pos), it.CallID, it.Name, string(customInput(it.Input))))
	case "local_shell_call":
		out = append(out, toolEv(tools.assign(pos), it.CallID, localShellName, string(actionOf(it.Action))))
	}
	return out
}

// appendParts emits one event per non-empty text part of the given type ("" =
// any). A field of an unexpected shape yields nothing: only the item types
// above are read, and a body is never rejected for what it does not model.
func appendParts(out []model.Event, raw json.RawMessage, typ string, ev func(string) model.Event) []model.Event {
	var parts []oaPart
	if decodeRaw(raw, &parts) != nil {
		return out
	}
	for _, p := range parts {
		if p.Text != "" && (typ == "" || p.Type == typ) {
			out = append(out, ev(p.Text))
		}
	}
	return out
}
