package normalise

import (
	"testing"

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
	for _, name := range []string{"openai/chat-stream-tools", "anthropic/messages-stream-thinking"} {
		w, _ := ForWire(wireOf(name))
		frames := fx.Chunks(name)
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
