package normalise

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

const fixtureDir = "../../testkit/testdata"

// readFixture loads a testkit fixture by name ("openai/chat-stream-text").
func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	for _, ext := range []string{".sse", ".json"} {
		if b, err := os.ReadFile(filepath.Join(fixtureDir, name+ext)); err == nil {
			return b
		}
	}
	t.Fatalf("fixture %q not found", name)
	return nil
}

// responseFixtures lists every upstream response fixture (not requests, not
// error bodies), so a new recording needs a golden file.
func responseFixtures(t testing.TB) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasPrefix(base, "request-") || strings.HasPrefix(base, "error-") {
			continue
		}
		rel, _ := filepath.Rel(fixtureDir, f)
		names = append(names, strings.TrimSuffix(filepath.ToSlash(rel), filepath.Ext(f)))
	}
	sort.Strings(names)
	return names
}

func wireOf(name string) model.WireFormat {
	if strings.HasPrefix(name, "anthropic/") {
		return model.WireAnthropicMessages
	}
	return model.WireOpenAIChat
}

// sseFrames splits an SSE stream into blank-line separated frames.
func sseFrames(b []byte) [][]byte {
	var out [][]byte
	for _, f := range bytes.Split(b, []byte("\n\n")) {
		if len(bytes.TrimSpace(f)) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// eventsOf maps a response fixture to events: SSE through the stateful
// mapper, JSON through MapBody.
func eventsOf(t testing.TB, name string) []model.Event {
	t.Helper()
	w, err := ForWire(wireOf(name))
	if err != nil {
		t.Fatal(err)
	}
	raw := readFixture(t, name)
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		evs, err := w.MapBody(raw)
		if err != nil {
			t.Fatalf("MapBody(%s): %v", name, err)
		}
		return evs
	}
	m := w.NewEventMapper()
	var all []model.Event
	for _, f := range sseFrames(raw) {
		ev, data := SplitFrame(f)
		evs, err := m.Frame(ev, data)
		if err != nil {
			t.Fatalf("Frame(%s): %v", name, err)
		}
		all = append(all, evs...)
	}
	return all
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
