package normalise

import (
	"reflect"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// customPatchJSON is the input of the custom_tool_call fixtures as the JSON
// string the neutral model carries.
const customPatchJSON = `"*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch"`

// TestParseRequest_ResponsesUnmodelledItemsNeverFailTheParse: item types whose
// fields have another JSON type than the modelled ones (tool_search_call
// arguments, computer_call_output output, ...) must not reject the body, so
// hooks and metering still run (verification finding B1).
func TestParseRequest_ResponsesUnmodelledItemsNeverFailTheParse(t *testing.T) {
	user := model.Message{Role: model.RoleUser, Text: "hi"}
	items := map[string]string{
		"tool_search_call object arguments":  `{"type":"tool_search_call","call_id":"c","arguments":{"query":"x"}}`,
		"tool_search_output":                 `{"type":"tool_search_output","call_id":"c","tools":[{"type":"function"}]}`,
		"computer_call_output object output": `{"type":"computer_call_output","call_id":"c","output":{"type":"computer_screenshot","image_url":"x"}}`,
		"computer_call":                      `{"type":"computer_call","call_id":"c","actions":[{"type":"click"}]}`,
		"mcp_call string arguments":          `{"type":"mcp_call","id":"m","arguments":"{}","output":"ok"}`,
		"mcp_list_tools":                     `{"type":"mcp_list_tools","tools":[{"name":"t"}]}`,
		"web_search_call action object":      `{"type":"web_search_call","action":{"type":"search","query":"q"}}`,
		"image_generation_call":              `{"type":"image_generation_call","result":"AAAA"}`,
		"code_interpreter_call":              `{"type":"code_interpreter_call","outputs":[{"type":"logs","logs":"x"}]}`,
		"compaction":                         `{"type":"compaction","encrypted_content":"x"}`,
		"item_reference":                     `{"type":"item_reference","id":"msg_x"}`,
		"a future type with odd fields":      `{"type":"future_thing","arguments":[1],"output":5,"content":{},"summary":"s","input":7,"action":"a"}`,
		"shell_call_output array output":     `{"type":"shell_call_output","call_id":"c","output":[{"stdout":"x"}]}`,
	}
	w, _ := ForWire(model.WireOpenAIResponses)
	for name, item := range items {
		t.Run(name, func(t *testing.T) {
			var c model.Call
			body := `{"model":"gpt-5-codex","stream":true,"input":[{"role":"user","content":"hi"},` + item + `]}`
			if err := w.ParseRequest([]byte(body), &c); err != nil {
				t.Fatalf("ParseRequest: %v", err)
			}
			if c.Model != "gpt-5-codex" || !c.Stream || !reflect.DeepEqual(c.Messages, []model.Message{user}) {
				t.Fatalf("call = %q %v %+v", c.Model, c.Stream, c.Messages)
			}
		})
	}
}

func TestMapBody_ResponsesUnmodelledOutputItemsNeverFailTheMap(t *testing.T) {
	const body = `{"status":"completed","output":[` +
		`{"type":"tool_search_call","arguments":{"query":"x"}},` +
		`{"type":"computer_call","actions":[{"type":"click"}]},` +
		`{"type":"mcp_call","arguments":"{}","output":{"a":1}},` +
		`{"type":"message","content":"not an array"},` +
		`{"type":"function_call","call_id":"c","name":"f","arguments":{"odd":true}},` +
		`{"type":"message","content":[{"type":"output_text","text":"ok"}]}],` +
		`"usage":{"input_tokens":30,"output_tokens":9}}`
	w, _ := ForWire(model.WireOpenAIResponses)
	evs, err := w.MapBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if f := fold(evs); f.Text != "ok" || f.Finish != "completed" || f.Usage.InputTokens != 30 || f.Usage.OutputTokens != 9 {
		t.Fatalf("folded = %+v", f)
	}
}

func TestMapBody_ResponsesCustomAndShellToolCalls(t *testing.T) {
	const body = `{"status":"completed","output":[` +
		`{"type":"custom_tool_call","call_id":"call_fixture007","name":"apply_patch","input":"a\nb"},` +
		`{"type":"local_shell_call","call_id":"call_fixture009","action":{"type":"exec","command":["ls"]}}],` +
		`"usage":{"input_tokens":5,"output_tokens":6}}`
	w, _ := ForWire(model.WireOpenAIResponses)
	evs, err := w.MapBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	assertTools(t, evs, []int{0, 1}, map[int]model.ToolCallDelta{
		0: {ID: "call_fixture007", Name: "apply_patch", Arguments: `"a\nb"`},
		1: {ID: "call_fixture009", Name: "local_shell", Arguments: `{"type":"exec","command":["ls"]}`},
	})
}

// TestEvents_ResponsesCustomToolStream: a custom tool call (Codex apply_patch)
// becomes one tool call whose arguments are the input as a JSON string.
func TestEvents_ResponsesCustomToolStream(t *testing.T) {
	f := fold(eventsOf(t, "openai/responses-stream-custom-tool"))
	want := map[int]model.ToolCallDelta{0: {ID: "call_fixture007", Name: "apply_patch", Arguments: customPatchJSON}}
	if len(f.Tools) != 1 || *f.Tools[0] != want[0] || f.Finish != "completed" || f.Usage.OutputTokens != 30 {
		t.Fatalf("folded = %+v tool %+v", f, f.Tools[0])
	}
}

func TestSummarizer_ResponsesCustomToolStream(t *testing.T) {
	s := summarise(t, model.WireOpenAIResponses, "openai/responses-stream-custom-tool.sse")
	if len(s.ToolCalls) != 1 || s.ToolCalls[0].Name != "apply_patch" || string(s.ToolCalls[0].Arguments) != customPatchJSON {
		t.Fatalf("summary = %+v", s)
	}
}

func TestEvents_ResponsesFailedAndIncompleteStreams(t *testing.T) {
	f := fold(eventsOf(t, "openai/responses-stream-failed"))
	if f.Text != "Hel" || f.Finish != "failed" || f.Usage.InputTokens != 12 {
		t.Fatalf("failed = %+v", f)
	}
	f = fold(eventsOf(t, "openai/responses-stream-incomplete"))
	if f.Text != "Hel" || f.Finish != "max_output_tokens" || f.Usage.OutputTokens != 16 {
		t.Fatalf("incomplete = %+v", f)
	}
	if s := summarise(t, model.WireOpenAIResponses, "openai/responses-stream-failed.sse"); s.FinishReason != "failed" {
		t.Fatalf("summary finish = %q", s.FinishReason)
	}
}
