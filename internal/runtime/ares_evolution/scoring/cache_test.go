package scoring

import (
	"sync"
	"testing"
	"time"
)

const (
	testScorerTypeLLM = "llm"
)

func TestNewScoreCache_UnlimitedSize(t *testing.T) {
	c := NewScoreCache(0)
	if c == nil {
		t.Fatal("NewScoreCache returned nil")
	}
	if c.maxSize != 0 {
		t.Fatalf("expected maxSize=0, got %d", c.maxSize)
	}
	if c.entries == nil {
		t.Fatal("entries map should be initialized")
	}
}

func TestScoreCache_PutAndGet(t *testing.T) {
	c := NewScoreCache(0)

	entry := CacheEntry{
		Hash:        12345,
		Score:       85.5,
		ScorerType:  testScorerTypeLLM,
		Timestamp:   time.Now().UnixNano(),
		SampleCount: 10,
		Confidence:  0.95,
	}

	c.Put(12345, entry)

	got, ok := c.Get(12345)
	if !ok {
		t.Fatal("expected to find entry after Put")
	}
	if got.Score != 85.5 {
		t.Fatalf("score mismatch: want 85.5, got %f", got.Score)
	}
	if got.ScorerType != testScorerTypeLLM {
		t.Fatalf("scorerType mismatch: want llm, got %s", got.ScorerType)
	}
	if got.SampleCount != 10 {
		t.Fatalf("sampleCount mismatch: want 10, got %d", got.SampleCount)
	}
	if got.Confidence != 0.95 {
		t.Fatalf("confidence mismatch: want 0.95, got %f", got.Confidence)
	}
}

func TestScoreCache_GetMiss(t *testing.T) {
	c := NewScoreCache(0)

	_, ok := c.Get(99999)
	if ok {
		t.Fatal("expected miss for non-existent hash")
	}
}

func TestScoreCache_PutOverwrite(t *testing.T) {
	c := NewScoreCache(0)

	entry1 := CacheEntry{Hash: 100, Score: 50.0, ScorerType: "heuristic"}
	entry2 := CacheEntry{Hash: 100, Score: 90.0, ScorerType: "arena"}

	c.Put(100, entry1)
	c.Put(100, entry2)

	got, _ := c.Get(100)
	if got.Score != 90.0 {
		t.Fatalf("overwrite should replace score: want 90.0, got %f", got.Score)
	}
	if got.ScorerType != "arena" {
		t.Fatalf("overwrite should replace scorerType: want arena, got %s", got.ScorerType)
	}
}

func TestScoreCache_Eviction(t *testing.T) {
	c := NewScoreCache(2)

	now := time.Now().UnixNano()

	// Fill cache to capacity.
	c.Put(1, CacheEntry{Hash: 1, Score: 10, Timestamp: now + 100})
	c.Put(2, CacheEntry{Hash: 2, Score: 20, Timestamp: now + 200})

	// Third entry triggers eviction of oldest (hash=1).
	c.Put(3, CacheEntry{Hash: 3, Score: 30, Timestamp: now + 300})

	// Oldest entry (hash=1) should be evicted.
	_, ok := c.Get(1)
	if ok {
		t.Fatal("oldest entry should have been evicted")
	}

	// Remaining entries should still exist.
	for _, hash := range []uint64{2, 3} {
		if _, ok := c.Get(hash); !ok {
			t.Fatalf("entry %d should still exist after eviction", hash)
		}
	}

	// Check eviction count.
	_, _, _, evictions := c.Stats()
	if evictions != 1 {
		t.Fatalf("expected 1 eviction, got %d", evictions)
	}
}

func TestScoreCache_Eviction_SameKeyNoEviction(t *testing.T) {
	c := NewScoreCache(2)

	c.Put(1, CacheEntry{Hash: 1, Score: 10, Timestamp: 100})
	c.Put(2, CacheEntry{Hash: 2, Score: 20, Timestamp: 200})

	// Updating existing key should NOT trigger eviction.
	c.Put(1, CacheEntry{Hash: 1, Score: 15, Timestamp: 300})

	_, _, size, evictions := c.Stats()
	if size != 2 {
		t.Fatalf("size should remain 2, got %d", size)
	}
	if evictions != 0 {
		t.Fatalf("no eviction expected when updating existing key, got %d", evictions)
	}
}

func TestScoreCache_Stats(t *testing.T) {
	c := NewScoreCache(0)

	c.Put(1, CacheEntry{Hash: 1, Score: 10})
	c.Put(2, CacheEntry{Hash: 2, Score: 20})

	// Generate hits and misses.
	c.Get(1)   // hit
	c.Get(2)   // hit
	c.Get(999) // miss

	hits, misses, size, evictions := c.Stats()
	if hits != 2 {
		t.Fatalf("expected 2 hits, got %d", hits)
	}
	if misses != 1 {
		t.Fatalf("expected 1 miss, got %d", misses)
	}
	if size != 2 {
		t.Fatalf("expected size 2, got %d", size)
	}
	if evictions != 0 {
		t.Fatalf("expected 0 evictions, got %d", evictions)
	}
}

func TestScoreCache_Clear(t *testing.T) {
	c := NewScoreCache(0)

	c.Put(1, CacheEntry{Hash: 1, Score: 10})
	c.Put(2, CacheEntry{Hash: 2, Score: 20})
	c.Get(1) // hit
	c.Get(9) // miss

	c.Clear()

	// Entries cleared.
	hits, misses, size, evictions := c.Stats()
	if hits != 0 || misses != 0 || size != 0 || evictions != 0 {
		t.Fatalf("stats should be reset after Clear: hits=%d misses=%d size=%d evictions=%d",
			hits, misses, size, evictions)
	}

	// Verify cleared entries are gone.
	_, ok := c.Get(1)
	if ok {
		t.Fatal("entries should be cleared")
	}
}

func TestScoreCache_ConcurrentAccess(t *testing.T) {
	c := NewScoreCache(1000)
	var wg sync.WaitGroup

	const goroutines = 50
	const opsPerGoroutine = 100

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				hash := uint64(id*opsPerGoroutine + j)
				c.Put(hash, CacheEntry{
					Hash:      hash,
					Score:     float64(hash),
					Timestamp: int64(hash),
				})
				c.Get(hash)
			}
		}(i)
	}

	wg.Wait()

	hits, misses, size, _ := c.Stats()
	totalOps := goroutines * opsPerGoroutine
	if int64(totalOps) != hits+misses {
		t.Fatalf("total operations mismatch: expected %d, got hits+misses=%d+%d=%d",
			totalOps, hits, misses, hits+misses)
	}
	if size > 1000 {
		t.Fatalf("cache exceeded max size: %d", size)
	}
}

func TestMakeEntry(t *testing.T) {
	before := time.Now().UnixNano()

	entry := MakeEntry(123, 88.5, testScorerTypeLLM, 5, 0.9)

	after := time.Now().UnixNano()

	if entry.Hash != 123 {
		t.Fatalf("hash mismatch: want 123, got %d", entry.Hash)
	}
	if entry.Score != 88.5 {
		t.Fatalf("score mismatch: want 88.5, got %f", entry.Score)
	}
	if entry.ScorerType != "llm" {
		t.Fatalf("scorerType mismatch: want llm, got %s", entry.ScorerType)
	}
	if entry.SampleCount != 5 {
		t.Fatalf("sampleCount mismatch: want 5, got %d", entry.SampleCount)
	}
	if entry.Confidence != 0.9 {
		t.Fatalf("confidence mismatch: want 0.9, got %f", entry.Confidence)
	}
	if entry.Timestamp < before || entry.Timestamp > after {
		t.Fatalf("timestamp out of range: got %d, expected [%d, %d]",
			entry.Timestamp, before, after)
	}
}
