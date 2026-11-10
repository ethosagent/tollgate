package normalise

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ethosagent/tollgate/internal/fixtures"
	"github.com/ethosagent/tollgate/internal/model"
)

func BenchmarkParseRequest(b *testing.B) {
	for _, name := range requestFixtures() {
		w, _ := ForWire(wireOf(name))
		body := fixture(b, name)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var c model.Call
				if err := w.ParseRequest(body, &c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEventMapper(b *testing.B) {
	for _, name := range []string{"openai/chat-stream-tools", "openai/responses-stream-tools",
		"openai/chat-stream-text", "openai/responses-stream-text", "anthropic/messages-stream-thinking"} {
		w, _ := ForWire(wireOf(name))
		frames := fixtures.Chunks(name)
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m := w.NewEventMapper()
				for _, f := range frames {
					ev, data := SplitFrame(f)
					if _, err := m.Frame(ev, data); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkParseTranscript parses the same 100-turn agent transcript (user
// text, one tool call, its output, per turn) on both OpenAI wires: the
// like-for-like cost of the Responses request parser against chat. The plan's
// fixtures differ in size (the Responses tools fixture is 2.5x the bytes), so
// this is the fair ratio.
func BenchmarkParseTranscript(b *testing.B) {
	const turns = 100
	var chat, resp strings.Builder
	chat.WriteString(`{"model":"gpt-5","stream":true,"messages":[`)
	resp.WriteString(`{"model":"gpt-5","stream":true,"input":[`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			chat.WriteByte(',')
			resp.WriteByte(',')
		}
		id := strconv.Itoa(i)
		chat.WriteString(`{"role":"user","content":"Please look at file number ` + id + ` and summarise what it does."},` +
			`{"role":"assistant","content":null,"tool_calls":[{"id":"call_` + id + `","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"src/file` + id + `.go\"}"}}]},` +
			`{"role":"tool","tool_call_id":"call_` + id + `","content":"package main\n\nfunc main() {}\n"}`)
		resp.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"Please look at file number ` + id + ` and summarise what it does."}]},` +
			`{"type":"function_call","id":"fc_` + id + `","call_id":"call_` + id + `","name":"read_file","arguments":"{\"path\":\"src/file` + id + `.go\"}"},` +
			`{"type":"function_call_output","call_id":"call_` + id + `","output":"package main\n\nfunc main() {}\n"}`)
	}
	chat.WriteString("]}")
	resp.WriteString("]}")
	for _, tc := range []struct {
		name string
		wire model.WireFormat
		body string
	}{{"chat", model.WireOpenAIChat, chat.String()}, {"responses", model.WireOpenAIResponses, resp.String()}} {
		w, _ := ForWire(tc.wire)
		body := []byte(tc.body)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				var c model.Call
				if err := w.ParseRequest(body, &c); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
