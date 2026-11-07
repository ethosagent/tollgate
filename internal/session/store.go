package session

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// Config configures a Strategy. A zero field takes its default.
type Config struct {
	TTL          time.Duration // quiet time before a session closes; default 5m
	Max          int           // live sessions kept (LRU beyond); default 100000
	Shards       int           // lock shards; default 16 (1 gives an exact global LRU)
	ClosedBuffer int           // capacity of the Closed channel; default 1024
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = 5 * time.Minute
	}
	if c.Max <= 0 {
		c.Max = 100_000
	}
	if c.Shards <= 0 {
		c.Shards = 16
	}
	if c.ClosedBuffer <= 0 {
		c.ClosedBuffer = 1024
	}
	return c
}

// Stats are the Strategy's counters since it was created.
type Stats struct {
	Live          int   // sessions currently live
	Expired       int64 // closed by the TTL sweeper
	Evicted       int64 // closed early because the store was full (LRU)
	Replaced      int64 // predicted runs ended by a new run with the same prefix
	ClosedDropped int64 // closed sessions not emitted because Closed was full
	Agree         int64 // header calls the predictor attributed the same way
	Disagree      int64 // header calls the predictor attributed differently
}

// live is a session in the store. count is the turns of its last call, used
// by the continuation rule (predicted sessions only).
type live struct {
	sess  model.Session
	count int
}

// shadow is the predictor's view of a header-supplied session: the most
// recent session seen with a given prefix and its turns.
type shadow struct {
	owner string
	count int
}

// Strategy is the default model.SessionStrategy, header-or-predict. It is
// safe for concurrent use. It starts no goroutine of its own: the TTL sweeper
// is a timer that runs only while sessions are live, and Close stops it.
type Strategy struct {
	cfg    Config
	live   *cache[live]
	shadow *cache[shadow]
	seq    atomic.Uint64 // ids for predicted sessions; seeded from the start time
	out    chan model.Session

	expired, evicted, replaced, dropped, agree, disagree atomic.Int64

	mu     sync.Mutex // guards closed, timer and sends on out
	closed bool
	timer  *time.Timer
}

// New returns a Strategy. Call Close when done.
func New(cfg Config) *Strategy {
	cfg = cfg.withDefaults()
	s := &Strategy{cfg: cfg, out: make(chan model.Session, cfg.ClosedBuffer),
		live: newCache[live](cfg.Max, cfg.Shards), shadow: newCache[shadow](cfg.Max, cfg.Shards)}
	s.seq.Store(uint64(time.Now().UnixMilli()))
	return s
}

// Closed returns sessions that ended: quiet for the TTL, evicted when the
// store was full, or replaced by a new run with the same prefix. Each session
// is sent once. The channel is closed by Close. Nothing needs to read it; a
// full channel drops and counts (Stats.ClosedDropped) rather than block.
func (s *Strategy) Closed() <-chan model.Session { return s.out }

// Stats returns the counters.
func (s *Strategy) Stats() Stats {
	return Stats{Live: int(s.live.n.Load()), Expired: s.expired.Load(), Evicted: s.evicted.Load(),
		Replaced: s.replaced.Load(), ClosedDropped: s.dropped.Load(),
		Agree: s.agree.Load(), Disagree: s.disagree.Load()}
}

// Resolve attributes c to a session: the caller's header (Call.SessionHeader)
// when usable, keyed by tenant, else a prediction from c.Messages.
func (s *Strategy) Resolve(_ context.Context, t model.Tenant, c *model.Call) model.Session {
	now := time.Now()
	var sess model.Session
	var gone []model.Session
	if id := c.SessionHeader; validHeaderID(id) {
		sess, gone = s.fromHeader(t.ID, id, now)
		s.observe(t.ID, id, sess.Calls, c.Messages, now)
	} else {
		sess, gone = s.predict(t.ID, c.Messages, now)
	}
	s.emit(gone, &s.evicted)
	s.arm()
	return sess
}

// fromHeader returns the session for a caller-supplied id, counting this call.
func (s *Strategy) fromHeader(tenant, id string, now time.Time) (model.Session, []model.Session) {
	key := "h\x00" + tenant + "\x00" + id
	sh := s.live.shardOf(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	var gone []model.Session
	v := sh.get(key, now)
	if v == nil {
		v = &live{sess: model.Session{ID: id, TenantID: tenant, Source: model.SessionFromHeader, StartedAt: now}}
		gone = sessionsOf(sh.put(key, v, now, s.live.perShard))
	}
	v.sess.Calls++
	v.sess.LastSeen = now
	return v.sess, gone
}

// predict returns the session of a call that carries no header. A call whose
// prefix matches the live run and satisfies the continuation rule joins it;
// otherwise the previous run with that prefix is over and a new one starts.
func (s *Strategy) predict(tenant string, msgs []model.Message, now time.Time) (model.Session, []model.Session) {
	pre := prefixHash(tenant, msgs)
	key := "p\x00" + tenant + "\x00" + pre
	sh := s.live.shardOf(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	var gone []model.Session
	v := sh.get(key, now)
	if v != nil && continues(v.count, msgs) {
		v.sess.Calls++
		v.sess.LastSeen, v.count = now, turns(msgs)
		return v.sess, nil
	}
	if v != nil {
		s.replaced.Add(1)
		s.emitOne(sh.remove(key).sess)
	}
	v = &live{count: turns(msgs), sess: model.Session{ID: pre + "-" + strconv.FormatUint(s.seq.Add(1), 36),
		TenantID: tenant, Source: model.SessionPredicted, StartedAt: now, LastSeen: now, Calls: 1}}
	gone = sessionsOf(sh.put(key, v, now, s.live.perShard))
	return v.sess, gone
}

func sessionsOf(vs []*live) []model.Session {
	out := make([]model.Session, len(vs))
	for i, v := range vs {
		out[i] = v.sess
	}
	return out
}
