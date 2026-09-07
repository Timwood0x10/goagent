package scoring

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// ErrNilCache is returned when a nil cache is passed to NewCachedScorer.
// Errors are defined in errors.go.

// ErrNilUnderlyingScorer is returned when a nil underlying scorer is passed to NewCachedScorer.
// Errors are defined in errors.go.

// CachedScorer wraps a genome.ScorerFunc with a score cache.
// Before calling the underlying scorer, it checks if an equivalent strategy
// has been scored before and returns the cached result if available.
type CachedScorer struct {
	cache      *ScoreCache
	underlying genome.ScorerFunc // the real scoring function
	scorerType string            // label for cache entries (e.g., "llm", "heuristic")
}

// Ensure CachedScorer implements the expected interface at compile time.
var _ interface {
	Score(ctx context.Context, s *mutation.Strategy) (float64, bool, error)
} = (*CachedScorer)(nil)

// NewCachedScorer creates a cache-backed scorer wrapper.
//
// Args:
//
//	cache - the score cache to use (must not be nil).
//	underlying - the actual scoring function to call on cache miss.
//	scorerType - label for cache entries (e.g., "llm", "heuristic").
//
// Returns:
//
//	*CachedScorer - the wrapped scorer.
//	error - non-nil if cache or underlying is nil.
func NewCachedScorer(cache *ScoreCache, underlying genome.ScorerFunc, scorerType string) (*CachedScorer, error) {
	if cache == nil {
		return nil, fmt.Errorf("new cached scorer: %w", ErrNilCache)
	}
	if underlying == nil {
		return nil, fmt.Errorf("new cached scorer: %w", ErrNilUnderlyingScorer)
	}
	return &CachedScorer{
		cache:      cache,
		underlying: underlying,
		scorerType: scorerType,
	}, nil
}

// Score evaluates a strategy, using cache when possible.
//
// On cache hit: returns the cached score with cached=true and no error.
// On cache miss: calls the underlying scorer, caches the result, and returns
// it with cached=false.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//
// Returns:
//
//	float64 - the fitness score.
//	bool - true if score came from cache.
//	error - non-nil if scoring fails.
func (cs *CachedScorer) Score(ctx context.Context, s *mutation.Strategy) (float64, bool, error) {
	hash, err := StrategyHash(s)
	if err != nil {
		return 0, false, fmt.Errorf("cached scorer hash: %w", err)
	}

	// Check cache first.
	if entry, ok := cs.cache.Get(hash); ok {
		return entry.Score, true, nil
	}

	// Cache miss: call underlying scorer.
	score := cs.underlying(s)

	// Store result in cache.
	entry := MakeEntry(hash, score, cs.scorerType, 1, 1.0)
	cs.cache.Put(hash, entry)

	return score, false, nil
}
