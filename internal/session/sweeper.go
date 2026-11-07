package session

import (
	"sync/atomic"
	"time"

	"github.com/ethosagent/tollgate/internal/model"
)

// minSweep keeps the sweeper from spinning for a very small TTL.
const minSweep = 10 * time.Millisecond

// period is how often the sweeper looks for quiet sessions: a quarter of
// the TTL, so a session closes within 1.25 x TTL of its last call.
func (s *Strategy) period() time.Duration { return max(s.cfg.TTL/4, minSweep) }

// arm starts the sweeper timer if it is not running. It is called on every
// Resolve, after a session may have been added. While the timer is armed it
// is one atomic load: no lock (HOT-6). The wakeup cannot be lost: tick
// clears armed before it counts the live sessions, so either it sees a
// session inserted before this load, or this load sees armed false and
// takes the lock to re-arm (Go atomics are sequentially consistent).
func (s *Strategy) arm() {
	if s.armed.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && s.timer == nil {
		s.timer = time.AfterFunc(s.period(), s.tick)
		s.armed.Store(true)
	}
}

// tick sweeps once and re-arms while any session is live. The timer is the
// only background activity: with no live sessions nothing is scheduled, and
// a pending timer is not a goroutine.
func (s *Strategy) tick() {
	now := time.Now()
	s.emit(sessionsOf(s.live.sweep(now, s.cfg.TTL)), &s.expired)
	shadowLive := int64(0)
	if s.shadow != nil {
		s.shadow.sweep(now, s.cfg.TTL)
		shadowLive = s.shadow.n.Load()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	s.armed.Store(false) // before the count: see arm
	if !s.closed && s.live.n.Load()+shadowLive > 0 {
		s.timer = time.AfterFunc(s.period(), s.tick)
		s.armed.Store(true)
	}
}

// emit counts gone against counter and sends each session on Closed.
func (s *Strategy) emit(gone []model.Session, counter *atomic.Int64) {
	counter.Add(int64(len(gone)))
	for _, g := range gone {
		s.emitOne(g)
	}
}

// emitOne sends one closed session without blocking: a full channel drops it
// and counts it, and after Close nothing is sent.
func (s *Strategy) emitOne(g model.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.out <- g:
	default:
		s.dropped.Add(1)
	}
}

// Close stops the sweeper and closes the Closed channel. It is idempotent.
// Resolve keeps working afterwards but closes nothing.
func (s *Strategy) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	close(s.out)
}

// observe is the accuracy harness: for a call that carries a header it also
// predicts, and counts whether the prediction attributes the call as the
// header does. The predictor is right when it joins the call to the same
// header session as before, or starts a new one for a header session's first
// call. It joins a different header session (a false merge) or fails to
// recognise a later call (a false split) when it is wrong.
func (s *Strategy) observe(tenant, id string, calls int, msgs []model.Message, now time.Time) {
	if len(msgs) == 0 {
		return
	}
	key := "s\x00" + tenant + "\x00" + prefixHash(tenant, msgs)
	sh := s.shadow.shardOf(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	v := sh.get(key, now)
	joined := v != nil && continues(v.count, msgs)
	if (joined && v.owner == id) || (!joined && calls == 1) {
		s.agree.Add(1)
	} else {
		s.disagree.Add(1)
	}
	if v == nil {
		v = &shadow{}
		sh.put(key, v, now, s.shadow.perShard)
	}
	v.owner, v.count = id, turns(msgs)
}
