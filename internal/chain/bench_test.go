package chain

import (
	"context"
	"strings"
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

// benchBody is a typical small prompt; Before and OnResponse copy it per hook.
var benchBody = []byte(`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 4000) + `"}]}`)

// BenchmarkChain_Before is the per-call cost of the Before stage with one
// rewriting hook: a deep copy, a goroutine and two timers (HOT-5, HOT-7).
func BenchmarkChain_Before(b *testing.B) {
	log, _ := newLogger()
	ch := New([]model.Hook{&hook{name: "a", before: func(context.Context, *model.Call) (*model.Response, error) { return nil, nil }}}, log, testLimits)
	call := newCall(string(benchBody))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ch.Before(ctx, call)
	}
}

// BenchmarkChain_BeforeNoHookImplementsIt is the Before stage when the only
// hook embeds NoopHook and overrides After alone: the stage is skipped.
func BenchmarkChain_BeforeNoHookImplementsIt(b *testing.B) {
	log, _ := newLogger()
	ch := New([]model.Hook{afterOnly{}}, log, testLimits)
	call := newCall(string(benchBody))
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ch.Before(ctx, call)
	}
}

// BenchmarkChain_OnResponse is the per-call cost of the OnResponse stage with
// one pass-through hook over a 4 KB body.
func BenchmarkChain_OnResponse(b *testing.B) {
	log, _ := newLogger()
	ch := New([]model.Hook{&hook{name: "a"}}, log, testLimits)
	call, resp := newCall("x"), &model.Response{Status: 200, Body: benchBody}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ch.OnResponse(ctx, call, resp)
	}
}
