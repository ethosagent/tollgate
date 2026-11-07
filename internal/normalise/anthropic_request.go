package normalise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// anReqBlock is one content block of a request message.
type anReqBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   *anContent      `json:"content"`
}

// anContent is a string or an array of blocks (system, message, tool_result).
type anContent []anReqBlock

func (c *anContent) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		return nil
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = anContent{{Type: "text", Text: s}}
	case len(b) > 0 && b[0] == '[':
		var blocks []anReqBlock
		if err := json.Unmarshal(b, &blocks); err != nil {
			return err
		}
		*c = blocks
	default:
		return errors.New("content must be a string, array or null")
	}
	return nil
}

func (c *anContent) text() string {
	if c == nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range *c {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

type anRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	System   anContent `json:"system"`
	Messages []struct {
		Role    string    `json:"role"`
		Content anContent `json:"content"`
	} `json:"messages"`
}

// ParseRequest fills Model, Stream and Messages from a messages body. The
// system prompt becomes the first RoleSystem message; tool_result blocks
// become RoleTool messages placed before the rest of their user turn.
func (w anthropicWire) ParseRequest(body []byte, c *model.Call) error {
	var r anRequest
	if err := w.decode(body, &r); err != nil {
		return fmt.Errorf("anthropic request: %w", err)
	}
	c.Model, c.Stream = r.Model, r.Stream
	c.Messages = make([]model.Message, 0, len(r.Messages)+1)
	if sys := (&r.System).text(); sys != "" {
		c.Messages = append(c.Messages, model.Message{Role: model.RoleSystem, Text: sys})
	}
	for _, m := range r.Messages {
		c.Messages = appendAnthropicMessage(c.Messages, m.Role, m.Content)
	}
	return nil
}

func appendAnthropicMessage(out []model.Message, role string, blocks anContent) []model.Message {
	msg := model.Message{Role: model.Role(role)}
	var text, reasoning strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			reasoning.WriteString(b.Thinking)
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{ID: b.ID, Name: b.Name, Arguments: b.Input})
		case "tool_result":
			out = append(out, model.Message{Role: model.RoleTool, Text: b.Content.text(), ToolCallID: b.ToolUseID})
		}
	}
	msg.Text, msg.Reasoning = text.String(), reasoning.String()
	if msg.Text == "" && msg.Reasoning == "" && len(msg.ToolCalls) == 0 && len(blocks) > 0 && hasToolResult(blocks) {
		return out
	}
	return append(out, msg)
}

func hasToolResult(blocks anContent) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}
