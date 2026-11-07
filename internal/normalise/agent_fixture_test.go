package normalise

import (
	"fmt"

	"github.com/ethosagent/tollgate/internal/model"
)

const agentTask = "Fix the failing TestParse in the repo."

// agentSteps are the tool calls of the recorded agent run behind the
// anthropic/request-agent-step-N fixtures: call N carries the first N-1.
var agentSteps = []struct{ tool, input, result string }{
	{"read_file", `{"path":"parse_test.go"}`, "func TestParse(t *testing.T) { want 3 got 2 }"},
	{"read_file", `{"path":"parse.go"}`, "func Parse(s string) int { return len(s)-1 }"},
	{"edit_file", `{"path":"parse.go","old":"len(s)-1","new":"len(s)"}`, "ok"},
	{"run_tests", `{"pkg":"./..."}`, "FAIL: TestFormat"},
	{"edit_file", `{"path":"format.go","old":"%d","new":"%03d"}`, "ok"},
}

// agentMessages is the neutral conversation of an agent-run request after
// done tool round trips, starting from task.
func agentMessages(done int, task string) []model.Message {
	msgs := []model.Message{
		{Role: model.RoleSystem, Text: "You are a coding agent. Use the tools to fix the failing test."},
		{Role: model.RoleUser, Text: task},
	}
	for i, s := range agentSteps[:done] {
		id := fmt.Sprintf("toolu_fixture_%d", i+1)
		msgs = append(msgs,
			model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: id, Name: s.tool, Arguments: []byte(s.input)}}},
			model.Message{Role: model.RoleTool, Text: s.result, ToolCallID: id})
	}
	return msgs
}
