package cache

import (
	"hash/maphash"
	"sync"
	"sync/atomic"
	"time"
)

const shardCount = 64

type entry[V any] struct {
	value   V
	group   string
	expires int64
}

type shard[V any] struct {
	mu sync.RWMutex
	m  map[string]entry[V]
}

type Cache[V any] struct {
	shards   [shardCount]shard[V]
	seed     maphash.Seed
	perShard int
	ttl      time.Duration
	epoch    atomic.Uint64
	hits     atomic.Uint64
	misses   atomic.Uint64
}

func New[V any](maxEntries int, ttl time.Duration) *Cache[V] {
	per := maxEntries / shardCount
	if per < 1 {
		per = 1
	}
	c := &Cache[V]{seed: maphash.MakeSeed(), perShard: per, ttl: ttl}
	for i := range c.shards {
		c.shards[i].m = make(map[string]entry[V])
	}
	return c
}

func (c *Cache[V]) shardFor(key string) *shard[V] {
	return &c.shards[maphash.String(c.seed, key)%shardCount]
}

func (c *Cache[V]) Epoch() uint64 {
	return c.epoch.Load()
}

func (c *Cache[V]) Get(key string) (V, bool) {
	s := c.shardFor(key)
	s.mu.RLock()
	e, ok := s.m[key]
	s.mu.RUnlock()
	if ok && e.expires > time.Now().UnixNano() {
		c.hits.Add(1)
		return e.value, true
	}
	c.misses.Add(1)
	var zero V
	return zero, false
}

func (c *Cache[V]) Set(key, group string, value V, epoch uint64) {
	if c.epoch.Load() != epoch {
		return
	}
	now := time.Now().UnixNano()
	s := c.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.epoch.Load() != epoch {
		return
	}
	if _, exists := s.m[key]; !exists && len(s.m) >= c.perShard {
		c.evict(s, now)
	}
	s.m[key] = entry[V]{value: value, group: group, expires: now + int64(c.ttl)}
}

func (c *Cache[V]) evict(s *shard[V], now int64) {
	for k, e := range s.m {
		if e.expires <= now {
			delete(s.m, k)
		}
	}
	for k := range s.m {
		if len(s.m) < c.perShard {
			return
		}
		delete(s.m, k)
	}
}

func (c *Cache[V]) Delete(key string) {
	c.epoch.Add(1)
	s := c.shardFor(key)
	s.mu.Lock()
	delete(s.m, key)
	s.mu.Unlock()
}

func (c *Cache[V]) DeleteGroup(group string) {
	c.epoch.Add(1)
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		for k, e := range s.m {
			if e.group == group {
				delete(s.m, k)
			}
		}
		s.mu.Unlock()
	}
}

func (c *Cache[V]) Clear() {
	c.epoch.Add(1)
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		clear(s.m)
		s.mu.Unlock()
	}
}

func (c *Cache[V]) Len() int {
	n := 0
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.RLock()
		n += len(s.m)
		s.mu.RUnlock()
	}
	return n
}

func (c *Cache[V]) Stats() (hits, misses uint64) {
	return c.hits.Load(), c.misses.Load()
}
