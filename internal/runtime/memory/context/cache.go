package context

import (
	"context"
	"sync"
	"time"
)

// Cache provides in-memory caching capabilities.
type Cache struct {
	items     map[string]*CacheItem
	mu        sync.RWMutex
	maxSize   int
	ttl       time.Duration
	stopCh    chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	wg        sync.WaitGroup
}

// CacheItem represents a cache entry.
type CacheItem struct {
	Key        string
	Value      interface{}
	Expiration time.Time
}

// NewCache creates a new Cache.
// The cleanup goroutine is started automatically to prevent memory leaks.
func NewCache(maxSize int, ttl time.Duration) *Cache {
	cache := &Cache{
		items:   make(map[string]*CacheItem),
		maxSize: maxSize,
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	// Start cleanup goroutine automatically to prevent memory leaks
	cache.Start()
	return cache
}

// Start starts the cleanup goroutine.
// This method is idempotent - calling it multiple times has no additional effect.
func (c *Cache) Start() {
	c.startOnce.Do(func() {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.cleanupLoop()
		}()
	})
}

// Set stores a value in cache.
func (c *Cache) Set(ctx context.Context, key string, value interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.items) >= c.maxSize {
		c.evictOldest()
	}

	expiration := time.Now().Add(c.ttl)
	c.items[key] = &CacheItem{
		Key:        key,
		Value:      value,
		Expiration: expiration,
	}

	return nil
}

// Get retrieves a value from cache.
func (c *Cache) Get(ctx context.Context, key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, exists := c.items[key]
	if !exists || time.Now().After(item.Expiration) {
		return nil, false
	}
	return item.Value, true
}

// Delete removes a cache entry.
func (c *Cache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
	return nil
}

// Clear removes all entries.
func (c *Cache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*CacheItem)
	return nil
}

// Size returns the number of items.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.items)
}

// evictOldest removes the oldest item.
func (c *Cache) evictOldest() {
	var oldest *CacheItem
	var oldestKey string

	for key, item := range c.items {
		if oldest == nil || item.Expiration.Before(oldest.Expiration) {
			oldest = item
			oldestKey = key
		}
	}

	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}

// cleanupLoop periodically removes expired items.
func (c *Cache) cleanupLoop() {
	interval := c.ttl / 2
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-c.stopCh:
			return
		}
	}
}

// Stop stops the cleanup goroutine.
// This method is idempotent and safe to call multiple times.
// Subsequent calls after the first will be no-ops.
func (c *Cache) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
	c.wg.Wait()
}

// cleanup removes expired items.
func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, item := range c.items {
		if now.After(item.Expiration) {
			delete(c.items, key)
		}
	}
}
