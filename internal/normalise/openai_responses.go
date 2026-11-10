package normalise

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// responsesWire normalises OpenAI Responses API traffic (POST /v1/responses),
// streamed and buffered.
type responsesWire struct{}

type orUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *orUsage) toModel() model.Usage {
	return model.Usage{
		InputTokens:       u.InputTokens,
		OutputTokens:      u.OutputTokens,
		CachedInputTokens: u.InputTokensDetails.CachedTokens,
		ReasoningTokens:   u.OutputTokensDetails.ReasoningTokens,
	}
}

// orResponse is the part of a Response object both forms need. The output
// array is left out of the stream decode on purpose: a terminal frame repeats
// every item already streamed, and skipping it is cheaper than reading it.
type orResponse struct {
	Status string `json:"status"`
	Error  *struct {
		Code    json.RawMessage `json:"code"` // a string, or a number from some gateways
		Message string          `json:"message"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *orUsage `json:"usage"`
}

// closing appends the events that end a response: its finish reason (or its
// error), then its usage. Finish before usage, as the chat wire orders them.
// fallback is the status to assume when the object names none.
func (r *orResponse) closing(out []model.Event, fallback string) []model.Event {
	status := r.Status
	if status == "" {
		status = fallback
	}
	switch {
	case status == "failed":
		out = append(out, errorEvent(r.errorMessage()), finishEv("failed"))
	case status == "" && r.Error != nil:
		out = append(out, errorEvent(r.errorMessage()))
	case status == "incomplete":
		reason := "incomplete"
		if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
			reason = r.IncompleteDetails.Reason
		}
		out = append(out, finishEv(reason))
	case status == "completed" || status == "cancelled":
		out = append(out, finishEv(status))
	}
	if r.Usage != nil {
		out = append(out, usageEvent(r.Usage.toModel()))
	}
	return out
}

func (r *orResponse) errorMessage() string {
	switch {
	case r.Error == nil:
		return "response failed"
	case r.Error.Message != "":
		return r.Error.Message
	}
	if code := codeString(r.Error.Code); code != "" {
		return code
	}
	return "response failed"
}

// orEvent is one stream event. Delta is a string for every event this wire
// maps (text, summary, reasoning, function arguments).
type orEvent struct {
	Type        string `json:"type"`
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
	Item        struct {
		Type   string          `json:"type"`
		CallID string          `json:"call_id"`
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`  // custom_tool_call
		Action json.RawMessage `json:"action"` // local_shell_call
	} `json:"item"`
	Response orResponse `json:"response"`
	// Message and Code belong to the stream "error" event.
	Message string          `json:"message"`
	Code    json.RawMessage `json:"code"`
}

// NewEventMapper returns a mapper that tracks tool-call numbering.
func (w responsesWire) NewEventMapper() EventMapper { return &responsesMapper{} }

type responsesMapper struct {
	ev    orEvent // decode target, reset per frame
	tools toolOrdinals
	state streamState
}

// skipsResponsesEvent names the events that carry nothing the neutral model
// keeps: lifecycle, part boundaries, and the ".done" repeats of text already
// streamed. (output_item.done is not skipped: it completes custom and shell
// tool calls, whose input does not stream as JSON.) With an SSE event name they are dropped undecoded. The
// terminal events are not here: they carry the usage.
func skipsResponsesEvent(event string) bool {
	switch event {
	case "response.created", "response.in_progress", "response.queued",
		"response.content_part.added", "response.content_part.done",
		"response.output_text.done",
		"response.function_call_arguments.done", "response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done", "response.reasoning_summary_text.done",
		"response.reasoning_text.done", "response.output_text.annotation.added":
		return true
	}
	return false
}

// Frame maps one Responses stream event. The JSON "type" is authoritative;
// the SSE event name only lets boring events skip the decode. Unknown types
// (future events, tool-specific progress) yield nothing.
func (m *responsesMapper) Frame(event string, data []byte) ([]model.Event, error) {
	if len(data) == 0 || string(data) == "[DONE]" || skipsResponsesEvent(event) || cannotMap(event, data) {
		return nil, nil
	}
	m.ev = orEvent{}
	if err := strictDecode(data, &m.ev); err != nil {
		return nil, err
	}
	return m.state.observe(m.dispatch()), nil
}

// cannotMap drops an output_item frame whose item type the mapper ignores
// without decoding it: added matters only for function_call, done only for
// custom_tool_call and local_shell_call. The type name must appear in the
// frame to be read, so a miss here is a miss in the decode too; a false hit
// (the name inside text) just decodes. Only a named event is judged.
func cannotMap(event string, data []byte) bool {
	switch event {
	case "response.output_item.added":
		return !bytes.Contains(data, itemFunctionCall)
	case "response.output_item.done":
		return !bytes.Contains(data, itemCustomTool) && !bytes.Contains(data, itemLocalShell)
	}
	return false
}

var (
	itemFunctionCall = []byte(`"function_call"`)
	itemCustomTool   = []byte(`"custom_tool_call"`)
	itemLocalShell   = []byte(`"local_shell_call"`)
)

func (m *responsesMapper) dispatch() []model.Event {
	e := &m.ev
	switch e.Type {
	case "response.output_text.delta":
		if e.Delta != "" {
			return []model.Event{textEv(e.Delta)}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if e.Delta != "" {
			return []model.Event{reasoningEv(e.Delta)}
		}
	case "response.output_item.added":
		if e.Item.Type == "function_call" {
			return []model.Event{toolEv(m.tools.assign(e.OutputIndex), e.Item.CallID, e.Item.Name, "")}
		}
	case "response.output_item.done":
		return m.itemDone()
	case "response.function_call_arguments.delta":
		return []model.Event{toolEv(m.tools.assign(e.OutputIndex), "", "", e.Delta)}
	case "response.completed":
		return e.Response.closing(nil, "completed")
	case "response.incomplete":
		return e.Response.closing(nil, "incomplete")
	case "response.failed":
		return e.Response.closing(nil, "failed")
	case "error":
		return []model.Event{errorEvent(m.errorMessage())}
	}
	return nil
}

// itemDone maps a finished custom or shell tool call to one complete tool
// event. Their input is freeform text or an action object, not streamed JSON
// arguments, so it is taken whole from the item.
func (m *responsesMapper) itemDone() []model.Event {
	it := &m.ev.Item
	var args json.RawMessage
	name := it.Name
	switch it.Type {
	case "custom_tool_call":
		args = customInput(it.Input)
	case "local_shell_call":
		name, args = localShellName, actionOf(it.Action)
	default:
		return nil
	}
	return []model.Event{toolEv(m.tools.assign(m.ev.OutputIndex), it.CallID, name, string(args))}
}

func (m *responsesMapper) errorMessage() string {
	switch {
	case m.ev.Message != "":
		return m.ev.Message
	}
	if code := codeString(m.ev.Code); code != "" {
		return code
	}
	return "unknown error"
}

// Finish reports a stream that ended without a finish reason.
func (m *responsesMapper) Finish() []model.Event { return m.state.end() }

// Gap records dropped frames; see EventMapper.Gap.
func (m *responsesMapper) Gap(n int64) { m.state.gap(n) }

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

// codeString renders an error code that is a JSON string or number.
func codeString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if t := strings.TrimSpace(string(raw)); t != "null" {
		return t
	}
	return ""
}
