package scoring

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

const (
	testParamTemperature = "temperature"
)

var testStrategy = &mutation.Strategy{
	ID:             "cached-test-strategy",
	Params:         map[string]any{testParamTemperature: 0.7, "top_k": 40},
	PromptTemplate: "think-prompt",
}

func TestNewCachedScorer_NilCache(t *testing.T) {
	underlying := genome.ConstantScorer(50.0)
	_, err := NewCachedScorer(nil, underlying, "test")
	if err == nil {
		t.Fatal("expected error for nil cache")
	}
	if !errors.Is(err, ErrNilCache) {
		t.Fatalf("expected ErrNilCache, got %v", err)
	}
}

func TestNewCachedScorer_NilUnderlying(t *testing.T) {
	cache := NewScoreCache(100)
	_, err := NewCachedScorer(cache, nil, "test")
	if err == nil {
		t.Fatal("expected error for nil underlying scorer")
	}
	if !errors.Is(err, ErrNilUnderlyingScorer) {
		t.Fatalf("expected ErrNilUnderlyingScorer, got %v", err)
	}
}

func TestNewCachedScorer_Success(t *testing.T) {
	cache := NewScoreCache(100)
	underlying := genome.ConstantScorer(50.0)

	cs, err := NewCachedScorer(cache, underlying, ScorerTypeLLM)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cs.cache == nil {
		t.Fatal("cache field should be set")
	}
	if cs.scorerType != ScorerTypeLLM {
		t.Fatalf("scorerType mismatch: want llm, got %s", cs.scorerType)
	}
}

func TestCachedScorer_CacheMiss(t *testing.T) {
	cache := NewScoreCache(100)
	callCount := atomic.Int64{}
	underlying := func(s *mutation.Strategy) float64 {
		callCount.Add(1)
		return 75.5
	}

	cs, _ := NewCachedScorer(cache, underlying, "heuristic")
	score, cached, err := cs.Score(context.Background(), testStrategy)

	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if cached {
		t.Fatal("first call should be a cache miss")
	}
	if score != 75.5 {
		t.Fatalf("score mismatch: want 75.5, got %f", score)
	}
	if callCount.Load() != 1 {
		t.Fatalf("underlying should be called once, got %d", callCount.Load())
	}
}

func TestCachedScorer_CacheHit(t *testing.T) {
	cache := NewScoreCache(100)
	callCount := atomic.Int64{}
	underlying := func(s *mutation.Strategy) float64 {
		callCount.Add(1)
		return 60.0
	}

	cs, _ := NewCachedScorer(cache, underlying, "llm")

	// First call — miss.
	score1, cached1, _ := cs.Score(context.Background(), testStrategy)
	if cached1 {
		t.Fatal("first call should be a cache miss")
	}

	// Second call — hit.
	score2, cached2, _ := cs.Score(context.Background(), testStrategy)
	if !cached2 {
		t.Fatal("second call should be a cache hit")
	}
	if score2 != score1 {
		t.Fatalf("cached score should match original: want %f, got %f", score1, score2)
	}
	if callCount.Load() != 1 {
		t.Fatalf("underlying should only be called once on cache hit, got %d calls", callCount.Load())
	}
}

func TestCachedScorer_DifferentStrategiesIndependent(t *testing.T) {
	cache := NewScoreCache(100)
	underlying := genome.ConstantScorer(42.0)

	cs, _ := NewCachedScorer(cache, underlying, "test")

	stratA := &mutation.Strategy{
		Params:         map[string]any{testParamTemperature: 0.5},
		PromptTemplate: "prompt-a",
	}
	stratB := &mutation.Strategy{
		Params:         map[string]any{testParamTemperature: 0.9},
		PromptTemplate: "prompt-b",
	}

	scoreA, _, _ := cs.Score(context.Background(), stratA)
	scoreB, _, _ := cs.Score(context.Background(), stratB)

	if scoreA != 42.0 || scoreB != 42.0 {
		t.Fatalf("both strategies should get scored: A=%f, B=%f", scoreA, scoreB)
	}

	// Verify independent caching.
	hits, _, _, _ := cache.Stats()
	// Two misses (first call each), then two more Get calls inside Score.
	// Each Score does one Get (miss) then one Put.
	// So we expect 2 misses from the two initial Score calls.
	if hits != 0 {
		t.Fatalf("expected 0 hits for distinct strategies, got %d", hits)
	}
}

func TestCachedScorer_NilStrategy(t *testing.T) {
	cache := NewScoreCache(100)
	underlying := genome.ConstantScorer(50.0)

	cs, _ := NewCachedScorer(cache, underlying, "test")
	_, _, err := cs.Score(context.Background(), nil)

	if err == nil {
		t.Fatal("expected error for nil strategy")
	}
}

func TestCachedScorer_MultipleCacheHits(t *testing.T) {
	cache := NewScoreCache(100)
	callCount := atomic.Int64{}
	underlying := func(s *mutation.Strategy) float64 {
		callCount.Add(1)
		return 99.0
	}

	cs, _ := NewCachedScorer(cache, underlying, "arena")

	// Call 10 times with the same strategy.
	for range 10 {
		score, _, err := cs.Score(context.Background(), testStrategy)
		if err != nil {
			t.Fatalf("Score failed: %v", err)
		}
		if score != 99.0 {
			t.Fatalf("score mismatch: want 99.0, got %f", score)
		}
	}

	if callCount.Load() != 1 {
		t.Fatalf("underlying should be called exactly once for 10 identical requests, got %d", callCount.Load())
	}

	hits, misses, _, _ := cache.Stats()
	if hits != 9 { // first is miss, remaining 9 are hits
		t.Fatalf("expected 9 cache hits, got %d", hits)
	}
	if misses != 1 {
		t.Fatalf("expected 1 cache miss, got %d", misses)
	}
}
