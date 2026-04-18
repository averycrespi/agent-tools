package inject

import (
	"sync"
	"time"
)

// cacheKey is the composite key for a cached secret value.
type cacheKey struct {
	agent string
	name  string
}

// cacheEntry holds a cached secret value and scope with its expiry time.
type cacheEntry struct {
	value     string
	scope     string
	expiresAt time.Time
}

// Cache is an in-memory TTL cache for resolved secret values.
// It is safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
	ttl     time.Duration
}

// NewCache creates a new Cache with the given default TTL.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		entries: make(map[cacheKey]cacheEntry),
		ttl:     ttl,
	}
}

// Get returns the cached value and scope for (agent, name) if the entry exists
// and has not expired. Returns ("", "", false) when the entry is absent or stale.
func (c *Cache) Get(agent, name string) (value, scope string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, exists := c.entries[cacheKey{agent, name}]
	if !exists {
		return "", "", false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.entries, cacheKey{agent, name})
		return "", "", false
	}
	return e.value, e.scope, true
}

// Set stores value and scope for (agent, name) with the given expiry time.
// If expiresAt is zero, the cache's default TTL is used from now.
func (c *Cache) Set(agent, name, value, scope string, expiresAt time.Time) {
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(c.ttl)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[cacheKey{agent, name}] = cacheEntry{value: value, scope: scope, expiresAt: expiresAt}
}

// Invalidate removes all entries from the cache.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[cacheKey]cacheEntry)
}
