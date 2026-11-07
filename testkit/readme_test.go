package testkit_test

import (
	"context"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// auditHook is the extension in the README's "Testing your extension".
type auditHook struct{ tollgate.NoopHook }

func (auditHook) Name() string { return "audit" }

func (auditHook) Before(_ context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	c.Meta = map[string]any{"audited": true}
	return nil, nil
}

// TestReadmeExample keeps the README snippet compiling and true.
func TestReadmeExample(t *testing.T) {
	rec := testkit.Run(t, auditHook{}, "openai/chat-stream-text", "anthropic/messages")

	before := rec.Of(testkit.StageBefore)
	if len(before) != 2 || before[1].Call.Model != "claude-sonnet-4-5" {
		t.Fatalf("Before saw %d calls: %+v", len(before), before)
	}
	for _, a := range rec.Of(testkit.StageAfter) {
		if a.Metric.Finish != tollgate.FinishOK {
			t.Errorf("%s ended %q", a.Call.Model, a.Metric.Finish)
		}
	}
	if got := rec.Replies[0].Status; got != 200 {
		t.Fatalf("client status = %d", got)
	}
}
