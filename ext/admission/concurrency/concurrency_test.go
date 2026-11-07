package concurrency

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethosagent/tollgate"
)

func newLimiter(t *testing.T, cfg Config) *Limiter {
	t.Helper()
	l, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func acquire(t *testing.T, l *Limiter, tenant string) func() {
	t.Helper()
	rel, err := l.Acquire(context.Background(), tollgate.Tenant{ID: tenant}, nil)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", tenant, err)
	}
	return rel
}

func TestAcquire_RejectsOverLimitAfterTimeout(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1, QueueTimeout: 20 * time.Millisecond, RetryAfter: 3 * time.Second})
	rel := acquire(t, l, "a")
	defer rel()
	start := time.Now()
	_, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil)
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("returned before the queue timeout")
	}
	var se tollgate.StatusError
	if !errors.As(err, &se) || se.HTTPStatus() != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want StatusError 429", err)
	}
	var re tollgate.RetryAfterError
	if !errors.As(err, &re) || re.RetryAfter() != 3*time.Second {
		t.Fatalf("err = %v, want RetryAfterError 3s", err)
	}
}

func TestAcquire_ZeroTimeoutRejectsImmediately(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1})
	rel := acquire(t, l, "a")
	defer rel()
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil); err == nil {
		t.Fatal("want rejection")
	}
}

func TestAcquire_WaiterGetsSlotWhenReleased(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1, QueueTimeout: 5 * time.Second})
	rel := acquire(t, l, "a")
	got := make(chan error, 1)
	go func() {
		r, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil)
		if err == nil {
			r()
		}
		got <- err
	}()
	rel()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("waiter: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter never got the slot")
	}
}

func TestAcquire_ContextCancelWhileWaiting(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1, QueueTimeout: time.Minute})
	rel := acquire(t, l, "a")
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := l.Acquire(ctx, tollgate.Tenant{ID: "a"}, nil)
		errc <- err
	}()
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Acquire ignored ctx")
	}
}

func TestRelease_IdempotentFreesOneSlot(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 2})
	r1 := acquire(t, l, "a")
	r2 := acquire(t, l, "a")
	r1()
	r1() // double release must not free r2's slot
	r3 := acquire(t, l, "a")
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil); err == nil {
		t.Fatal("double release freed two slots")
	}
	r2()
	r3()
}

func TestAcquire_PerTenantOverridesAndIsolation(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1, PerTenant: map[string]int{"big": 3}})
	for i := 0; i < 3; i++ {
		acquire(t, l, "big")
	}
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "big"}, nil); err == nil {
		t.Fatal("big tenant exceeded its override")
	}
	acquire(t, l, "small") // unaffected by big being saturated
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "small"}, nil); err == nil {
		t.Fatal("small tenant exceeded the default")
	}
}

func TestLimiter_ForgetsIdleTenants(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1})
	acquire(t, l, "a")()
	n := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		n += len(sh.buckets)
		sh.mu.Unlock()
	}
	if n != 0 {
		t.Fatalf("%d idle buckets retained", n)
	}
}

func TestAcquire_ConcurrentNeverExceedsLimit(t *testing.T) {
	const limit = 3
	l := newLimiter(t, Config{DefaultMax: limit, QueueTimeout: 10 * time.Second})
	var mu sync.Mutex
	cur, peak := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			mu.Lock()
			cur++
			peak = max(peak, cur)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			cur--
			mu.Unlock()
			rel()
		}()
	}
	wg.Wait()
	if peak > limit {
		t.Fatalf("peak in-flight %d > %d", peak, limit)
	}
}

func TestNew_Validation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"zero default", Config{}, "DefaultMax"},
		{"negative timeout", Config{DefaultMax: 1, QueueTimeout: -1}, "QueueTimeout"},
		{"bad override", Config{DefaultMax: 1, PerTenant: map[string]int{"a": 0}}, "PerTenant"},
		{"empty override id", Config{DefaultMax: 1, PerTenant: map[string]int{"": 1}}, "PerTenant"},
		{"negative retry after", Config{DefaultMax: 1, RetryAfter: -1}, "RetryAfter"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLimiterError_Message(t *testing.T) {
	l := newLimiter(t, Config{DefaultMax: 1})
	acquire(t, l, "secret-tenant")
	_, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "secret-tenant"}, nil)
	if err == nil || !strings.Contains(err.Error(), "concurrency") {
		t.Fatalf("err = %v", err)
	}
}

func TestNew_CopiesPerTenant(t *testing.T) {
	per := map[string]int{"a": 1}
	l, err := New(Config{DefaultMax: 5, PerTenant: per})
	if err != nil {
		t.Fatal(err)
	}
	per["a"] = 5
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(context.Background(), tollgate.Tenant{ID: "a"}, nil); err == nil {
		t.Fatal("limit follows a PerTenant map mutated after New")
	}
}

// BenchmarkAcquireRelease runs acquire+release for many tenants in parallel:
// the per-call path must not serialise on one process-wide lock (HOT-6).
func BenchmarkAcquireRelease(b *testing.B) {
	l, err := New(Config{DefaultMax: 1 << 20})
	if err != nil {
		b.Fatal(err)
	}
	var next atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		t := tollgate.Tenant{ID: fmt.Sprintf("tenant-%d", next.Add(1))}
		for pb.Next() {
			release, err := l.Acquire(context.Background(), t, nil)
			if err != nil {
				b.Fatal(err)
			}
			release()
		}
	})
}
