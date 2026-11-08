package normalise

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

func TestParseRequest_ResponsesFixtures(t *testing.T) {
	sys := model.Message{Role: model.RoleSystem, Text: "You are a coding agent."}
	tests := []struct {
		fixture string
		model   string
		want    []model.Message
		meta    map[string]any
	}{
		{"openai/request-responses-stream", "gpt-4.1-mini",
			[]model.Message{sys, {Role: model.RoleUser, Text: "Say hello."}}, map[string]any{}},
		{"openai/request-responses-tools", "o4-mini", []model.Message{sys,
			{Role: model.RoleUser, Text: "What is the weather in Paris?"},
			{Role: model.RoleAssistant, Reasoning: "Call the weather tool.", ToolCalls: []model.ToolCall{
				{ID: "call_fixture002", Name: "get_weather", Arguments: []byte(`{"city":"Paris"}`)}}},
			{Role: model.RoleTool, Text: `{"temp_c":14}`, ToolCallID: "call_fixture002"},
			{Role: model.RoleUser, Text: "And in Rome?"}},
			map[string]any{model.MetaPreviousResponseID: "resp_fixture000",
				model.MetaReasoningEffort: "medium", model.MetaReasoningSummary: "auto"}},
		{"openai/request-responses-custom", "gpt-5-codex", []model.Message{sys,
			{Role: model.RoleUser, Text: "Create hello.txt"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
				{ID: "call_fixture008", Name: "apply_patch", Arguments: []byte(customPatchJSON)}}},
			{Role: model.RoleTool, Text: "Success", ToolCallID: "call_fixture008"},
			{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
				{ID: "call_fixture009", Name: "local_shell", Arguments: []byte(`{"type":"exec","command":["ls","-la"],"env":{}}`)}}},
			{Role: model.RoleTool, Text: `{"output":"hello.txt"}`, ToolCallID: "call_fixture009"},
			{Role: model.RoleUser, Text: "Now list the files."}}, map[string]any{}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			c := parse(t, model.WireOpenAIResponses, string(fixture(t, tc.fixture)))
			if c.Model != tc.model || !c.Stream || c.Choices != 0 {
				t.Fatalf("model=%q stream=%v choices=%d", c.Model, c.Stream, c.Choices)
			}
			if !reflect.DeepEqual(c.Messages, tc.want) {
				t.Fatalf("messages:\n got %+v\nwant %+v", c.Messages, tc.want)
			}
			if len(c.Meta) != len(tc.meta) || (len(tc.meta) > 0 && !reflect.DeepEqual(c.Meta, tc.meta)) {
				t.Fatalf("meta = %v, want %v", c.Meta, tc.meta)
			}
		})
	}
}

func TestParseRequest_ResponsesItems(t *testing.T) {
	const body = `{"model":"m","input":[
	 {"role":"developer","content":"be brief"},
	 {"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"x"},{"type":"input_text","text":"b"}]},
	 {"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},
	 {"type":"function_call","call_id":"c1","name":"f","arguments":"{\"x\":1}"},
	 {"type":"function_call","call_id":"c2","name":"g","arguments":""},
	 {"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"r"},{"type":"input_text","text":"1"}]},
	 {"type":"function_call_output","call_id":"c2","output":"r2"},
	 {"type":"item_reference","id":"msg_x"},
	 {"type":"reasoning","summary":[{"type":"summary_text","text":"last"}]}]}`
	c := parse(t, model.WireOpenAIResponses, body)
	want := []model.Message{
		{Role: model.RoleSystem, Text: "be brief"},
		{Role: model.RoleUser, Text: "ab"},
		{Role: model.RoleAssistant, Text: "ok", ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "f", Arguments: []byte(`{"x":1}`)}, {ID: "c2", Name: "g"}}},
		{Role: model.RoleTool, Text: "r1", ToolCallID: "c1"},
		{Role: model.RoleTool, Text: "r2", ToolCallID: "c2"},
		{Role: model.RoleAssistant, Reasoning: "last"},
	}
	if c.Stream || !reflect.DeepEqual(c.Messages, want) {
		t.Fatalf("got %+v\nwant %+v", c.Messages, want)
	}
}

func TestParseRequest_ResponsesMalformed(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	for name, body := range map[string]string{
		"empty": ``, "not json": `{`, "input is number": `{"input":5}`,
		"input is object": `{"input":{}}`, "stream not bool": `{"stream":"yes"}`,
		"content is number": `{"input":[{"role":"user","content":5}]}`,
		"items not objects": `{"input":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var c model.Call
			if err := w.ParseRequest([]byte(body), &c); err == nil {
				t.Fatalf("want error, got %+v", c)
			}
		})
	}
}

func TestParseRequest_ResponsesLeavesMetaAloneWhenAbsent(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	c := model.Call{Meta: map[string]any{"keep": "me"}}
	if err := w.ParseRequest([]byte(`{"model":"m","input":"hi","previous_response_id":"resp_fixture9"}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Meta["keep"] != "me" || c.Meta[model.MetaPreviousResponseID] != "resp_fixture9" {
		t.Fatalf("meta = %v", c.Meta)
	}
}

func TestEvents_ResponsesContent(t *testing.T) {
	tests := []struct {
		fixture string
		want    folded
	}{
		{"openai/responses-stream-text", folded{Text: "Hello world", Finish: "completed", Usage: model.Usage{InputTokens: 12, OutputTokens: 2}}},
		{"openai/responses-stream-reasoning", folded{Text: "Hello", Reasoning: "The user wants a greeting.", Finish: "completed",
			Usage: model.Usage{InputTokens: 12, OutputTokens: 9, ReasoningTokens: 6}}},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			got := fold(eventsOf(t, tc.fixture))
			if got.Text != tc.want.Text || got.Reasoning != tc.want.Reasoning ||
				got.Finish != tc.want.Finish || got.Usage != tc.want.Usage {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
	got := fold(eventsOf(t, "openai/responses-stream-tools"))
	tc := got.Tools[0]
	if len(got.Tools) != 1 || tc.Name != "get_weather" || tc.ID != "call_fixture001" ||
		tc.Arguments != `{"city":"Paris"}` || got.Usage.InputTokens != 40 || got.Usage.OutputTokens != 11 {
		t.Fatalf("got %+v tool %+v", got, tc)
	}
}

// TestResponses_EquivalentToChatOnSharedSemantics: the same conversation over
// the two OpenAI wires folds to the same text, reasoning, tool call and
// usage. Only the finish reason differs (chat "stop"/"tool_calls", Responses
// "completed"), as it does between chat and Anthropic.
func TestResponses_EquivalentToChatOnSharedSemantics(t *testing.T) {
	for _, tc := range []struct{ chat, resp string }{
		{"openai/chat-stream-text", "openai/responses-stream-text"},
		{"openai/chat-stream-tools", "openai/responses-stream-tools"},
		{"openai/chat-stream-reasoning", "openai/responses-stream-reasoning"},
	} {
		t.Run(tc.resp, func(t *testing.T) {
			c, r := fold(eventsOf(t, tc.chat)), fold(eventsOf(t, tc.resp))
			if c.Finish == "" || r.Finish == "" {
				t.Fatal("both wires must report a finish reason")
			}
			c.Finish, r.Finish = "", ""
			equalFolded(t, r, c)
		})
	}
}

func TestMapBody_ResponsesMatchesStreamedEquivalent(t *testing.T) {
	const toolBody = `{"id":"resp_fixture002","object":"response","status":"completed","output":[` +
		`{"id":"fc_fixture001","type":"function_call","status":"completed","call_id":"call_fixture001",` +
		`"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}],` +
		`"usage":{"input_tokens":40,"output_tokens":11,"total_tokens":51}}`
	const reasonBody = `{"id":"resp_fixture003","object":"response","status":"completed","output":[` +
		`{"id":"rs_fixture001","type":"reasoning","summary":[{"type":"summary_text","text":"The user wants a greeting."}]},` +
		`{"id":"msg_fixture003","type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],` +
		`"usage":{"input_tokens":12,"output_tokens":9,"output_tokens_details":{"reasoning_tokens":6}}}`
	tests := []struct {
		name, stream string
		body         func(t *testing.T) []byte
	}{
		{"text", "openai/responses-stream-text", func(t *testing.T) []byte { return fixture(t, "openai/responses") }},
		{"tools", "openai/responses-stream-tools", func(*testing.T) []byte { return []byte(toolBody) }},
		{"reasoning", "openai/responses-stream-reasoning", func(*testing.T) []byte { return []byte(reasonBody) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(model.WireOpenAIResponses)
			evs, err := w.MapBody(tc.body(t))
			if err != nil {
				t.Fatal(err)
			}
			equalFolded(t, fold(evs), fold(eventsOf(t, tc.stream)))
		})
	}
}

func TestMapBody_ResponsesStatuses(t *testing.T) {
	tests := []struct {
		name, body, finish, err string
	}{
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, "max_output_tokens", ""},
		{"incomplete no reason", `{"status":"incomplete","output":[]}`, "incomplete", ""},
		{"failed", `{"status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`, "failed", "boom"},
		{"error object", `{"error":{"message":"bad key"}}`, "", "bad key"},
		{"in progress", `{"status":"in_progress","output":[]}`, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(model.WireOpenAIResponses)
			evs, err := w.MapBody([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			var errMsg string
			for _, e := range evs {
				if e.Kind == model.EventError {
					errMsg = e.Err
				}
			}
			if f := fold(evs); f.Finish != tc.finish || errMsg != tc.err {
				t.Fatalf("finish=%q err=%q, want %q %q", f.Finish, errMsg, tc.finish, tc.err)
			}
		})
	}
	w, _ := ForWire(model.WireOpenAIResponses)
	for _, bad := range []string{``, `{`, `{"output":5}`} {
		if _, err := w.MapBody([]byte(bad)); err == nil {
			t.Errorf("MapBody(%q): want error", bad)
		}
	}
}

func TestMapFrame_ResponsesTerminalFrames(t *testing.T) {
	tests := []struct {
		name, event, data, finish, err string
		usage                          model.Usage
	}{
		{"incomplete", "response.incomplete", `{"type":"response.incomplete","response":{"status":"incomplete",` +
			`"incomplete_details":{"reason":"content_filter"},"usage":{"input_tokens":5,"output_tokens":7}}}`,
			"content_filter", "", model.Usage{InputTokens: 5, OutputTokens: 7}},
		{"failed", "response.failed", `{"type":"response.failed","response":{"status":"failed",` +
			`"error":{"code":"rate_limit_exceeded","message":"slow down"}}}`, "failed", "slow down", model.Usage{}},
		{"failed without message", "response.failed", `{"type":"response.failed","response":{"error":{"code":"server_error"}}}`,
			"failed", "server_error", model.Usage{}},
		{"failed with a numeric code keeps its usage", "response.failed", `{"type":"response.failed","response":{"status":"failed",` +
			`"error":{"code":502},"usage":{"input_tokens":4,"output_tokens":1}}}`, "failed", "502", model.Usage{InputTokens: 4, OutputTokens: 1}},
		{"error event with a numeric code", "error", `{"type":"error","code":429}`, "", "429", model.Usage{}},
		{"error event", "error", `{"type":"error","code":"server_error","message":"oops"}`, "", "oops", model.Usage{}},
		{"nameless frame uses the JSON type", "", `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2,` +
			`"input_tokens_details":{"cached_tokens":1}}}}`, "completed", "", model.Usage{InputTokens: 1, OutputTokens: 2, CachedInputTokens: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, _ := ForWire(model.WireOpenAIResponses)
			m := w.NewEventMapper()
			evs, err := m.Frame(tc.event, []byte(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			var errMsg string
			for _, e := range evs {
				if e.Kind == model.EventError {
					errMsg = e.Err
				}
			}
			f := fold(evs)
			if f.Finish != tc.finish || errMsg != tc.err || f.Usage != tc.usage {
				t.Fatalf("finish=%q err=%q usage=%+v, want %q %q %+v", f.Finish, errMsg, f.Usage, tc.finish, tc.err, tc.usage)
			}
			if got := m.Finish(); len(got) != 0 {
				t.Fatalf("Finish after a terminal frame = %+v", got)
			}
		})
	}
}

func TestMapFrame_ResponsesRecordedMidStreamError(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	m := w.NewEventMapper()
	evs := feed(t, m, fixtures.Chunks("openai/responses-stream-error"))
	last := evs[len(evs)-1]
	if fold(evs).Text != "Hel" || last.Kind != model.EventError || last.Err != "The server had an error while processing your request." {
		t.Fatalf("events = %+v", evs)
	}
	if got := m.Finish(); len(got) != 0 {
		t.Fatalf("Finish after an error event = %+v", got)
	}
}

func TestMapFrame_ResponsesTruncatedStream(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	m := w.NewEventMapper()
	feed(t, m, fixtures.Chunks("openai/responses-stream-text")[:5])
	if got := m.Finish(); len(got) != 1 || got[0].Kind != model.EventError {
		t.Fatalf("Finish on a truncated stream = %+v", got)
	}
	if got := m.Finish(); got != nil {
		t.Fatalf("Finish is not idempotent: %+v", got)
	}
}

func TestMapFrame_ResponsesIgnoresUnknownAndHandlesMalformed(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	m := w.NewEventMapper()
	for _, f := range [][2]string{
		{"response.created", `{"type":"response.created"`}, // skipped by name, never decoded
		{"response.web_search_call.searching", `{"type":"response.web_search_call.searching"}`},
		{"", `{"type":"response.future_event","delta":"x"}`},
		{"", ``}, {"", `[DONE]`},
	} {
		if evs, err := m.Frame(f[0], []byte(f[1])); err != nil || len(evs) != 0 {
			t.Errorf("Frame(%q, %q) = %+v, %v; want nothing", f[0], f[1], evs, err)
		}
	}
	if _, err := m.Frame("response.output_text.delta", []byte(`{"type":`)); err == nil {
		t.Error("malformed delta frame: want error")
	}
}

func TestMapFrame_ResponsesToolOrdinalsFollowOutputIndex(t *testing.T) {
	w, _ := ForWire(model.WireOpenAIResponses)
	evs := feed(t, w.NewEventMapper(), sseData(
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"a","name":"f","arguments":""}}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","call_id":"b","name":"g","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":3,"delta":"{\"y\":2}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"x\":1}"}`,
	))
	assertTools(t, evs, []int{0, 1, 1, 0}, twoTools)
}

func TestSummarizer_Responses(t *testing.T) {
	s := summarise(t, model.WireOpenAIResponses, "openai/responses-stream-text.sse")
	if s.FinishReason != "completed" || s.Usage != (model.Usage{InputTokens: 12, OutputTokens: 2}) || s.Events == 0 || len(s.ToolCalls) != 0 {
		t.Fatalf("text summary = %+v", s)
	}
	s = summarise(t, model.WireOpenAIResponses, "openai/responses-stream-tools.sse")
	if s.FinishReason != "completed" || s.Usage.OutputTokens != 11 || len(s.ToolCalls) != 1 {
		t.Fatalf("tool summary = %+v", s)
	}
	if tc := s.ToolCalls[0]; tc.ID != "call_fixture001" || tc.Name != "get_weather" || string(tc.Arguments) != `{"city":"Paris"}` {
		t.Fatalf("tool call = %+v (%s)", tc, tc.Arguments)
	}
	s = summarise(t, model.WireOpenAIResponses, "openai/responses-stream-reasoning.sse")
	if s.Usage.ReasoningTokens != 6 || s.FinishReason != "completed" {
		t.Fatalf("reasoning summary = %+v", s)
	}
}

func TestTerminal_ResponsesOpens(t *testing.T) {
	cases := []struct {
		name, event, data string
		want              bool
	}{
		{"text delta", "response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`, false},
		{"output item done", "response.output_item.done", `{"type":"response.output_item.done"}`, false},
		{"created", "response.created", `{"type":"response.created"}`, false},
		{"completed", "response.completed", `{"type":"response.completed"}`, true},
		{"incomplete", "response.incomplete", `{"type":"response.incomplete"}`, true},
		{"failed", "response.failed", `{"type":"response.failed"}`, true},
		{"nameless completed", "", `{"type":"response.completed","response":{}}`, true},
		{"nameless text quoting completed", "", `{"type":"response.output_text.delta","delta":"{\"type\":\"response.completed\""}`, false},
		{"nameless without leading type", "", `{"sequence_number":3}`, false},
		{"error is not a region opener", "error", `{"type":"error"}`, false},
	}
	term, err := TerminalFor(model.WireOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := term.Opens(tc.event, []byte(tc.data)); got != tc.want {
				t.Fatalf("Opens = %v, want %v", got, tc.want)
			}
		})
	}
	nameless := []byte(`{"type":"response.completed"}`)
	if n := testing.AllocsPerRun(100, func() { term.Opens("", nameless) }); n != 0 {
		t.Fatalf("Opens allocated %v times, want 0", n)
	}
}

func TestSplicer_ResponsesOneResponseFromTwoStreams(t *testing.T) {
	term, _ := TerminalFor(model.WireOpenAIResponses)
	sp := term.NewSplicer()
	for _, f := range [][]byte{
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`),
		frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"delta":"x"}`),
		frame("response.output_item.added", `{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call"}}`),
	} {
		ev, data := SplitFrame(f)
		sp.Sent(ev, data)
	}
	sp.Begin()
	for _, name := range []string{"response.created", "response.in_progress"} {
		if _, keep := recoverFrame(sp, frame(name, `{"type":"`+name+`"}`)); keep {
			t.Fatalf("%s of the recovery stream must be dropped", name)
		}
	}
	out, keep := recoverFrame(sp, frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"message","note":"output_index\":9"}}`))
	if !keep || !strings.Contains(string(out), `"output_index":2`) || !strings.HasPrefix(string(out), "event: response.output_item.added\ndata: ") ||
		!strings.Contains(string(out), `"note":"output_index\":9"`) {
		t.Fatalf("recovery frame not shifted: keep=%v %s", keep, out)
	}
	delta := frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":0,"delta":"y"}`)
	if out, _ := recoverFrame(sp, delta); !strings.Contains(string(out), `"output_index":2`) {
		t.Fatalf("delta not shifted: %s", out)
	}
	sp.Begin() // a second recovery continues after the first one
	if out, _ := recoverFrame(sp, frame("response.output_item.added", `{"type":"response.output_item.added","output_index":0}`)); !strings.Contains(string(out), `"output_index":3`) {
		t.Fatalf("second recovery not offset after the first: %s", out)
	}
	other := frame("response.refusal.delta", `{"type":"response.refusal.delta","delta":"no"}`)
	if out, keep := recoverFrame(sp, other); !keep || string(out) != string(other) {
		t.Fatalf("frame without output_index changed: keep=%v %q", keep, out)
	}
	bad := frame("response.output_text.delta", `{"output_index":`)
	if out, keep := recoverFrame(sp, bad); !keep || string(out) != string(bad) {
		t.Fatalf("undecodable frame must pass unchanged: keep=%v %q", keep, out)
	}
}

func FuzzResponsesEvents(f *testing.F) { runFuzz(f, model.WireOpenAIResponses, "openai/responses") }
