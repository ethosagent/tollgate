package normalise

import (
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// TestOpenAI_MultiChoiceToolsDoNotCollide covers review-004: with n>1 two
// choices that both use tool index 0 are two tool calls, not one merged one.
func TestOpenAI_MultiChoiceToolsDoNotCollide(t *testing.T) {
	want := map[int]model.ToolCallDelta{
		0: {ID: "a", Name: "f", Arguments: `{"x":1}`},
		1: {ID: "b", Name: "g", Arguments: `{"y":2}`},
	}
	t.Run("stream", func(t *testing.T) {
		evs := feed(t, openAIWire{}.NewEventMapper(), sseData(
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"f","arguments":""}}]}},`+
				`{"index":1,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"g","arguments":""}}]}}]}`,
			`{"choices":[{"index":1,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"y\":2}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"x\":1}"}}]}}]}`,
		))
		assertTools(t, evs, []int{0, 1, 1, 0}, want)
	})
	t.Run("body", func(t *testing.T) {
		evs, err := openAIWire{}.MapBody([]byte(`{"choices":[` +
			`{"index":0,"message":{"tool_calls":[{"id":"a","function":{"name":"f","arguments":"{\"x\":1}"}}]}},` +
			`{"index":1,"message":{"tool_calls":[{"id":"b","function":{"name":"g","arguments":"{\"y\":2}"}}]}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		assertTools(t, evs, []int{0, 1}, want)
	})
}

// TestParse_DecodesBodyOnce restores the proof for HOT-4 / review-004: each
// request body and each stream:false body is JSON-decoded exactly once.
func TestParse_DecodesBodyOnce(t *testing.T) {
	var n int
	decodeObserver = func() { n++ }
	t.Cleanup(func() { decodeObserver = nil })
	for _, name := range requestFixtures() {
		w, _ := ForWire(wireOf(name))
		n = 0
		if err := w.ParseRequest(fixture(t, name), &model.Call{}); err != nil || n != 1 {
			t.Fatalf("%s: ParseRequest decoded %d times (err %v), want 1", name, n, err)
		}
	}
	for _, name := range []string{"openai/chat", "anthropic/messages"} {
		w, _ := ForWire(wireOf(name))
		n = 0
		if _, err := w.MapBody(fixture(t, name)); err != nil || n != 1 {
			t.Fatalf("%s: MapBody decoded %d times (err %v), want 1", name, n, err)
		}
	}
}

// TestEventMapper_GapSuppressesFalseTruncation: after the queue reported a
// gap, a missing finish frame may have been dropped rather than never sent,
// so Finish must not claim truncation; the queue's End.Finish says how the
// call really ended.
func TestEventMapper_GapSuppressesFalseTruncation(t *testing.T) {
	for _, name := range []string{"openai/chat-stream-text", "anthropic/messages-stream-text"} {
		t.Run(name, func(t *testing.T) {
			w, _ := ForWire(wireOf(name))
			frames := fx.Chunks(name)
			m := w.NewEventMapper()
			feed(t, m, frames[:2])
			m.Gap(3)
			if got := m.Finish(); len(got) != 0 {
				t.Fatalf("Finish after a gap = %+v, want nothing", got)
			}
		})
	}
}

// TestMapFrame_RecordedMidStreamError replays the recorded mid-stream error
// fixtures (add-provider step 1): partial text, then a terminal EventError,
// and Finish reports no extra truncation.
func TestMapFrame_RecordedMidStreamError(t *testing.T) {
	for name, wantErr := range map[string]string{
		"openai/chat-stream-error":        "The server had an error while processing your request.",
		"anthropic/messages-stream-error": "Overloaded",
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := ForWire(wireOf(name))
			m := w.NewEventMapper()
			evs := feed(t, m, fx.Chunks(name))
			last := evs[len(evs)-1]
			if fold(evs).Text != "Hel" || last.Kind != model.EventError || last.Err != wantErr {
				t.Fatalf("events = %+v", evs)
			}
			if got := m.Finish(); len(got) != 0 {
				t.Fatalf("Finish after an error event = %+v", got)
			}
		})
	}
}
