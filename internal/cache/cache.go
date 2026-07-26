// Package cache is a small TTL cache, sized so a busy channel cannot burn the
// BF4DB quota (70 requests/minute) on the same lookup over and over.
package cache

import (
	"sync"
	"time"
)

type entry[T any] struct {
	value   T
	expires time.Time
}

// Cache maps keys to values that expire. It is safe for concurrent use.
type Cache[T any] struct {
	ttl time.Duration
	max int

	mu      sync.Mutex
	entries map[string]entry[T]
	now     func() time.Time // swapped in tests
}

// New builds a cache holding at most max entries for ttl each.
func New[T any](ttl time.Duration, max int) *Cache[T] {
	if max < 1 {
		max = 1
	}
	return &Cache[T]{
		ttl:     ttl,
		max:     max,
		entries: make(map[string]entry[T], max),
		now:     time.Now,
	}
}

// Get returns the cached value when it is present and still fresh.
func (c *Cache[T]) Get(key string) (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, false
	}
	if c.now().After(e.expires) {
		delete(c.entries, key)
		var zero T
		return zero, false
	}
	return e.value, true
}

// Set stores a value, evicting expired entries (and then the soonest to
// expire) when the cache is full.
func (c *Cache[T]) Set(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if _, replacing := c.entries[key]; !replacing && len(c.entries) >= c.max {
		c.evictLocked(now)
	}
	c.entries[key] = entry[T]{value: value, expires: now.Add(c.ttl)}
}

// Len reports how many entries are currently held, expired ones included.
func (c *Cache[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache[T]) evictLocked(now time.Time) {
	oldestKey := ""
	var oldest time.Time

	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
			continue
		}
		if oldestKey == "" || e.expires.Before(oldest) {
			oldestKey, oldest = k, e.expires
		}
	}
	if len(c.entries) >= c.max && oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}
