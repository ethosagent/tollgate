package normalise

import (
	"bytes"
	"encoding/json"

	"github.com/ethosagent/tollgate/internal/model"
)

// orInput is the slow-path "input": a bare string (one user message) or an
// array. The common array is decoded directly into []orItem, in one pass;
// orInput only runs when that fails (a string input, or a malformed one).
type orInput []orItem

func (in *orInput) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '[' {
		items, err := decodeContent(b, func(s string) orItem {
			return orItem{Type: "message", Role: "user", Content: orText{s: s}}
		})
		*in = items
		return err
	}
	// Size the slice up front: orItem is large and growing it by doubling
	// was most of this parser's allocated bytes.
	items := make([]orItem, 0, min(len(b)/itemBytesGuess+1, maxItemsGuess))
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	*in = items
	return nil
}

// An input item is rarely under this many bytes; the guess only sizes the
// first allocation, the slice still grows past it.
const (
	itemBytesGuess = 96
	maxItemsGuess  = 4096
)

// orItem is one input item. The fields whose JSON type differs between item
// types stay lenient or raw and are used only by the branch of the item types
// that read them, so an item the builder does not model can never fail the
// parse: tool_search_call.arguments and computer_call_output.output are
// objects, for example. Item types with no place in the neutral model
// (item_reference, web_search_call, ...) are skipped. Each field is decoded
// once, by the decoder that reads the item.
type orItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Content   orText          `json:"content"`
	Arguments orArgs          `json:"arguments"`
	Output    orText          `json:"output"`
	Summary   orParts         `json:"summary"`
	Input     json.RawMessage `json:"input"`
	Action    json.RawMessage `json:"action"`
}

type orHead struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	Instructions       string `json:"instructions"`
	PreviousResponseID string `json:"previous_response_id"`
	Reasoning          struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
}

type orRequest struct {
	orHead
	Input orInput `json:"input"`
}

// ParseRequest fills Model, Stream, Messages and the Meta keys of a
// Responses create-request body. instructions becomes the first system
// message; function_call items become tool calls on an assistant message
// (joined to the assistant message just before them, as chat puts text and
// tool_calls on one message); function_call_output items become RoleTool
// messages; reasoning summaries become the Reasoning of the next assistant
// message.
func (w responsesWire) ParseRequest(body []byte, c *model.Call) error {
	var r orRequest
	if err := strictDecode(body, &r); err != nil {
		return wrapRequestErr("openai responses request", err)
	}
	c.Model, c.Stream = r.Model, r.Stream
	b := orBuilder{msgs: make([]model.Message, 0, len(r.Input)+1)}
	if r.Instructions != "" {
		b.msgs = append(b.msgs, model.Message{Role: model.RoleSystem, Text: r.Instructions})
	}
	for i := range r.Input {
		if err := b.add(&r.Input[i]); err != nil {
			return wrapRequestErr("openai responses request", err)
		}
	}
	c.Messages = b.finish()
	setMeta(c, model.MetaPreviousResponseID, r.PreviousResponseID)
	setMeta(c, model.MetaReasoningEffort, r.Reasoning.Effort)
	setMeta(c, model.MetaReasoningSummary, r.Reasoning.Summary)
	return nil
}

// setMeta records a non-empty request field, creating Meta when the caller
// left it nil.
func setMeta(c *model.Call, key, v string) {
	if v == "" {
		return
	}
	if c.Meta == nil {
		c.Meta = make(map[string]any, 3)
	}
	c.Meta[key] = v
}

// orBuilder turns input items into messages.
type orBuilder struct {
	msgs      []model.Message
	reasoning string // summary text waiting for its assistant message
	// openAsst is true when the last message is an assistant message that a
	// following function_call may extend.
	openAsst bool
}

func (b *orBuilder) add(it *orItem) error {
	switch it.Type {
	case "reasoning":
		if it.Summary.bad {
			return errContent
		}
		for _, p := range it.Summary.parts {
			b.reasoning += p.Text
		}
	case "function_call":
		b.toolCall(it.CallID, it.Name, json.RawMessage(it.Arguments))
	case "custom_tool_call":
		b.toolCall(it.CallID, it.Name, customInput(it.Input))
	case "local_shell_call":
		b.toolCall(it.CallID, localShellName, actionOf(it.Action))
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		text, err := it.Output.read()
		if err != nil {
			return err
		}
		id := it.CallID
		if id == "" {
			id = it.ID
		}
		b.push(model.Message{Role: model.RoleTool, Text: text, ToolCallID: id}, false)
	case "message", "":
		if it.Role == "" { // an item without a role or type is not a message
			return nil
		}
		text, err := it.Content.read()
		if err != nil {
			return err
		}
		role := openAIRole(it.Role)
		b.push(model.Message{Role: role, Text: text}, role == model.RoleAssistant)
	}
	return nil
}

// localShellName is the tool name given to local_shell_call items, which
// carry an action and no name.
const localShellName = "local_shell"

func (b *orBuilder) toolCall(id, name string, args json.RawMessage) {
	a := b.assistant()
	a.ToolCalls = append(a.ToolCalls, model.ToolCall{ID: id, Name: name, Arguments: args})
}

// decodeRaw decodes raw into v; an absent or null field leaves v alone.
func decodeRaw(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// argumentsOf returns function_call arguments (a JSON text in a string).
func argumentsOf(raw json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return rawArguments(s)
}

// customInput returns a custom tool's freeform input as a JSON string, so
// the arguments stay valid JSON whatever the text is (a patch, for example).
func customInput(raw json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return nil
	}
	return quoteJSON(s)
}

func quoteJSON(s string) json.RawMessage {
	b, _ := json.Marshal(s) // a string always marshals
	return b
}

// actionOf returns a local_shell_call's action object as the arguments.
func actionOf(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

// push appends a message; a pending reasoning summary joins an assistant one.
func (b *orBuilder) push(m model.Message, assistant bool) {
	if assistant {
		m.Reasoning, b.reasoning = b.reasoning, ""
	}
	b.msgs = append(b.msgs, m)
	b.openAsst = assistant
}

// assistant returns the assistant message a function_call belongs to: the
// open one, or a new one. Either takes the pending reasoning.
func (b *orBuilder) assistant() *model.Message {
	if !b.openAsst {
		b.push(model.Message{Role: model.RoleAssistant}, true)
	}
	m := &b.msgs[len(b.msgs)-1]
	m.Reasoning, b.reasoning = m.Reasoning+b.reasoning, ""
	return m
}

// finish returns the messages; reasoning with no assistant message after it
// (a transcript that ends on it) becomes an assistant message of its own.
func (b *orBuilder) finish() []model.Message {
	if b.reasoning != "" {
		b.push(model.Message{Role: model.RoleAssistant}, true)
	}
	return b.msgs
}
