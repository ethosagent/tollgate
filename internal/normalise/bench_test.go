package normalise

import (
	"encoding/json"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// countingWire builds each wire with a decoder that counts top-level body
// decodes, so HOT-4 (parse once) is proven by a number, not a comment.
func countingWires(n *int) map[string]Wire {
	dec := func(b []byte, v any) error { *n++; return json.Unmarshal(b, v) }
	return map[string]Wire{
		"openai/request-chat-tools":         openAIWire{decode: dec},
		"anthropic/request-messages-tools":  anthropicWire{decode: dec},
		"openai/request-chat-stream":        openAIWire{decode: dec},
		"anthropic/request-messages-stream": anthropicWire{decode: dec},
	}
}

func TestParseRequest_DecodesBodyOnce(t *testing.T) {
	var n int
	for name, w := range countingWires(&n) {
		t.Run(name, func(t *testing.T) {
			n = 0
			var c model.Call
			if err := w.ParseRequest(readFixture(t, name), &c); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("body decoded %d times, want exactly 1", n)
			}
		})
	}
}

func BenchmarkParseRequest(b *testing.B) {
	var n int
	for name, w := range countingWires(&n) {
		body := readFixture(b, name)
		b.Run(name, func(b *testing.B) {
			n = 0
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var c model.Call
				if err := w.ParseRequest(body, &c); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(n)/float64(b.N), "decodes/op")
		})
	}
}

func BenchmarkEventMapper(b *testing.B) {
	for _, name := range []string{"openai/chat-stream-tools", "anthropic/messages-stream-thinking"} {
		w, _ := ForWire(wireOf(name))
		frames := sseFrames(readFixture(b, name))
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
