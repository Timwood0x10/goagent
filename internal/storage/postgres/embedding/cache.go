package embedding

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/Timwood0x10/ares/internal/errors"
)

// RedisClient defines the interface for Redis operations.
// This allows for optional Redis dependency and easy testing.
type RedisClient interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Del(ctx context.Context, keys ...string) error
	Keys(ctx context.Context, pattern string) ([]string, error)
	// Scan performs a cursor-based SCAN iteration, which is non-blocking
	// unlike Keys. Returns the next cursor (0 means iteration complete),
	// the keys found in this batch, and any error.
	Scan(ctx context.Context, cursor uint64, match string, count int64) (uint64, []string, error)
}

// CacheKey represents a cache key for embeddings.
type CacheKey struct {
	Text   string
	Model  string
	Method string
}

// String returns the string representation of the cache key using BLAKE2b-128 hash.
// BLAKE2b provides:
// - Security: Cryptographically secure hash function
// - Performance: 20-30% faster than SHA256
// - Efficiency: 128-bit output is sufficient for cache key collision resistance
func (k *CacheKey) String() string {
	// Standard format: hash(text + model + method)
	keyData := fmt.Sprintf("%s|%s|%s", k.Text, k.Model, k.Method)

	// Use BLAKE2b-256 and truncate to 128 bits for security and performance
	hash := blake2b.Sum256([]byte(keyData))

	// Convert first 16 bytes (128 bits) to hex string
	return fmt.Sprintf("embed:%s", hex.EncodeToString(hash[:16]))
}

// EmbeddingCache provides caching functionality for embeddings.
// It supports both Redis and in-memory cache as fallback.
type EmbeddingCache struct {
	redis   RedisClient
	memory  *MemoryCache
	ttl     time.Duration
	enabled atomic.Bool
}

// NewEmbeddingCache creates a new embedding cache.
// If redisClient is nil, it will use in-memory cache only.
func NewEmbeddingCache(redisClient RedisClient, ttl time.Duration) *EmbeddingCache {
	c := &EmbeddingCache{
		redis:  redisClient,
		memory: NewMemoryCache(),
		ttl:    ttl,
	}
	c.enabled.Store(true)
	return c
}

// Get retrieves an embedding from cache.
// It tries Redis first, then falls back to memory cache.
func (c *EmbeddingCache) Get(ctx context.Context, key *CacheKey) ([]float64, bool) {
	if !c.enabled.Load() {
		return nil, false
	}

	keyStr := key.String()

	// Try Redis first
	if c.redis != nil {
		val, err := c.redis.Get(ctx, keyStr)
		if err == nil {
			var embedding []float64
			if err := json.Unmarshal([]byte(val), &embedding); err == nil {
				return embedding, true
			}
		}
	}

	// Fallback to memory cache
	if c.memory != nil {
		val, found := c.memory.Get(keyStr)
		if found {
			var embedding []float64
			if err := json.Unmarshal(val, &embedding); err == nil {
				return embedding, true
			}
		}
	}

	return nil, false
}

// Set stores an embedding in cache.
// It stores in both Redis and memory cache (if available).
func (c *EmbeddingCache) Set(ctx context.Context, key *CacheKey, embedding []float64) error {
	if !c.enabled.Load() {
		return nil
	}

	data, err := json.Marshal(embedding)
	if err != nil {
		return errors.Wrap(err, "marshal embedding")
	}

	keyStr := key.String()

	// Try to store in Redis
	if c.redis != nil {
		if err := c.redis.Set(ctx, keyStr, string(data), c.ttl); err != nil {
			// Redis error is not fatal, continue with memory cache
			log.Debug("Failed to store in Redis cache", "error", err)
		}
	}

	// Always store in memory cache
	if c.memory != nil {
		c.memory.Set(keyStr, data, c.ttl)
	}

	return nil
}

// Delete removes an embedding from cache.
func (c *EmbeddingCache) Delete(ctx context.Context, key *CacheKey) error {
	if !c.enabled.Load() {
		return nil
	}

	keyStr := key.String()

	// Try to delete from Redis
	if c.redis != nil {
		if err := c.redis.Del(ctx, keyStr); err != nil {
			log.Warn("embedding cache: del key", "key", keyStr, "error", err)
		}
	}

	// Delete from memory cache
	if c.memory != nil {
		c.memory.Delete(keyStr)
	}

	return nil
}

// Clear removes all embeddings from cache.
func (c *EmbeddingCache) Clear(ctx context.Context) error {
	if !c.enabled.Load() {
		return nil
	}

	// Use SCAN instead of KEYS to avoid blocking Redis on large keyspaces.
	if c.redis != nil {
		var cursor uint64
		for {
			next, keys, err := c.redis.Scan(ctx, cursor, "embed:*", 100)
			if err != nil {
				log.Warn("embedding cache: scan keys", "error", err)
				break
			}
			if len(keys) > 0 {
				if err := c.redis.Del(ctx, keys...); err != nil {
					log.Warn("embedding cache: del keys", "error", err)
				}
			}
			if next == 0 {
				break
			}
			cursor = next
		}
	}

	// Clear memory cache
	if c.memory != nil {
		keys := c.memory.Keys("embed:*")
		for _, key := range keys {
			c.memory.Delete(key)
		}
	}

	return nil
}

// Close stops the cache and cleans up resources.
// This should be called when the cache is no longer needed to prevent goroutine leaks.
func (c *EmbeddingCache) Close() {
	if c.memory != nil {
		c.memory.Close()
	}
}

// GetStats returns cache statistics.
func (c *EmbeddingCache) GetStats(ctx context.Context) (*CacheStats, error) {
	if !c.enabled.Load() {
		return &CacheStats{Enabled: false}, nil
	}

	// Use SCAN instead of KEYS to avoid blocking Redis on large keyspaces.
	var redisKeys int
	if c.redis != nil {
		var cursor uint64
		for {
			next, keys, err := c.redis.Scan(ctx, cursor, "embed:*", 100)
			if err != nil {
				log.Warn("embedding cache: scan keys for stats", "error", err)
				break
			}
			redisKeys += len(keys)
			if next == 0 {
				break
			}
			cursor = next
		}
	}

	var memoryKeys int
	if c.memory != nil {
		memoryKeys = len(c.memory.Keys("embed:*"))
	}

	return &CacheStats{
		Enabled:    true,
		RedisKeys:  redisKeys,
		MemoryKeys: memoryKeys,
		TotalKeys:  redisKeys + memoryKeys,
		TTL:        c.ttl,
	}, nil
}

// CacheStats represents cache statistics.
type CacheStats struct {
	Enabled    bool
	RedisKeys  int // Number of keys in Redis
	MemoryKeys int // Number of keys in memory
	TotalKeys  int // Total number of keys
	TTL        time.Duration
}

// Enable enables the cache.
func (c *EmbeddingCache) Enable() {
	c.enabled.Store(true)
}

// Disable disables the cache.
func (c *EmbeddingCache) Disable() {
	c.enabled.Store(false)
}

// IsEnabled returns whether the cache is enabled.
func (c *EmbeddingCache) IsEnabled() bool {
	return c.enabled.Load()
}

// MemoryCache provides in-memory caching as a fallback.
type MemoryCache struct {
	mu       sync.RWMutex
	items    map[string]cacheItem
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

type cacheItem struct {
	value      []byte
	expiration time.Time
}

// NewMemoryCache creates a new in-memory cache.
// The cleanup goroutine runs periodically to remove expired items.
func NewMemoryCache() *MemoryCache {
	ctx, cancel := context.WithCancel(context.Background())
	m := &MemoryCache{
		items:  make(map[string]cacheItem),
		ctx:    ctx,
		cancel: cancel,
	}

	// Start cleanup goroutine with proper lifecycle management
	m.wg.Add(1)
	go m.cleanup()

	return m
}

// Close stops the cleanup goroutine and cleans up resources.
// This should be called when the cache is no longer needed to prevent goroutine leaks.
func (m *MemoryCache) Close() {
	m.stopOnce.Do(func() {
		m.cancel()
		m.wg.Wait()

		// Clear all items
		m.mu.Lock()
		m.items = make(map[string]cacheItem)
		m.mu.Unlock()
	})
}

// Get retrieves a value from memory cache.
func (m *MemoryCache) Get(key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	item, found := m.items[key]
	if !found {
		return nil, false
	}

	// Check expiration
	if time.Now().After(item.expiration) {
		return nil, false
	}

	// Return a copy so the caller cannot mutate the cached backing array.
	out := make([]byte, len(item.value))
	copy(out, item.value)
	return out, true
}

// Set stores a value in memory cache. The value is copied so the caller
// cannot mutate the cached entry through the original slice.
func (m *MemoryCache) Set(key string, value []byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	v := make([]byte, len(value))
	copy(v, value)
	m.items[key] = cacheItem{
		value:      v,
		expiration: time.Now().Add(ttl),
	}
}

// Delete removes a value from memory cache.
func (m *MemoryCache) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.items, key)
}

// Keys returns all keys matching pattern.
func (m *MemoryCache) Keys(pattern string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var keys []string
	for k := range m.items {
		// Simple pattern matching (supports * wildcard)
		if matchPattern(k, pattern) {
			keys = append(keys, k)
		}
	}
	return keys
}

// cleanup removes expired items periodically.
// This goroutine runs until the context is cancelled or Close is called.
func (m *MemoryCache) cleanup() {
	defer m.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			// Context cancelled, stop cleanup
			return

		case <-ticker.C:
			m.mu.Lock()
			now := time.Now()
			for k, item := range m.items {
				if now.After(item.expiration) {
					delete(m.items, k)
				}
			}
			m.mu.Unlock()
		}
	}
}

// matchPattern checks if key matches pattern (supports * wildcard).
func matchPattern(key, pattern string) bool {
	if pattern == "*" {
		return true
	}

	// Simple implementation: if pattern ends with *, check prefix
	if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(key) >= len(prefix) && key[:len(prefix)] == prefix
	}

	return key == pattern
}
