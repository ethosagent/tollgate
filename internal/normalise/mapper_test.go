package normalise

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden event files (refused when CI=true)")

var kindNames = map[model.EventKind]string{
	model.EventTextDelta:      "text",
	model.EventReasoningDelta: "reasoning",
	model.EventToolCallDelta:  "tool_call",
	model.EventUsage:          "usage",
	model.EventFinish:         "finish",
	model.EventError:          "error",
}

// goldenEvent is the readable on-disk form of a model.Event.
type goldenEvent struct {
	Kind     string               `json:"kind"`
	Text     string               `json:"text,omitempty"`
	ToolCall *model.ToolCallDelta `json:"tool_call,omitempty"`
	Usage    *model.Usage         `json:"usage,omitempty"`
	Finish   string               `json:"finish,omitempty"`
	Err      string               `json:"err,omitempty"`
}

func encodeGolden(t testing.TB, evs []model.Event) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, e := range evs {
		g := goldenEvent{Kind: kindNames[e.Kind], Text: e.Text, ToolCall: e.ToolCall,
			Usage: e.Usage, Finish: e.FinishReason, Err: e.Err}
		line, err := json.Marshal(g)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

var errUpdateInCI = errors.New("-update is refused when CI=true")

// checkUpdateAllowed refuses golden rewrites in CI.
func checkUpdateAllowed(update bool, ci string) error {
	if update && ci == "true" {
		return errUpdateInCI
	}
	return nil
}

func TestCheckUpdateAllowed(t *testing.T) {
	tests := []struct {
		name   string
		update bool
		ci     string
		want   error
	}{
		{"update locally is allowed", true, "", nil},
		{"no update in CI is allowed", false, "true", nil},
		{"update in CI is refused", true, "true", errUpdateInCI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkUpdateAllowed(tc.update, tc.ci); !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalise_FixturesMatchGolden(t *testing.T) {
	if err := checkUpdateAllowed(*update, os.Getenv("CI")); err != nil {
		t.Fatal(err)
	}
	names := responseFixtures()
	if len(names) < 8 {
		t.Fatalf("expected at least 8 response fixtures, found %v", names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			got := encodeGolden(t, eventsOf(t, name))
			path := filepath.Join("testdata", name+".golden.jsonl")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden (run go test ./internal/normalise -update): %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("events differ from %s\n got:\n%s\nwant:\n%s", path, got, want)
			}
		})
	}
}

func TestForWire(t *testing.T) {
	for _, w := range []model.WireFormat{model.WireOpenAIChat, model.WireAnthropicMessages} {
		if _, err := ForWire(w); err != nil {
			t.Errorf("ForWire(%s): %v", w, err)
		}
	}
	if _, err := ForWire(model.WirePassthrough); !errors.Is(err, ErrUnsupportedWire) {
		t.Fatalf("passthrough: got %v, want ErrUnsupportedWire", err)
	}
}

func TestSplitFrame(t *testing.T) {
	tests := []struct {
		name, in, event, data string
	}{
		{"data only", "data: {\"a\":1}", "", `{"a":1}`},
		{"event and data", "event: ping\ndata: {}", "ping", "{}"},
		{"crlf", "event: x\r\ndata: y\r\n", "x", "y"},
		{"comment ignored", ": keepalive\ndata: z", "", "z"},
		{"multi data joins with newline", "data: a\ndata: b", "", "a\nb"},
		{"three data lines", "data: a\ndata: b\ndata: c", "", "a\nb\nc"},
		{"no space after colon", "data:q", "", "q"},
		{"empty", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.in)
			ev, data := SplitFrame(in)
			if ev != tc.event || string(data) != tc.data {
				t.Fatalf("got (%q,%q), want (%q,%q)", ev, data, tc.event, tc.data)
			}
			if string(in) != tc.in {
				t.Fatalf("SplitFrame changed its input: %q", in)
			}
		})
	}
}

// TestSplitFrame_OneDataLineCopiesNothing: the usual frame is split
// without an allocation (HOT-5); data aliases the frame.
func TestSplitFrame_OneDataLineCopiesNothing(t *testing.T) {
	frame := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`)
	if n := testing.AllocsPerRun(100, func() { SplitFrame(frame) }); n != 0 {
		t.Fatalf("SplitFrame allocated %v times per frame, want 0", n)
	}
}

func fuzzSeeds(f *testing.F, prefix string) {
	for _, name := range responseFixtures() {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			f.Add(fixture(f, name))
		}
	}
	f.Add([]byte("data: {"))
	f.Add([]byte("data: [DONE]"))
	f.Add([]byte("event: weird\ndata: {\"type\":\"nope\"}"))
}

func runFuzz(f *testing.F, wire model.WireFormat, prefix string) {
	fuzzSeeds(f, prefix)
	w, err := ForWire(wire)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(_ *testing.T, in []byte) {
		m := w.NewEventMapper()
		for _, fr := range bytes.Split(in, []byte("\n\n")) {
			ev, data := SplitFrame(fr)
			_, _ = m.Frame(ev, data) // errors are fine, panics are not
		}
		_, _ = m.Frame("", in)
		_, _ = w.MapBody(in)
		var c model.Call
		_ = w.ParseRequest(in, &c)
	})
}

func FuzzOpenAIEvents(f *testing.F)    { runFuzz(f, model.WireOpenAIChat, "openai/") }
func FuzzAnthropicEvents(f *testing.F) { runFuzz(f, model.WireAnthropicMessages, "anthropic/") }
