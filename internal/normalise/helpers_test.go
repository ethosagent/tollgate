package normalise

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// fixture loads a testkit fixture by name ("openai/chat-stream-text").
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := fx.Fixture(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// responseFixtures lists every upstream response fixture (not requests, not
// error bodies), so a new recording needs a golden file.
func responseFixtures() []string {
	var names []string
	for _, n := range fx.FixtureNames() {
		base := path.Base(n)
		if !strings.HasPrefix(base, "request-") && !strings.HasPrefix(base, "error-") {
			names = append(names, n)
		}
	}
	return names
}

// requestFixtures lists every request-body fixture.
func requestFixtures() []string {
	var names []string
	for _, n := range fx.FixtureNames() {
		if strings.HasPrefix(path.Base(n), "request-") {
			names = append(names, n)
		}
	}
	return names
}

func wireOf(name string) model.WireFormat {
	if fx.PathFor(name) == "/v1/messages" {
		return model.WireAnthropicMessages
	}
	return model.WireOpenAIChat
}

// feed maps SSE frames through m and returns all events.
func feed(t testing.TB, m EventMapper, frames [][]byte) []model.Event {
	t.Helper()
	var all []model.Event
	for _, f := range frames {
		ev, data := SplitFrame(f)
		evs, err := m.Frame(ev, data)
		if err != nil {
			t.Fatalf("Frame: %v", err)
		}
		all = append(all, evs...)
	}
	return all
}

// eventsOf maps a response fixture to events: SSE through the stateful
// mapper, JSON through MapBody.
func eventsOf(t testing.TB, name string) []model.Event {
	t.Helper()
	w, err := ForWire(wireOf(name))
	if err != nil {
		t.Fatal(err)
	}
	raw := fixture(t, name)
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		evs, err := w.MapBody(raw)
		if err != nil {
			t.Fatalf("MapBody(%s): %v", name, err)
		}
		return evs
	}
	return feed(t, w.NewEventMapper(), fx.Chunks(name))
}

// folded is the final state a consumer would derive from an event sequence.
type folded struct {
	Text, Reasoning string
	Tools           map[int]*model.ToolCallDelta
	Usage           model.Usage
	Finish          string
}

func fold(evs []model.Event) folded {
	f := folded{Tools: map[int]*model.ToolCallDelta{}}
	for _, e := range evs {
		switch e.Kind {
		case model.EventTextDelta:
			f.Text += e.Text
		case model.EventReasoningDelta:
			f.Reasoning += e.Text
		case model.EventToolCallDelta:
			t := f.Tools[e.ToolCall.Index]
			if t == nil {
				t = &model.ToolCallDelta{Index: e.ToolCall.Index}
				f.Tools[t.Index] = t
			}
			if e.ToolCall.ID != "" {
				t.ID = e.ToolCall.ID
			}
			if e.ToolCall.Name != "" {
				t.Name = e.ToolCall.Name
			}
			t.Arguments += e.ToolCall.Arguments
		case model.EventUsage:
			f.Usage = *e.Usage
		case model.EventFinish:
			f.Finish = e.FinishReason
		}
	}
	return f
}

// compactJSON normalises whitespace so streamed and whole arguments compare.
func compactJSON(t testing.TB, s string) string {
	t.Helper()
	if s == "" {
		return ""
	}
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("compact %q: %v", s, err)
	}
	return b.String()
}
