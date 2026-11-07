package chain

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

type fin struct {
	name   string
	decide func(context.Context, *model.Call, model.StreamSummary) model.Decision
}

func (f fin) Name() string { return f.name }
func (f fin) Decide(ctx context.Context, c *model.Call, s model.StreamSummary) model.Decision {
	return f.decide(ctx, c, s)
}

func always(d model.Decision) fin {
	return fin{name: "always", decide: func(context.Context, *model.Call, model.StreamSummary) model.Decision { return d }}
}

var recoverBody = model.Decision{Action: model.DecisionRecover, Body: []byte(`{"again":true}`)}

func newBarrier(t *testing.T, lim FinalizeLimits, fs ...model.Finalizer) (*Barrier, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	l := slog.New(slog.NewTextHandler(&logs, nil))
	if lim.Decide == 0 {
		lim.Decide = time.Second
	}
	return NewFinalizers(fs, l, lim).Begin(&model.Call{ID: "c1", Tenant: model.Tenant{ID: "acme"}}), &logs
}

func TestFinalizers_EmptyListIsEmpty(t *testing.T) {
	if !NewFinalizers(nil, nil, FinalizeLimits{}).Empty() {
		t.Fatal("no finalizers must report Empty")
	}
	if NewFinalizers([]model.Finalizer{nil, always(model.Decision{})}, nil, FinalizeLimits{}).Empty() {
		t.Fatal("one finalizer must not report Empty")
	}
}

func TestBarrier_DecideRunsInOrderAndFirstActionWins(t *testing.T) {
	var order []string
	mk := func(name string, d model.Decision) model.Finalizer {
		return fin{name: name, decide: func(context.Context, *model.Call, model.StreamSummary) model.Decision {
			order = append(order, name)
			return d
		}}
	}
	replace := model.Decision{Action: model.DecisionReplace, Response: &model.Response{Body: []byte("x")}}
	b, _ := newBarrier(t, FinalizeLimits{Budget: 2, Ceiling: 5}, mk("a", model.Decision{}), mk("b", replace), mk("c", recoverBody))
	got := b.Decide(context.Background(), model.StreamSummary{})
	if got.Action != model.DecisionReplace || strings.Join(order, ",") != "a,b" {
		t.Fatalf("decision %+v after %v", got, order)
	}
}

func TestBarrier_PanicAndSlowDecideRelease(t *testing.T) {
	slow := fin{name: "slow", decide: func(context.Context, *model.Call, model.StreamSummary) model.Decision {
		time.Sleep(300 * time.Millisecond) // ignores its context on purpose
		return recoverBody
	}}
	boom := fin{name: "boom", decide: func(context.Context, *model.Call, model.StreamSummary) model.Decision { panic("secret body") }}
	for name, f := range map[string]model.Finalizer{"panic": boom, "slow": slow} {
		t.Run(name, func(t *testing.T) {
			b, logs := newBarrier(t, FinalizeLimits{Decide: 20 * time.Millisecond, Budget: 2, Ceiling: 5}, f)
			start := time.Now()
			if got := b.Decide(context.Background(), model.StreamSummary{}); got.Action != model.DecisionRelease {
				t.Fatalf("decision %+v, want release", got)
			}
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("a slow Decide held the caller past its deadline")
			}
			if l := logs.String(); !strings.Contains(l, "finalizer skipped") || !strings.Contains(l, "call_id=c1") || strings.Contains(l, "secret") {
				t.Fatalf("log = %q", l)
			}
		})
	}
}

func TestBarrier_BudgetsTurnRecoverIntoRelease(t *testing.T) {
	cases := []struct {
		name      string
		budget    int
		ceiling   int
		finalizer int
		want      int // recovers granted
	}{
		{"per-finalizer budget", 2, 10, 1, 2},
		{"per-call ceiling across finalizers", 2, 3, 2, 3},
		{"budget of one", 1, 1, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fs []model.Finalizer
			for range tc.finalizer {
				fs = append(fs, always(recoverBody))
			}
			b, logs := newBarrier(t, FinalizeLimits{Budget: tc.budget, Ceiling: tc.ceiling}, fs...)
			granted := 0
			for range 10 {
				if b.Decide(context.Background(), model.StreamSummary{}).Action == model.DecisionRecover {
					granted++
				}
			}
			if granted != tc.want {
				t.Fatalf("granted %d recoveries, want %d", granted, tc.want)
			}
			if !strings.Contains(logs.String(), "over budget") {
				t.Fatalf("an exceeded budget must be logged: %q", logs.String())
			}
		})
	}
}

func TestBarrier_MalformedDecisionsRelease(t *testing.T) {
	for name, d := range map[string]model.Decision{
		"recover without body":  {Action: model.DecisionRecover},
		"replace without reply": {Action: model.DecisionReplace},
		"unknown action":        {Action: 99},
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := newBarrier(t, FinalizeLimits{Budget: 2, Ceiling: 5}, always(d))
			if got := b.Decide(context.Background(), model.StreamSummary{}); got.Action != model.DecisionRelease {
				t.Fatalf("decision %+v, want release", got)
			}
		})
	}
}

func TestBarrier_FinalizerGetsPrivateCopies(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	mutate := fin{name: "mut", decide: func(_ context.Context, c *model.Call, s model.StreamSummary) model.Decision {
		defer wg.Done()
		c.Meta["k"] = "changed"
		c.Body[0] = 'X'
		s.ToolCalls[0].Arguments[0] = 'X'
		return model.Decision{}
	}}
	call := &model.Call{ID: "c", Body: []byte("orig"), Meta: map[string]any{"k": "v"}}
	sum := model.StreamSummary{ToolCalls: []model.ToolCall{{Arguments: []byte(`{"a":1}`)}}}
	NewFinalizers([]model.Finalizer{mutate}, nil, FinalizeLimits{Decide: time.Second, Budget: 1, Ceiling: 1}).Begin(call).Decide(context.Background(), sum)
	wg.Wait()
	if string(call.Body) != "orig" || call.Meta["k"] != "v" || sum.ToolCalls[0].Arguments[0] != '{' {
		t.Fatal("a finalizer wrote through to the call or the summary")
	}
}

// TestBarrier_TotalDecideTimeIsBounded: each Decide gets at most
// Limits.Decide, and all Decide calls of one call together at most
// Limits.Total, recovery rounds included; once spent, the barrier releases
// without asking (STR-4, STR-7).
func TestBarrier_TotalDecideTimeIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var n atomic.Int32
		waits := fin{name: "waits", decide: func(ctx context.Context, _ *model.Call, _ model.StreamSummary) model.Decision {
			n.Add(1)
			<-ctx.Done() // a finalizer that always uses its whole deadline
			return recoverBody
		}}
		b, logs := newBarrier(t, FinalizeLimits{Decide: 100 * time.Millisecond, Total: 250 * time.Millisecond, Budget: 5, Ceiling: 5},
			waits, waits, waits)
		start := time.Now()
		if got := b.Decide(context.Background(), model.StreamSummary{}); got.Action != model.DecisionRelease {
			t.Fatalf("decision %+v, want release", got)
		}
		if took := time.Since(start); took != 250*time.Millisecond || n.Load() != 3 {
			t.Fatalf("first round took %v with %d calls, want exactly the 250ms total over 3 calls", took, n.Load())
		}
		start = time.Now()
		if got := b.Decide(context.Background(), model.StreamSummary{}); got.Action != model.DecisionRelease || time.Since(start) != 0 || n.Load() != 3 {
			t.Fatalf("second round: %+v after %v and %d calls; want an immediate release, no finalizer asked", got, time.Since(start), n.Load())
		}
		if !strings.Contains(logs.String(), "reason="+reasonFinalizeDeadline) {
			t.Fatalf("logs %q, want the spent budget logged", logs.String())
		}
	})
}
