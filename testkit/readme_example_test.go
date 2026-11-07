// This file is the example in README.md, "Testing your extension", verbatim
// after the package clause: TestReadme_ExampleIsThisFile fails if the two
// differ, so the README's code is the code that compiles and runs here.

package testkit_test

import (
	"context"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// auditHook is the extension under test: it tags every call in Before.
type auditHook struct{ tollgate.NoopHook }

func (auditHook) Name() string { return "audit" }

func (auditHook) Before(_ context.Context, c *tollgate.Call) (*tollgate.Response, error) {
	c.Meta = map[string]any{"audited": true}
	return nil, nil
}

func TestAuditHook(t *testing.T) {
	rec := testkit.Run(t, auditHook{}, "openai/chat-stream-text", "anthropic/messages")

	before := rec.Of(testkit.StageBefore) // what Before was given, per call
	if len(before) != 2 || before[1].Call.Model != "claude-sonnet-4-5" {
		t.Fatalf("Before saw %d calls: %+v", len(before), before)
	}
	if n := len(rec.Of(testkit.StageAfter)); n != 0 { // only the stages it implements
		t.Fatalf("After ran %d times; auditHook only implements Before", n)
	}
	if rec.Replies[0].Status != 200 { // what the client got
		t.Fatalf("client status = %d", rec.Replies[0].Status)
	}
}
