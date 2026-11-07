package testkit_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

// Hooks that embed NoopHook and implement some stages, in every shape Go
// allows; the same cases as the core's detection (internal/chain/detect_test.go).
type afterOnly struct{ tollgate.NoopHook }

func (afterOnly) After(context.Context, *tollgate.Call, tollgate.Metric) {}

type ptrBefore struct{ tollgate.NoopHook }

func (*ptrBefore) Before(context.Context, *tollgate.Call) (*tollgate.Response, error) {
	return nil, nil
}

type genericEvent[T any] struct{ tollgate.NoopHook }

func (genericEvent[T]) OnEvent(context.Context, *tollgate.Call, tollgate.Event) {}

type innerResp struct{ tollgate.NoopHook }

func (*innerResp) OnResponse(_ context.Context, _ *tollgate.Call, r *tollgate.Response) (*tollgate.Response, error) {
	return r, nil
}

type outerResp struct{ innerResp }

type closedOnly struct{ tollgate.NoopHook }

func (closedOnly) OnSessionClosed(context.Context, tollgate.Session) {}

type plainEmbed struct{ tollgate.NoopHook }

func TestRun_RecordsOnlyOverriddenStages(t *testing.T) {
	tests := []struct {
		name    string
		hook    tollgate.Hook
		fixture string
		want    []testkit.Stage
	}{
		{"embedding only", plainEmbed{}, "openai/chat-stream-text", nil},
		{"value receiver", afterOnly{}, "openai/chat-stream-text", []testkit.Stage{testkit.StageAfter}},
		{"value receiver via pointer", &afterOnly{}, "openai/chat-stream-text", []testkit.Stage{testkit.StageAfter}},
		{"pointer receiver", &ptrBefore{}, "openai/chat-stream-text", []testkit.Stage{testkit.StageBefore}},
		{"generic type", genericEvent[int]{}, "openai/chat-stream-text", []testkit.Stage{testkit.StageOnEvent}},
		{"override in an embedded type", &outerResp{}, "openai/chat", []testkit.Stage{testkit.StageOnResponse}},
		{"OnSessionClosed only", closedOnly{}, "openai/chat", []testkit.Stage{testkit.StageSessionClosed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := testkit.Run(t, tt.hook, tt.fixture)
			seen := map[testkit.Stage]bool{}
			for _, s := range rec.Stages() {
				seen[s] = true
			}
			if got := slices.Sorted(maps.Keys(seen)); !slices.Equal(got, slices.Sorted(slices.Values(tt.want))) {
				t.Fatalf("recorded stages = %v, want only %v", got, tt.want)
			}
			if rec.Replies[0].Status != 200 {
				t.Fatalf("status = %d", rec.Replies[0].Status)
			}
		})
	}
}

type namedHook struct {
	probeHook
	name string
}

func (h namedHook) Name() string { return h.name }

type namedFinalizer struct {
	releaseFinalizer
	name string
}

func (f namedFinalizer) Name() string { return f.name }

func TestRunWith_SeveralHooksAndFinalizersAllRun(t *testing.T) {
	rec := testkit.RunWith(t, testkit.Config{
		Plugins: []any{namedHook{name: "a"}, namedHook{name: "b"},
			namedFinalizer{name: "f1"}, namedFinalizer{name: "f2"}},
		Fixtures: []string{"openai/chat-stream-text"},
	})
	pluginsOf := func(s testkit.Stage) []string {
		var out []string
		for _, inv := range rec.Of(s) {
			out = append(out, inv.Plugin)
		}
		return out
	}
	if got := pluginsOf(testkit.StageBefore); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Before ran %v, want a then b", got)
	}
	if got := pluginsOf(testkit.StageAfter); len(got) != 2 {
		t.Fatalf("After ran %v, want both hooks", got)
	}
	if got := pluginsOf(testkit.StageDecide); !slices.Equal(got, []string{"f1", "f2"}) {
		t.Fatalf("Decide ran %v, want f1 then f2", got)
	}
}
