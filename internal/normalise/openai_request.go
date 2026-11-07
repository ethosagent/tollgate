package normalise

import (
	"strings"

	"github.com/ethosagent/tollgate/internal/model"
)

// oaPart is one typed part of array content.
type oaPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// oaContent is a message content: a string, an array of typed parts, or null.
type oaContent []oaPart

func (c *oaContent) UnmarshalJSON(b []byte) error {
	parts, err := decodeContent(b, func(s string) oaPart { return oaPart{Type: "text", Text: s} })
	if err != nil {
		return err
	}
	*c = parts
	return nil
}

// text joins the text of every part.
func (c oaContent) text() string {
	if len(c) == 1 {
		return c[0].Text
	}
	var sb strings.Builder
	for _, p := range c {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

type oaRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	N        int    `json:"n"`
	Messages []struct {
		Role             string       `json:"role"`
		Content          oaContent    `json:"content"`
		ReasoningContent string       `json:"reasoning_content"`
		ToolCalls        []oaToolCall `json:"tool_calls"`
		ToolCallID       string       `json:"tool_call_id"`
	} `json:"messages"`
}

// ParseRequest fills Model, Stream, Choices and Messages from a
// chat.completions body.
func (w openAIWire) ParseRequest(body []byte, c *model.Call) error {
	var r oaRequest
	if err := strictDecode(body, &r); err != nil {
		return wrapRequestErr("openai request", err)
	}
	c.Model, c.Stream, c.Choices = r.Model, r.Stream, r.N
	c.Messages = make([]model.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		msg := model.Message{Role: openAIRole(m.Role), Text: m.Content.text(),
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
