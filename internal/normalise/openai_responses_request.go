package normalise

import "github.com/ethosagent/tollgate/internal/model"

// orInput is a Responses "input": a bare string (one user message) or an
// array of items.
type orInput []orItem

func (in *orInput) UnmarshalJSON(b []byte) error {
	items, err := decodeContent(b, func(s string) orItem {
		return orItem{Type: "message", Role: "user", Content: oaContent{{Type: "input_text", Text: s}}}
	})
	if err != nil {
		return err
	}
	*in = items
	return nil
}

// orItem is one input item. Fields not listed (images, ids, status,
// annotations) are ignored. Item types the neutral model has no place for
// (item_reference, web_search_call, computer_call, ...) are skipped.
type orItem struct {
	Type      string    `json:"type"`
	Role      string    `json:"role"`
	Content   oaContent `json:"content"`
	CallID    string    `json:"call_id"`
	Name      string    `json:"name"`
	Arguments string    `json:"arguments"`
	Output    oaContent `json:"output"`
	Summary   []oaPart  `json:"summary"`
}

type orRequest struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	Instructions       string `json:"instructions"`
	PreviousResponseID string `json:"previous_response_id"`
	Reasoning          struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
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
		b.add(&r.Input[i])
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

func (b *orBuilder) add(it *orItem) {
	switch it.Type {
	case "reasoning":
		for _, p := range it.Summary {
			b.reasoning += p.Text
		}
	case "function_call":
		a := b.assistant()
		a.ToolCalls = append(a.ToolCalls, model.ToolCall{
			ID: it.CallID, Name: it.Name, Arguments: rawArguments(it.Arguments)})
	case "function_call_output":
		b.push(model.Message{Role: model.RoleTool, Text: it.Output.text(), ToolCallID: it.CallID}, false)
	case "message", "":
		if it.Role != "" { // an item without a role or type is not a message
			role := openAIRole(it.Role)
			b.push(model.Message{Role: role, Text: it.Content.text()}, role == model.RoleAssistant)
		}
	}
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
