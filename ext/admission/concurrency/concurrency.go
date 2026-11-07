package concurrency

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/ethosagent/tollgate"
)

var _ tollgate.Admission = (*Limiter)(nil)

// defaultRetryAfter is the Retry-After hint when Config.RetryAfter is zero.
const defaultRetryAfter = time.Second

// Config configures New. New copies it, so changing cfg (or its PerTenant
// map) afterwards has no effect.
type Config struct {
	DefaultMax   int            // max in-flight calls per tenant; required, > 0
	PerTenant    map[string]int // tenant ID -> max in-flight, overrides DefaultMax
	QueueTimeout time.Duration  // how long a call may wait for a slot; 0 rejects at once
	RetryAfter   time.Duration  // Retry-After hint on rejection; 0 means 1s
}

// shardCount splits the tenant table so concurrent calls of different
// tenants rarely share a lock (HOT-6). A power of two keeps the modulo cheap.
const shardCount = 64

// Limiter is a tollgate.Admission that caps in-flight calls per tenant. It
// is safe for concurrent use.
type Limiter struct {
	cfg    Config
	seed   maphash.Seed
	shards [shardCount]shard
}

// shard owns the buckets of the tenants that hash to it.
type shard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

// bucket is one tenant's semaphore. refs counts holders plus waiters so the
// bucket can be dropped when the tenant goes idle.
type bucket struct {
	sem  chan struct{}
	refs int
}

// busyError is the 429 returned when a tenant stays saturated.
type busyError struct{ retryAfter time.Duration }

func (e *busyError) Error() string             { return "concurrency: tenant at its in-flight limit" }
func (e *busyError) HTTPStatus() int           { return http.StatusTooManyRequests }
func (e *busyError) RetryAfter() time.Duration { return e.retryAfter }

// New validates cfg once and builds the limiter.
func New(cfg Config) (*Limiter, error) {
	if cfg.DefaultMax <= 0 {
		return nil, errors.New("concurrency: DefaultMax must be > 0")
	}
	if cfg.QueueTimeout < 0 {
		return nil, errors.New("concurrency: QueueTimeout must not be negative")
	}
	if cfg.RetryAfter < 0 {
		return nil, errors.New("concurrency: RetryAfter must not be negative")
	}
	for id, n := range cfg.PerTenant {
		if id == "" || n <= 0 {
			return nil, fmt.Errorf("concurrency: PerTenant needs a non-empty ID and a limit > 0 (got %q: %d)", id, n)
		}
	}
	if cfg.RetryAfter == 0 {
		cfg.RetryAfter = defaultRetryAfter
	}
	cfg.PerTenant = maps.Clone(cfg.PerTenant)
	l := &Limiter{cfg: cfg, seed: maphash.MakeSeed()}
	for i := range l.shards {
		l.shards[i].buckets = make(map[string]*bucket)
	}
	return l, nil
}

// Acquire takes one slot for the tenant, waiting up to QueueTimeout or until
// ctx ends. Past the timeout it returns a 429 error that implements
// tollgate.StatusError and tollgate.RetryAfterError. The returned release is
// safe to call more than once; only the first call frees the slot.
func (l *Limiter) Acquire(ctx context.Context, t tollgate.Tenant, _ *tollgate.Call) (func(), error) {
	b := l.enter(t.ID)
	if err := l.wait(ctx, b); err != nil {
		l.leave(t.ID)
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-b.sem
			l.leave(t.ID)
		})
	}, nil
}

func (l *Limiter) wait(ctx context.Context, b *bucket) error {
	select {
	case b.sem <- struct{}{}:
		return nil
	default:
	}
	if l.cfg.QueueTimeout == 0 {
		return &busyError{l.cfg.RetryAfter}
	}
	timer := time.NewTimer(l.cfg.QueueTimeout)
	defer timer.Stop()
	select {
	case b.sem <- struct{}{}:
		return nil
	case <-timer.C:
		return &busyError{l.cfg.RetryAfter}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) shardOf(id string) *shard {
	return &l.shards[maphash.String(l.seed, id)%shardCount]
}

func (l *Limiter) enter(id string) *bucket {
	sh := l.shardOf(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b, ok := sh.buckets[id]
	if !ok {
		limit := l.cfg.DefaultMax
		if n, ok := l.cfg.PerTenant[id]; ok {
			limit = n
		}
		b = &bucket{sem: make(chan struct{}, limit)}
		sh.buckets[id] = b
	}
	b.refs++
	return b
}

func (l *Limiter) leave(id string) {
	sh := l.shardOf(id)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b := sh.buckets[id]
	if b.refs--; b.refs == 0 {
		delete(sh.buckets, id)
	}
}
