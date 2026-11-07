package chain

import (
	"context"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// BenchmarkEvents_Deliver measures the per-event cost of the OnEvent fan-out
// to two hooks (HOT-5).
func BenchmarkEvents_Deliver(b *testing.B) {
	log, _ := newLogger()
	ch := New([]model.Hook{&hook{name: "a"}, &hook{name: "b"}}, log, testLimits)
	d := ch.Events(newCall("x"))
	ctx, e := context.Background(), model.Event{Kind: model.EventTextDelta, Text: "hi"}
	b.ReportAllocs()
	for b.Loop() {
		d.Deliver(ctx, e)
	}
}
