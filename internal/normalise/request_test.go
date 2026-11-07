package normalise

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

func parse(t *testing.T, wire model.WireFormat, body string) model.Call {
	t.Helper()
	w, err := ForWire(wire)
	if err != nil {
		t.Fatal(err)
	}
	var c model.Call
	if err := w.ParseRequest([]byte(body), &c); err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	return c
}

func TestParseRequest_Fixtures(t *testing.T) {
	sys := model.Message{Role: model.RoleSystem, Text: "You are a coding agent."}
	tests := []struct {
		fixture string
		model   string
		stream  bool
		want    []model.Message
	}{
		{"openai/request-chat-stream", "gpt-4.1-mini", true,
			[]model.Message{sys, {Role: model.RoleUser, Text: "Say hello."}}},
		{"openai/request-chat-tools", "gpt-4.1-mini", true,
			[]model.Message{sys, {Role: model.RoleUser, Text: "What is the weather in Paris?"}}},
		{"anthropic/request-messages-stream", "claude-sonnet-5-5", true,
			[]model.Message{sys, {Role: model.RoleUser, Text: "Say hello."}}},
		{"anthropic/request-messages-tools", "claude-sonnet-5-5", true,
			[]model.Message{sys, {Role: model.RoleUser, Text: "What is the weather in Paris?"}}},
		{"anthropic/request-agent-other", "claude-sonnet-5-5", false, agentMessages(0, "Summarise the README.")},
	}
	for step := 1; step <= 6; step++ {
		tests = append(tests, struct {
			fixture string
			model   string
			stream  bool
			want    []model.Message
		}{fmt.Sprintf("anthropic/request-agent-step-%d", step), "claude-sonnet-5-5", false, agentMessages(step-1, agentTask)})
	}
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.fixture] = true
		t.Run(tc.fixture, func(t *testing.T) {
			c := parse(t, wireOf(tc.fixture), string(fixture(t, tc.fixture)))
			if c.Model != tc.model || c.Stream != tc.stream {
				t.Fatalf("got model=%q stream=%v, want %q %v", c.Model, c.Stream, tc.model, tc.stream)
			}
			if !reflect.DeepEqual(c.Messages, tc.want) {
				t.Fatalf("messages:\n got %+v\nwant %+v", c.Messages, tc.want)
			}
		})
	}
	for _, name := range requestFixtures() {
		if !covered[name] {
			t.Errorf("request fixture %s has no expected Call in this table", name)
		}
	}
}

func TestParseRequest_ConversationOpenAI(t *testing.T) {
	const body = `{"model":"m","stream":false,"messages":[
	 {"role":"developer","content":"be brief"},
	 {"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]},
	 {"role":"assistant","content":null,"reasoning_content":"thinking","tool_calls":[
	   {"id":"c1","type":"function","function":{"name":"f","arguments":"{\"x\":1}"}},
	   {"id":"c2","type":"function","function":{"name":"g","arguments":""}}]},
	 {"role":"tool","tool_call_id":"c1","content":"result"},
	 {"role":"user","content":"thanks"}]}`
	c := parse(t, model.WireOpenAIChat, body)
	want := []model.Message{
		{Role: model.RoleSystem, Text: "be brief"},
		{Role: model.RoleUser, Text: "ab"},
		{Role: model.RoleAssistant, Reasoning: "thinking", ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "f", Arguments: []byte(`{"x":1}`)}, {ID: "c2", Name: "g"}}},
		{Role: model.RoleTool, Text: "result", ToolCallID: "c1"},
		{Role: model.RoleUser, Text: "thanks"},
	}
	if c.Model != "m" || c.Stream || !reflect.DeepEqual(c.Messages, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestParseRequest_Choices(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"default", `{"model":"m","messages":[]}`, 0},
		{"one", `{"model":"m","n":1,"messages":[]}`, 1},
		{"several", `{"model":"m","n":3,"stream":true,"messages":[]}`, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if c := parse(t, model.WireOpenAIChat, tc.body); c.Choices != tc.want {
				t.Fatalf("Choices = %d, want %d", c.Choices, tc.want)
			}
		})
	}
}

func TestParseRequest_ConversationAnthropic(t *testing.T) {
	const body = `{"model":"m","stream":true,"system":[{"type":"text","text":"s1"},{"type":"text","text":"s2"}],"messages":[
	 {"role":"user","content":"hi"},
	 {"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"ok"},
	   {"type":"tool_use","id":"t1","name":"f","input":{"x":1}},{"type":"tool_use","id":"t2","name":"g","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"},
	   {"type":"tool_result","tool_use_id":"t2","content":[{"type":"text","text":"r"},{"type":"text","text":"2"}]}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t3","content":"r3"},{"type":"text","text":"and more"}]}]}`
	c := parse(t, model.WireAnthropicMessages, body)
	want := []model.Message{
		{Role: model.RoleSystem, Text: "s1s2"},
		{Role: model.RoleUser, Text: "hi"},
		{Role: model.RoleAssistant, Text: "ok", Reasoning: "hm", ToolCalls: []model.ToolCall{
			{ID: "t1", Name: "f", Arguments: []byte(`{"x":1}`)}, {ID: "t2", Name: "g", Arguments: []byte(`{}`)}}},
		{Role: model.RoleTool, Text: "r1", ToolCallID: "t1"},
		{Role: model.RoleTool, Text: "r2", ToolCallID: "t2"},
		{Role: model.RoleTool, Text: "r3", ToolCallID: "t3"},
		{Role: model.RoleUser, Text: "and more"},
	}
	if c.Model != "m" || !c.Stream || !reflect.DeepEqual(c.Messages, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestParseRequest_Malformed(t *testing.T) {
	bodies := map[string]string{
		"empty":              ``,
		"whitespace":         "  \n",
		"not json":           `{`,
		"content is number":  `{"messages":[{"role":"user","content":5}]}`,
		"content is object":  `{"messages":[{"role":"user","content":{}}]}`,
		"messages not array": `{"messages":"x"}`,
		"stream not bool":    `{"stream":"yes"}`,
	}
	for _, wire := range []model.WireFormat{model.WireOpenAIChat, model.WireAnthropicMessages} {
		w, _ := ForWire(wire)
		for name, body := range bodies {
			t.Run(string(wire)+"/"+name, func(t *testing.T) {
				var c model.Call
				if err := w.ParseRequest([]byte(body), &c); err == nil {
					t.Fatalf("want error, got call %+v", c)
				}
			})
		}
	}
}
