package normalise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// oaContent is a message content: a string, an array of typed parts, or null.
type oaContent string

func (c *oaContent) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		return nil
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = oaContent(s)
	case len(b) > 0 && b[0] == '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &parts); err != nil {
			return err
		}
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Text)
		}
		*c = oaContent(sb.String())
	default:
		return errors.New("message content must be a string, array or null")
	}
	return nil
}

type oaRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role             string       `json:"role"`
		Content          oaContent    `json:"content"`
		ReasoningContent string       `json:"reasoning_content"`
		ToolCalls        []oaToolCall `json:"tool_calls"`
		ToolCallID       string       `json:"tool_call_id"`
	} `json:"messages"`
}

// ParseRequest fills Model, Stream and Messages from a chat.completions body.
func (w openAIWire) ParseRequest(body []byte, c *model.Call) error {
	var r oaRequest
	if err := w.decode(body, &r); err != nil {
		return fmt.Errorf("openai request: %w", err)
	}
	c.Model, c.Stream = r.Model, r.Stream
	c.Messages = make([]model.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		msg := model.Message{Role: openAIRole(m.Role), Text: string(m.Content),
			Reasoning: m.ReasoningContent, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
				ID: tc.ID, Name: tc.Function.Name, Arguments: rawArguments(tc.Function.Arguments)})
		}
		c.Messages = append(c.Messages, msg)
	}
	return nil
}

func openAIRole(r string) model.Role {
	switch r {
	case "developer", "system":
		return model.RoleSystem
	case "function", "tool":
		return model.RoleTool
	}
	return model.Role(r)
}
