package session

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

func resolve(s *Strategy, tenant, header string, msgs ...model.Message) model.Session {
	c := &model.Call{Messages: msgs, SessionHeader: header}
	return s.Resolve(context.Background(), model.Tenant{ID: tenant}, c)
}

func agentRun(steps int) [][]model.Message {
	msgs := []model.Message{sys("S"), user("fix the test")}
	run := [][]model.Message{append([]model.Message{}, msgs...)}
	for i := 1; i < steps; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs, asst("", id), tool(id))
		run = append(run, append([]model.Message{}, msgs...))
	}
	return run
}

func newStrategy(t *testing.T, cfg Config) *Strategy {
	t.Helper()
	s := New(cfg)
	t.Cleanup(s.Close)
	return s
}

func TestResolve_HeaderIsPartitionedByTenant(t *testing.T) {
	s := newStrategy(t, Config{})
	a1 := resolve(s, "acme", "run-1")
	a2 := resolve(s, "acme", "run-1")
	b1 := resolve(s, "globex", "run-1")
	if a1.ID != "run-1" || a1.Source != model.SessionFromHeader || a1.TenantID != "acme" {
		t.Fatalf("header session = %+v", a1)
	}
	if a2.Calls != 2 || !a2.LastSeen.After(a1.LastSeen) && !a2.LastSeen.Equal(a1.LastSeen) {
		t.Fatalf("second call = %+v, want Calls 2", a2)
	}
	if b1.Calls != 1 || b1.TenantID != "globex" || b1.StartedAt.IsZero() {
		t.Fatalf("same id in another tenant must be a separate session: %+v", b1)
	}
	if got := s.Stats().Live; got != 2 {
		t.Fatalf("live = %d, want 2", got)
	}
}

func TestResolve_InvalidHeaderFallsBackToPrediction(t *testing.T) {
	s := newStrategy(t, Config{})
	for name, h := range map[string]string{"control": "a\nb", "too long": string(make([]byte, 300))} {
		got := resolve(s, "acme", h, sys("S"), user("U"))
		if got.Source != model.SessionPredicted {
			t.Fatalf("%s: source = %v, want predicted", name, got.Source)
		}
	}
}

func TestResolve_AgentRunIsOneSession(t *testing.T) {
	s := newStrategy(t, Config{})
	var first model.Session
	for i, msgs := range agentRun(6) {
		got := resolve(s, "acme", "", msgs...)
		if i == 0 {
			first = got
		}
		if got.ID != first.ID || got.Calls != i+1 || got.Source != model.SessionPredicted {
			t.Fatalf("step %d: %+v, want session %q with %d calls", i+1, got, first.ID, i+1)
		}
	}
	other := resolve(s, "acme", "", sys("S"), user("a different task"))
	if other.ID == first.ID {
		t.Fatalf("a new first message must start a new session, got %q again", other.ID)
	}
}

func TestResolve_SamePrefixButNoContinuationStartsNewSession(t *testing.T) {
	s := newStrategy(t, Config{})
	run := agentRun(3)
	a := resolve(s, "acme", "", run[0]...)
	b := resolve(s, "acme", "", run[0]...) // identical first request: no tool call yet, not a continuation
	if a.ID == b.ID || b.Calls != 1 {
		t.Fatalf("restart with the same prefix reused session: %+v then %+v", a, b)
	}
	if got := resolve(s, "acme", "", run[1]...); got.ID != b.ID {
		t.Fatalf("step 2 joined %q, want the latest run %q", got.ID, b.ID)
	}
	select {
	case closed := <-s.Closed():
		if closed.ID != a.ID {
			t.Fatalf("closed %q, want the replaced run %q", closed.ID, a.ID)
		}
	default:
		t.Fatal("the replaced run was not emitted on Closed")
	}
}

func TestResolve_PredictionIsPartitionedByTenant(t *testing.T) {
	s := newStrategy(t, Config{})
	a := resolve(s, "acme", "", sys("S"), user("U"))
	b := resolve(s, "globex", "", sys("S"), user("U"))
	if a.ID == b.ID {
		t.Fatalf("same prompt in two tenants shared session %q", a.ID)
	}
}

func TestStats_PredictionAccuracyWhenHeaderPresent(t *testing.T) {
	s := newStrategy(t, Config{MeasurePrediction: true})
	run := agentRun(3)
	for _, msgs := range run {
		resolve(s, "acme", "run-1", msgs...)
	}
	resolve(s, "acme", "run-2", sys("S"), user("another task"))
	if st := s.Stats(); st.Agree != 4 || st.Disagree != 0 {
		t.Fatalf("agree/disagree = %d/%d, want 4/0", st.Agree, st.Disagree)
	}
	resolve(s, "acme", "run-3", run[2]...)            // a false merge: the predictor would join it to run-1
	resolve(s, "acme", "run-1", sys("S"), user("U2")) // a split: same session, unrelated prefix
	if st := s.Stats(); st.Agree != 4 || st.Disagree != 2 {
		t.Fatalf("agree/disagree = %d/%d, want 4/2", st.Agree, st.Disagree)
	}
}

func TestStats_PredictionIsNotMeasuredByDefault(t *testing.T) {
	s := newStrategy(t, Config{})
	resolve(s, "acme", "run-1", sys("S"), user("U"))
	resolve(s, "acme", "run-2", sys("S"), user("U"))
	if st := s.Stats(); st.Agree != 0 || st.Disagree != 0 || s.shadow != nil {
		t.Fatalf("stats = %+v, shadow store = %v; want no accuracy harness unless MeasurePrediction", st, s.shadow != nil)
	}
}

func TestDefaultShards_SmallStoresAreOneExactLRU(t *testing.T) {
	for _, tt := range []struct{ max, want int }{{1, 1}, {100, 1}, {1023, 1}, {4096, 4}, {100_000, 16}} {
		if got := defaultShards(tt.max); got != tt.want {
			t.Errorf("defaultShards(%d) = %d, want %d", tt.max, got, tt.want)
		}
	}
}

func TestMaxSessions_EvictsLeastRecentlyUsedAndCounts(t *testing.T) {
	s := newStrategy(t, Config{Max: 3, Shards: 1})
	for _, id := range []string{"a", "b", "c"} {
		resolve(s, "acme", id)
	}
	resolve(s, "acme", "a") // a is now the most recent; b is the oldest
	resolve(s, "acme", "d")
	st := s.Stats()
	if st.Live != 3 || st.Evicted != 1 {
		t.Fatalf("live/evicted = %d/%d, want 3/1", st.Live, st.Evicted)
	}
	if got := (<-s.Closed()).ID; got != "b" {
		t.Fatalf("evicted %q, want the least recently used %q", got, "b")
	}
	if got := resolve(s, "acme", "a"); got.Calls != 3 {
		t.Fatalf("a survived with %d calls, want 3", got.Calls)
	}
}

func TestTTL_QuietSessionsCloseOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{TTL: time.Minute})
		defer s.Close()
		resolve(s, "acme", "quiet")
		time.Sleep(40 * time.Second)
		resolve(s, "acme", "busy")
		time.Sleep(30 * time.Second) // quiet is 70s old, busy 30s
		synctest.Wait()
		got := <-s.Closed()
		if got.ID != "quiet" || got.Calls != 1 {
			t.Fatalf("closed %+v, want the quiet session", got)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := (<-s.Closed()).ID; got != "busy" {
			t.Fatalf("second close = %q, want busy", got)
		}
		if st := s.Stats(); st.Live != 0 || st.Expired != 2 {
			t.Fatalf("live/expired = %d/%d, want 0/2", st.Live, st.Expired)
		}
		select {
		case dup := <-s.Closed():
			t.Fatalf("session %q closed twice", dup.ID)
		default:
		}
	})
}

func TestClose_StopsTheSweeperAndClosesTheChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(Config{TTL: time.Minute})
		resolve(s, "acme", "x")
		s.Close()
		s.Close() // idempotent
		if _, ok := <-s.Closed(); ok {
			t.Fatal("Closed still open after Close")
		}
		time.Sleep(time.Hour)
		resolve(s, "acme", "y") // still resolves, never panics, never emits
	})
}

func TestClosed_FullChannelDropsAndCounts(t *testing.T) {
	s := newStrategy(t, Config{Max: 1, Shards: 1, ClosedBuffer: 1})
	for _, id := range []string{"a", "b", "c"} {
		resolve(s, "acme", id)
	}
	if st := s.Stats(); st.Evicted != 2 || st.ClosedDropped != 1 {
		t.Fatalf("evicted/dropped = %d/%d, want 2/1", st.Evicted, st.ClosedDropped)
	}
}
