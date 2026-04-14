package service

import (
	"sync"
	"time"
)

// LocalCache is a simple in-memory TTL cache for hot-path lookups
// that rarely change (ban status, channel availability).
// Avoids Redis round-trips on every SendMsg call.
type LocalCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	ttl     time.Duration
}

type cacheEntry struct {
	value    any
	expireAt time.Time
}

func NewLocalCache(ttl time.Duration) *LocalCache {
	return &LocalCache{
		entries: make(map[string]*cacheEntry),
		ttl:     ttl,
	}
}

func (c *LocalCache) Get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expireAt) {
		return nil, false
	}
	return e.value, true
}

func (c *LocalCache) Set(key string, value any) {
	c.mu.Lock()
	c.entries[key] = &cacheEntry{value: value, expireAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

func (c *LocalCache) Delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}
