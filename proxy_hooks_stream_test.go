package tollgate_test

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
	"github.com/ethosagent/tollgate/testkit"
)

func TestHooks_OnEventReceivesNormalisedEventsInOrder(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	for _, name := range testkit.FixtureNames() {
		if !strings.Contains(name, "stream") || strings.Contains(name, "/request-") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var got []tollgate.Event
			done := make(chan struct{})
			h := &tHook{name: "rec",
				onEvent: func(_ context.Context, _ *tollgate.Call, e tollgate.Event) {
					mu.Lock()
					got = append(got, e)
					mu.Unlock()
				},
				after: func(context.Context, *tollgate.Call, tollgate.Metric) { close(done) }}
			base, _ := newHookedProxy(t, tollgate.Options{}, h)
			resp := post(t, base, pathFor(name), requestBody(t, name), via(up, testkit.FixtureHeader, name))
			want, _ := testkit.Fixture(name)
			if body := readAll(t, resp); !bytes.Equal(body, want) {
				t.Fatalf("client bytes differ from the fixture")
			}
			<-done
			mu.Lock()
			defer mu.Unlock()
			if exp := expectedEvents(t, name); len(exp) == 0 || !reflect.DeepEqual(got, exp) {
				t.Fatalf("events\n got %+v\nwant %+v", got, exp)
			}
		})
	}
}

// TestHooks_SlowOnEventNeverDelaysClient reuses the 003 guarantee with a real
// hook: a hook stuck in OnEvent costs the client nothing, frames it cannot
// take are dropped and counted, and the consumer still ends with the call.
func TestHooks_SlowOnEventNeverDelaysClient(t *testing.T) {
	const delay, slack = 20 * time.Millisecond, 50 * time.Millisecond
	chunks := testkit.Chunks(streamFixture)
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: delay})
	done := make(chan tollgate.Metric, 1)
	stuck := &tHook{name: "stuck",
		onEvent: func(ctx context.Context, _ *tollgate.Call, _ tollgate.Event) { <-ctx.Done() }, // honours its ctx only
		after:   func(_ context.Context, _ *tollgate.Call, m tollgate.Metric) { done <- m }}
	base, p := newHookedProxy(t, tollgate.Options{Limits: tollgate.Limits{EventBuffer: 1}}, stuck)

	start := time.Now()
	resp := post(t, base, chatPath, requestBody(t, streamFixture), via(up))
	body := readAll(t, resp)
	took := time.Since(start)
	if fixtureTime := time.Duration(len(chunks)-1) * delay; took > fixtureTime+slack {
		t.Fatalf("stream took %v with a stuck hook, want at most %v", took, fixtureTime+slack)
	}
	if !bytes.Equal(body, bytes.Join(chunks, nil)) {
		t.Fatal("client bytes changed")
	}
	select { // the consumer is released by the call's end, so After runs
	case m := <-done:
		if m.Finish != tollgate.FinishOK {
			t.Fatalf("finish = %s", m.Finish)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not end with the call")
	}
	if p.DroppedEvents() == 0 {
		t.Fatal("no frame was dropped although the hook never took one")
	}
}

func TestHooks_AfterGetsTheMetricAndCannotBlockTheResponse(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: time.Millisecond})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var events atomic.Int32
	type seen struct {
		m      tollgate.Metric
		events int32
	}
	got := make(chan seen, 1)
	h := &tHook{name: "audit",
		onEvent: func(context.Context, *tollgate.Call, tollgate.Event) { events.Add(1) },
		after: func(_ context.Context, _ *tollgate.Call, m tollgate.Metric) {
			got <- seen{m, events.Load()}
			<-release // a stuck After must not reach the client
		}}
	base, _ := newHookedProxy(t, tollgate.Options{}, h)
	resp := post(t, base, chatPath, requestBody(t, streamFixture), via(up))
	if body := readAll(t, resp); len(body) == 0 { // returned although After is still blocked
		t.Fatal("empty body")
	}
	s := <-got
	m := s.m
	if m.Status != 200 || m.Finish != tollgate.FinishOK || !m.Stream || m.Model != "gpt-4.1-mini" || m.Provider != "openai" ||
		m.TenantID != "acme" || m.CallID == "" {
		t.Fatalf("metric = %+v", m)
	}
	if m.Usage != (tollgate.Usage{InputTokens: 12, OutputTokens: 2}) {
		t.Fatalf("usage = %+v, want it taken from the stream's events", m.Usage)
	}
	if m.Start.IsZero() || m.TTFB <= 0 || m.Duration < m.TTFB {
		t.Fatalf("timings start=%v ttfb=%v duration=%v", m.Start, m.TTFB, m.Duration)
	}
	if want := int32(len(expectedEvents(t, streamFixture))); s.events != want {
		t.Fatalf("After ran after %d of %d events", s.events, want)
	}
}

func TestHooks_ClientCancelStillEndsConsumerAndRunsAfter(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{ChunkDelay: 30 * time.Second})
	done := make(chan tollgate.Metric, 1)
	h := &tHook{name: "w",
		onEvent: func(ctx context.Context, _ *tollgate.Call, _ tollgate.Event) { <-ctx.Done() },
		after:   func(_ context.Context, _ *tollgate.Call, m tollgate.Metric) { done <- m }}
	base, _ := newHookedProxy(t, tollgate.Options{}, h)
	ctx, cancel := context.WithCancel(context.Background())
	resp := postCtx(ctx, t, base, chatPath, requestBody(t, streamFixture), via(up))
	first := testkit.Chunks(streamFixture)[0]
	if _, err := resp.Body.Read(make([]byte, len(first))); err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case m := <-done:
		if m.Finish != tollgate.FinishClientCancel {
			t.Fatalf("finish = %s, want client_cancel", m.Finish)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("After never ran for a cancelled call")
	}
}

// TestHooks_StuckOnEventDoesNotHoldAfter covers STR-9: an OnEvent hook that
// ignores its context pins only the consumer; After still runs, bounded by
// twice HookChainDeadline after the call ends.
func TestHooks_StuckOnEventDoesNotHoldAfter(t *testing.T) {
	up := testkit.NewFakeUpstream(t, testkit.Options{})
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	after := make(chan tollgate.Metric, 1)
	h := &tHook{name: "stuck",
		onEvent: func(context.Context, *tollgate.Call, tollgate.Event) { <-unblock },
		after:   func(_ context.Context, _ *tollgate.Call, m tollgate.Metric) { after <- m }}
	opts := tollgate.Options{Limits: tollgate.Limits{HookBeforeDeadline: 20 * time.Millisecond, HookChainDeadline: 50 * time.Millisecond}}
	base, _ := newHookedProxy(t, opts, h)
	_ = readAll(t, post(t, base, chatPath, requestBody(t, streamFixture), via(up)))
	select {
	case m := <-after:
		if m.Finish != tollgate.FinishOK {
			t.Fatalf("metric = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("After never ran: a stuck OnEvent hook held it")
	}
}
