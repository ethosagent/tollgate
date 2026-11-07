package chain

import (
	"context"
	"testing"

	"github.com/ethosagent/tollgate/internal/model"
)

// Hooks that embed NoopHook and override some methods, in every shape Go
// allows: value or pointer receiver, generic, overridden in an embedded type.
type afterOnly struct{ model.NoopHook }

func (afterOnly) After(context.Context, *model.Call, model.Metric) {}

type ptrBefore struct{ model.NoopHook }

func (*ptrBefore) Before(context.Context, *model.Call) (*model.Response, error) { return nil, nil }

type genericEvent[T any] struct{ model.NoopHook }

func (genericEvent[T]) OnEvent(context.Context, *model.Call, model.Event) {}

type innerResp struct{ model.NoopHook }

func (*innerResp) OnResponse(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
	return r, nil
}

type outerResp struct{ innerResp }

type plainEmbed struct{ model.NoopHook }

func TestNew_DetectsOverriddenMethodsOnce(t *testing.T) {
	all := []Stage{StageBefore, StageEvent, StageResponse, StageAfter, StageSessionClosed}
	tests := []struct {
		name string
		h    model.Hook
		want []Stage
	}{
		{"bare NoopHook", model.NoopHook{}, nil},
		{"pointer to NoopHook", &model.NoopHook{}, nil},
		{"embedding only", plainEmbed{}, nil},
		{"value receiver override", afterOnly{}, []Stage{StageAfter}},
		{"value receiver via pointer", &afterOnly{}, []Stage{StageAfter}},
		{"pointer receiver override", &ptrBefore{}, []Stage{StageBefore}},
		{"generic type", genericEvent[int]{}, []Stage{StageEvent}},
		{"override in an embedded type", &outerResp{}, []Stage{StageResponse}},
		{"no NoopHook: every method counts", &hook{name: "f"}, all},
	}
	log, _ := newLogger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New([]model.Hook{tt.h}, log, testLimits)
			for _, s := range all {
				want := false
				for _, w := range tt.want {
					want = want || w == s
				}
				if got := c.Has(s); got != want {
					t.Errorf("Has(%v) = %v, want %v", s, got, want)
				}
			}
			if c.Empty() {
				t.Error("Empty() = true for a chain with a hook")
			}
		})
	}
}

func TestWith_AppendsWithoutChangingTheOriginal(t *testing.T) {
	log, _ := newLogger()
	base := New(nil, log, testLimits)
	next := base.With(nil, afterOnly{}, nil, &ptrBefore{})
	if !base.Empty() || base.Has(StageAfter) {
		t.Fatal("With changed the original chain")
	}
	if next.Empty() || !next.Has(StageAfter) || !next.Has(StageBefore) || next.Has(StageEvent) {
		t.Fatalf("next stages wrong: %+v", next.by)
	}
	if again := next.With(); again != next {
		t.Fatal("With() with no hooks must return the same chain")
	}
}
