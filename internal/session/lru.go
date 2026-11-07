package session

import (
	"container/list"
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

// cache is a sharded map with a least-recently-used order per shard and a
// size cap. A shard's mutex is held by the caller around get/put/remove, so
// one lookup-then-update is atomic.
type cache[V any] struct {
	shards   []*shard[V]
	perShard int
	seed     maphash.Seed
	n        atomic.Int64 // entries across all shards
}

type shard[V any] struct {
	mu sync.Mutex
	m  map[string]*list.Element // key -> element holding *item[V]
	ll *list.List               // front is the most recently used
	n  *atomic.Int64
}

type item[V any] struct {
	key  string
	val  *V
	last time.Time
}

// newCache returns a cache of at most about limit entries over shards shards.
// The cap is per shard (limit divided up, rounded up), so a skewed key
// distribution evicts a little early; shards 1 gives an exact global LRU.
func newCache[V any](limit, shards int) *cache[V] {
	shards = max(1, min(shards, limit))
	c := &cache[V]{perShard: (limit + shards - 1) / shards, seed: maphash.MakeSeed()}
	for range shards {
		c.shards = append(c.shards, &shard[V]{m: map[string]*list.Element{}, ll: list.New(), n: &c.n})
	}
	return c
}

func (c *cache[V]) shardOf(key string) *shard[V] {
	return c.shards[maphash.String(c.seed, key)%uint64(len(c.shards))]
}

// get returns the live value for key, or nil, and marks it used at now.
func (s *shard[V]) get(key string, now time.Time) *V {
	e, ok := s.m[key]
	if !ok {
		return nil
	}
	it := e.Value.(*item[V])
	it.last = now
	s.ll.MoveToFront(e)
	return it.val
}

// put stores v under key (which must not be live) and returns the values it
// evicted to stay within perShard.
func (s *shard[V]) put(key string, v *V, now time.Time, perShard int) (evicted []*V) {
	s.m[key] = s.ll.PushFront(&item[V]{key: key, val: v, last: now})
	s.n.Add(1)
	for s.ll.Len() > perShard {
		evicted = append(evicted, s.removeElem(s.ll.Back()))
	}
	return evicted
}

func (s *shard[V]) remove(key string) *V {
	if e, ok := s.m[key]; ok {
		return s.removeElem(e)
	}
	return nil
}

func (s *shard[V]) removeElem(e *list.Element) *V {
	it := s.ll.Remove(e).(*item[V])
	delete(s.m, it.key)
	s.n.Add(-1)
	return it.val
}

// sweep removes and returns every entry unused since before now-ttl.
func (c *cache[V]) sweep(now time.Time, ttl time.Duration) (expired []*V) {
	for _, s := range c.shards {
		s.mu.Lock()
		for e := s.ll.Back(); e != nil; e = s.ll.Back() {
			if now.Sub(e.Value.(*item[V]).last) < ttl {
				break
			}
			expired = append(expired, s.removeElem(e))
		}
		s.mu.Unlock()
	}
	return expired
}
