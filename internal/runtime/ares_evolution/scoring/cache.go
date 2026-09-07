package scoring

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// CacheEntry holds a cached score record for a strategy.
type CacheEntry struct {
	// Hash is the strategy hash this entry corresponds to.
	Hash uint64

	// Score is the cached fitness score.
	Score float64

	// ScorerType identifies which scorer produced this score (e.g., "heuristic", "llm", "arena").
	ScorerType string

	// Timestamp when this entry was created (Unix nanos).
	Timestamp int64

	// SampleCount is how many evaluation samples contributed to this score.
	SampleCount int

	// Confidence is the confidence level (0-1) of this score.
	Confidence float64
}

type cacheItem struct {
	hash       uint64
	entry      CacheEntry
	generation uint64 // generation when this entry was created
}

// ScoreCache provides thread-safe score caching for evolved strategies.
// It avoids redundant LLM calls by caching previously computed scores.
//
// Uses LRU eviction via container/list when at capacity.
// Zero-value is NOT usable; use NewScoreCache to create an instance.
type ScoreCache struct {
	mu          sync.RWMutex
	entries     map[uint64]*list.Element // hash -> list element
	lru         list.List                // LRU order (front = most recently used)
	maxSize     int                      // maximum cache entries; 0 = unlimited
	maxCacheAge int                      // max generations before stale; 0 = unlimited
	generation  uint64                   // current generation counter
	hits        int64                    // number of cache hits
	misses      int64                    // number of cache misses
	evictions   int64                    // number of entries evicted due to capacity
}

// NewScoreCache creates a new score cache.
//
// Args:
//
//	maxSize - maximum entries (0 = unlimited).
//
// Returns:
//
//	*ScoreCache - the cache instance.
func NewScoreCache(maxSize int) *ScoreCache {
	return &ScoreCache{
		entries: make(map[uint64]*list.Element),
		maxSize: maxSize,
	}
}

// SetMaxCacheAge sets the maximum number of generations an entry is valid.
// After this many generations, the entry is treated as a cache miss and
// re-evaluated on the next lookup. 0 (default) means unlimited — entries
// never expire by age. Use NewGeneration() to advance the counter.
func (c *ScoreCache) SetMaxCacheAge(generations int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxCacheAge = generations
}

// NewGeneration advances the generation counter. Entries older than
// maxCacheAge will be treated as misses on subsequent Get() calls.
func (c *ScoreCache) NewGeneration() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
}

// Get retrieves a cached score for the given strategy hash.
// This method is thread-safe and promotes the entry as most recently used.
// Entries older than maxCacheAge generations are treated as misses.
//
// Args:
//
//	hash - the strategy hash to look up.
//
// Returns:
//
//	CacheEntry - the cached entry, zero value if not found.
//	bool - true if found in cache.
func (c *ScoreCache) Get(hash uint64) (CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[hash]
	if !ok {
		atomic.AddInt64(&c.misses, 1)
		return CacheEntry{}, false
	}

	item := elem.Value.(*cacheItem)
	if c.maxCacheAge > 0 && c.generation-item.generation > uint64(c.maxCacheAge) {
		// Entry is stale: evict and treat as miss.
		delete(c.entries, hash)
		c.lru.Remove(elem)
		atomic.AddInt64(&c.misses, 1)
		return CacheEntry{}, false
	}

	c.lru.MoveToFront(elem)
	atomic.AddInt64(&c.hits, 1)
	return item.entry, true
}

// Put stores a score in the cache.
// If the cache is full, evicts the least recently used entry.
// This method is thread-safe and uses a write lock.
//
// Args:
//
//	hash - the strategy hash.
//	entry - the cache entry to store.
func (c *ScoreCache) Put(hash uint64, entry CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Existing entry: update in place, refresh generation, and move to front.
	if elem, ok := c.entries[hash]; ok {
		item := elem.Value.(*cacheItem)
		item.entry = entry
		item.generation = c.generation
		c.lru.MoveToFront(elem)
		return
	}

	// Evict LRU entry if at capacity.
	if c.maxSize > 0 && len(c.entries) >= c.maxSize {
		back := c.lru.Back()
		if back != nil {
			item := back.Value.(*cacheItem)
			delete(c.entries, item.hash)
			c.lru.Remove(back)
			c.evictions++
		}
	}

	item := &cacheItem{hash: hash, entry: entry, generation: c.generation}
	elem := c.lru.PushFront(item)
	c.entries[hash] = elem
}

// Stats returns cache statistics.
//
// Returns:
//
//	hits - number of cache hits since creation (or last ResetStats).
//	misses - number of cache misses.
//	size - current entry count.
//	evictions - number of entries evicted due to capacity.
func (c *ScoreCache) Stats() (hits, misses, size, evictions int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.misses, int64(len(c.entries)), c.evictions
}

// Clear removes all entries from the cache and resets statistics.
func (c *ScoreCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[uint64]*list.Element)
	c.lru.Init()
	atomic.StoreInt64(&c.hits, 0)
	atomic.StoreInt64(&c.misses, 0)
	c.evictions = 0
}

// nowUnixNano returns the current Unix time in nanoseconds.
// Extracted as a package-level var for testability.
var nowUnixNano = func() int64 {
	return time.Now().UnixNano()
}

// MakeEntry constructs a CacheEntry with the current timestamp.
//
// Args:
//
//	hash - the strategy hash.
//	score - the fitness score.
//	scorerType - label identifying the scorer (e.g., "llm", "heuristic").
//	sampleCount - number of evaluation samples that contributed.
//	confidence - confidence level (0-1).
//
// Returns:
//
//	CacheEntry - the constructed cache entry.
func MakeEntry(hash uint64, score float64, scorerType string, sampleCount int, confidence float64) CacheEntry {
	return CacheEntry{
		Hash:        hash,
		Score:       score,
		ScorerType:  scorerType,
		Timestamp:   nowUnixNano(),
		SampleCount: sampleCount,
		Confidence:  confidence,
	}
}
