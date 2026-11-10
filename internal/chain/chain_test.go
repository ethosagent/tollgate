package chain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

var testLimits = Limits{Hook: 50 * time.Millisecond, Total: 200 * time.Millisecond}

func appendBody(s string) func(context.Context, *model.Call) (*model.Response, error) {
	return func(_ context.Context, c *model.Call) (*model.Response, error) {
		c.Body = append(c.Body[:len(c.Body):len(c.Body)], s...)
		return nil, nil
	}
}

func TestBefore_RunsInUseOrderAndRewritesBody(t *testing.T) {
	log, _ := newLogger()
	ch := New([]model.Hook{&hook{name: "a", before: appendBody("-a")}, &hook{name: "b", before: appendBody("-b")}}, log, testLimits)
	call := newCall("body")
	if resp := ch.Before(context.Background(), call); resp != nil {
		t.Fatalf("unexpected short-circuit %+v", resp)
	}
	if got := string(call.Body); got != "body-a-b" {
		t.Fatalf("body = %q, want hooks applied in Use order", got)
	}
}

func TestBefore_ResponseShortCircuitsTheRest(t *testing.T) {
	log, _ := newLogger()
	ran := false
	stop := &model.Response{Status: 418, Body: []byte("stop")}
	ch := New([]model.Hook{
		&hook{name: "gate", before: func(context.Context, *model.Call) (*model.Response, error) { return stop, nil }},
		&hook{name: "never", before: func(context.Context, *model.Call) (*model.Response, error) { ran = true; return nil, nil }},
	}, log, testLimits)
	if got := ch.Before(context.Background(), newCall("x")); got != stop || ran {
		t.Fatalf("resp=%v ran=%v, want the gate's response and no later hook", got, ran)
	}
}

func TestBefore_FailingHookIsLoggedAndSkipped(t *testing.T) {
	boom := errors.New("secret-detail")
	tests := []struct {
		name   string
		before func(context.Context, *model.Call) (*model.Response, error)
		reason string
	}{
		{"panic", func(_ context.Context, c *model.Call) (*model.Response, error) {
			c.Body = []byte("half")
			panic("secret-detail")
		}, "panic"},
		{"error", func(_ context.Context, c *model.Call) (*model.Response, error) {
			c.Body = []byte("half")
			return nil, boom
		}, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, logs := newLogger()
			ch := New([]model.Hook{&hook{name: "bad", before: tt.before}, &hook{name: "ok", before: appendBody("-ok")}}, log, testLimits)
			call := newCall("body")
			if resp := ch.Before(context.Background(), call); resp != nil {
				t.Fatalf("resp = %+v", resp)
			}
			got := logs.String()
			for _, want := range []string{"hook=bad", "tenant=acme", "call_id=call-1", "method=before", "reason=" + tt.reason} {
				if !strings.Contains(got, want) {
					t.Errorf("log %q lacks %q", got, want)
				}
			}
			if strings.Contains(got, "secret-detail") {
				t.Errorf("log leaks the panic value or error text: %q", got)
			}
			if string(call.Body) != "body-ok" {
				t.Fatalf("body = %q, want the failing hook's partial edit discarded and the next hook applied", call.Body)
			}
		})
	}
}

func TestBefore_SlowHookIsCancelledAndItsLateResultIgnored(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, logs := newLogger()
		var sawCancel bool
		slow := &hook{name: "slow", before: func(ctx context.Context, c *model.Call) (*model.Response, error) {
			select {
			case <-ctx.Done():
				sawCancel = true
			case <-time.After(time.Hour):
			}
			time.Sleep(10 * time.Millisecond) // returns late, with a rewrite and a response
			c.Body = []byte("late")
			return &model.Response{Status: 500}, nil
		}}
		ch := New([]model.Hook{slow, &hook{name: "fast", before: appendBody("-fast")}}, log, testLimits)
		call := newCall("body")
		start := time.Now()
		resp := ch.Before(context.Background(), call)
		if took := time.Since(start); resp != nil || took != testLimits.Hook {
			t.Fatalf("resp=%v took=%v, want the slow hook cut at %v and skipped", resp, took, testLimits.Hook)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if !sawCancel || string(call.Body) != "body-fast" {
			t.Fatalf("cancel=%v body=%q, want ctx cancelled and the late rewrite ignored", sawCancel, call.Body)
		}
		if !strings.Contains(logs.String(), "reason=timeout") || !strings.Contains(logs.String(), "hook=slow") {
			t.Fatalf("log = %q", logs.String())
		}
	})
}

func TestBefore_ChainDeadlineBoundsAllHooks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, _ := newLogger()
		var called []string
		step := func(name string) *hook {
			return &hook{name: name, before: func(ctx context.Context, c *model.Call) (*model.Response, error) {
				called = append(called, name)
				select {
				case <-time.After(40 * time.Millisecond):
					c.Body = append(c.Body[:len(c.Body):len(c.Body)], '+')
					c.Body = append(c.Body, name...)
				case <-ctx.Done():
				}
				return nil, nil
			}}
		}
		lim := Limits{Hook: 50 * time.Millisecond, Total: 100 * time.Millisecond}
		ch := New([]model.Hook{step("h1"), step("h2"), step("h3"), step("h4")}, log, lim)
		call := newCall("b")
		start := time.Now()
		ch.Before(context.Background(), call)
		synctest.Wait()
		if took := time.Since(start); took != lim.Total {
			t.Fatalf("chain took %v, want exactly the %v bound", took, lim.Total)
		}
		if got := strings.Join(called, ","); got != "h1,h2,h3" || string(call.Body) != "b+h1+h2" {
			t.Fatalf("called=%s body=%q, want h3 cut by the chain bound and h4 never started", got, call.Body)
		}
	})
}

func TestBefore_ClientGoneStopsTheChain(t *testing.T) {
	log, _ := newLogger()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	ch := New([]model.Hook{&hook{name: "a", before: func(context.Context, *model.Call) (*model.Response, error) { ran = true; return nil, nil }}}, log, testLimits)
	ch.Before(ctx, newCall("x"))
	if ran {
		t.Fatal("hook ran for a call whose client had gone")
	}
}

func TestOnResponse_ReplacesAndSkipsFailures(t *testing.T) {
	log, logs := newLogger()
	upper := func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
		return &model.Response{Status: r.Status, Body: []byte(strings.ToUpper(string(r.Body)))}, nil
	}
	ch := New([]model.Hook{
		&hook{name: "boom", onResp: func(_ context.Context, _ *model.Call, r *model.Response) (*model.Response, error) {
			r.Body[0] = 'X' // in-place edit of its own copy, then panic
			panic("x")
		}},
		&hook{name: "upper", onResp: upper},
		&hook{name: "err", onResp: func(context.Context, *model.Call, *model.Response) (*model.Response, error) {
			return nil, errors.New("no")
		}},
	}, log, testLimits)
	in := &model.Response{Status: 200, Body: []byte("abc")}
	out := ch.OnResponse(context.Background(), newCall("x"), in)
	if string(out.Body) != "ABC" || string(in.Body) != "abc" {
		t.Fatalf("out=%q in=%q, want the replacement and an untouched input", out.Body, in.Body)
	}
	if g := logs.String(); !strings.Contains(g, "hook=boom") || !strings.Contains(g, "method=on_response") || !strings.Contains(g, "hook=err") {
		t.Fatalf("log = %q", g)
	}
}

func TestEvents_DeliverInOrderAndDropAPanickingHookForTheCall(t *testing.T) {
	log, logs := newLogger()
	var got []string
	panics := 0
	ch := New([]model.Hook{
		&hook{name: "boom", onEvent: func(context.Context, *model.Call, model.Event) { panics++; panic("x") }},
		&hook{name: "rec", onEvent: func(_ context.Context, _ *model.Call, e model.Event) { got = append(got, e.Text) }},
	}, log, testLimits)
	call := newCall("x")
	ev := ch.Events(call)
	for _, s := range []string{"a", "b", "c"} {
		if !ev.Deliver(context.Background(), model.Event{Kind: model.EventTextDelta, Text: s}) {
			t.Fatal("Deliver reported not delivered")
		}
	}
	if strings.Join(got, "") != "abc" || panics != 1 || strings.Count(logs.String(), "hook=boom") != 1 {
		t.Fatalf("got=%v panics=%d log=%q, want in-order delivery, one panic and one log line", got, panics, logs.String())
	}
	if ev2 := ch.Events(call); !ev2.Deliver(context.Background(), model.Event{}) || panics != 2 {
		t.Fatalf("a new call must retry the hook (panics=%d)", panics)
	}
}

func TestEvents_CancelledContextDeliversNothing(t *testing.T) {
	log, _ := newLogger()
	n := 0
	ch := New([]model.Hook{&hook{name: "a", onEvent: func(context.Context, *model.Call, model.Event) { n++ }}}, log, testLimits)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ch.Events(newCall("x")).Deliver(ctx, model.Event{}) || n != 0 {
		t.Fatal("event delivered after the context ended")
	}
}

func TestAfterAndSessionClosed_PanicsAreContained(t *testing.T) {
	log, logs := newLogger()
	var afters, closes int
	ch := New([]model.Hook{
		&hook{name: "boom", after: func(context.Context, *model.Call, model.Metric) { panic("x") }, closed: func(context.Context, model.Session) { panic("x") }},
		&hook{name: "ok", after: func(_ context.Context, _ *model.Call, m model.Metric) { afters += m.Status }, closed: func(context.Context, model.Session) { closes++ }},
	}, log, testLimits)
	ch.After(context.Background(), newCall("x"), model.Metric{Status: 7})
	ch.SessionClosed(context.Background(), model.Session{ID: "s"})
	if afters != 7 || closes != 1 {
		t.Fatalf("afters=%d closes=%d, want the healthy hook to run after the panicking one", afters, closes)
	}
	if g := logs.String(); !strings.Contains(g, "method=after") || !strings.Contains(g, "method=session_closed") {
		t.Fatalf("log = %q", g)
	}
}

func TestWithin_ReturnsResultErrorAndTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v, err := Within(context.Background(), time.Second, func(context.Context) (int, error) { return 3, nil })
		if v != 3 || err != nil {
			t.Fatalf("v=%d err=%v", v, err)
		}
		_, err = Within(context.Background(), time.Second, func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 9, nil
		})
		if !errors.Is(err, ErrDeadline) {
			t.Fatalf("err = %v, want ErrDeadline", err)
		}
	})
}

// A short-circuiting hook keeps only its Meta: the rest of its call copy is
// dropped (the upstream is never called), and BeforeNamed says who answered.
func TestBefore_ShortCircuitAdoptsMetaAndNamesTheHook(t *testing.T) {
	log, _ := newLogger()
	stop := &model.Response{Status: 429, Body: []byte("stop")}
	gate := &hook{name: "gate", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		c.Meta["monitor"] = "loop"
		c.Body = []byte("rewritten")
		return stop, nil
	}}
	ch := New([]model.Hook{&hook{name: "first", before: func(_ context.Context, c *model.Call) (*model.Response, error) {
		c.Meta = map[string]any{"seen": true}
		return nil, nil
	}}, gate}, log, testLimits)
	call := newCall("orig")
	resp, by := ch.BeforeNamed(context.Background(), call)
	if resp != stop || by != "gate" {
		t.Fatalf("resp=%v by=%q, want the gate's response and name", resp, by)
	}
	if call.Meta["monitor"] != "loop" || call.Meta["seen"] != true {
		t.Fatalf("Meta = %v, want the earlier hook's and the short-circuiting hook's keys", call.Meta)
	}
	if string(call.Body) != "orig" {
		t.Fatalf("body = %q, want the call's other fields untouched", call.Body)
	}
	if _, by := New(nil, log, testLimits).BeforeNamed(context.Background(), newCall("x")); by != "" {
		t.Fatalf("by = %q with no short-circuit", by)
	}
}
