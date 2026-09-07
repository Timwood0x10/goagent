package scoring

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// Scorer type constants for cache entries and tier names.
const (
	ScorerTypeLLM       = "llm"
	ScorerTypeHeuristic = "heuristic"
)

// ErrNilCache is returned when a nil cache is passed to NewTieredScorer.
// Errors are defined in errors.go.

// ErrNilBudget is returned when a nil budget is passed to NewTieredScorer.
// Errors are defined in errors.go.

// ErrNilHeuristicScorer is returned when a nil heuristic scorer is passed to NewTieredScorer.
// Errors are defined in errors.go.

// Tier defines a scoring tier in the pipeline.
type Tier int

const (
	// TierCache checks the score cache first.
	TierCache Tier = iota + 1
	// TierHeuristic applies fast, cheap scoring.
	TierHeuristic
	// TierLLM uses expensive LLM-based scoring (budget-gated).
	TierLLM
)

// String returns a human-readable name for the tier.
func (t Tier) String() string {
	switch t {
	case TierCache:
		return "cache"
	case TierHeuristic:
		return ScorerTypeHeuristic
	case TierLLM:
		return ScorerTypeLLM
	default:
		return "unknown"
	}
}

// TieredScorer implements multi-tier scoring with budget control and caching.
// It routes each strategy through the most cost-effective tier that can provide a valid score.
type TieredScorer struct {
	cache     *ScoreCache
	budget    *Budget
	heuristic genome.ScorerFunc // always-available cheap scorer
	llm       genome.ScorerFunc // optional expensive LLM scorer (may be nil)

	cacheHits      atomic.Int64
	llmCalls       atomic.Int64
	heuristicCalls atomic.Int64
	fallbacks      atomic.Int64
	totalScored    atomic.Int64
}

// TieredScorerConfig holds configuration for creating a tiered scorer.
type TieredScorerConfig struct {
	// Cache is the shared score cache (required).
	Cache *ScoreCache

	// Budget is the LLM call budget tracker (required).
	Budget *Budget

	// HeuristicScorer is the fast, always-available scoring function (required).
	HeuristicScorer genome.ScorerFunc

	// LLMScorer is the optional expensive LLM-based scoring function.
	// When nil, all strategies are scored by the heuristic tier after cache miss.
	LLMScorer genome.ScorerFunc
}

// NewTieredScorer creates a new tiered scorer pipeline.
//
// Args:
//
//	cfg - configuration (Cache, Budget, and HeuristicScorer must not be nil).
//
// Returns:
//
//	*TieredScorer - the configured tiered scorer.
//	error - non-nil if required dependencies are nil.
func NewTieredScorer(cfg TieredScorerConfig) (*TieredScorer, error) {
	if cfg.Cache == nil {
		return nil, fmt.Errorf("new tiered scorer: %w", ErrNilTieredCache)
	}
	if cfg.Budget == nil {
		return nil, fmt.Errorf("new tiered scorer: %w", ErrNilBudget)
	}
	if cfg.HeuristicScorer == nil {
		return nil, fmt.Errorf("new tiered scorer: %w", ErrNilHeuristicScorer)
	}
	return &TieredScorer{
		cache:     cfg.Cache,
		budget:    cfg.Budget,
		heuristic: cfg.HeuristicScorer,
		llm:       cfg.LLMScorer,
	}, nil
}

// Score evaluates a strategy through the tiered pipeline.
// The flow is:
//  1. Check cache → return if hit (TierCache)
//  2. If LLM scorer exists and budget allows → try LLM (TierLLM)
//  3. Otherwise → use heuristic scorer (TierHeuristic)
//
// On LLM failure, automatically falls back to heuristic and records it.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//
// Returns:
//
//	float64 - the fitness score.
//	Tier - which tier produced this score.
//	error - non-nil only if all tiers fail.
func (ts *TieredScorer) Score(ctx context.Context, s *mutation.Strategy) (float64, Tier, error) {
	hash, err := StrategyHash(s)
	if err != nil {
		return 0, 0, fmt.Errorf("tiered scorer hash: %w", err)
	}

	// Tier 1: Check cache first.
	if entry, ok := ts.cache.Get(hash); ok {
		ts.budget.RecordCacheHit()
		ts.cacheHits.Add(1)
		ts.totalScored.Add(1)
		log.Debug("tiered_scorer: cache hit",
			"hash", hash, "score", entry.Score, "scorer_type", entry.ScorerType)
		return entry.Score, TierCache, nil
	}

	// Tier 2: Try LLM scorer if available and within budget.
	if ts.llm != nil && ts.budget.TryRecordLLMCall() {
		score, scored := ts.tryLLMScore(ctx, s, hash)
		if scored {
			return score, TierLLM, nil
		}
		// LLM failed or panicked; fall through to heuristic.
	}

	// Tier 3: Heuristic fallback (always available).
	score := ts.heuristic(s)
	entry := MakeEntry(hash, score, ScorerTypeHeuristic, 1, 0.5)
	ts.cache.Put(hash, entry)

	ts.heuristicCalls.Add(1)
	ts.totalScored.Add(1)

	return score, TierHeuristic, nil
}

// tryLLMScore attempts to score using the LLM scorer with panic recovery.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//	hash - pre-computed strategy hash.
//
// Returns:
//
//	float64 - the fitness score (0.0 on failure).
//	bool - true if scoring succeeded.
func (ts *TieredScorer) tryLLMScore(ctx context.Context, s *mutation.Strategy, hash uint64) (float64, bool) {
	var score float64
	var success bool

	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Warn("tiered_scorer: LLM scorer panicked",
					"hash", hash, "recovery", r)
				ts.budget.RecordFallback()
				ts.fallbacks.Add(1)
				success = false
			}
		}()

		score = ts.llm(s)
		success = true
	}()

	if !success {
		return 0, false
	}

	entry := MakeEntry(hash, score, ScorerTypeLLM, 1, 1.0)
	ts.cache.Put(hash, entry)

	ts.llmCalls.Add(1)
	ts.totalScored.Add(1)

	log.Debug("tiered_scorer: LLM scored",
		"hash", hash, "score", score)
	return score, true
}

// Stats returns scoring statistics since creation or last ResetStats.
//
// Returns:
//
//	map[string]int64 - statistics including cache_hits, llm_calls, heuristic_calls,
//	                  fallbacks, total_scored.
func (ts *TieredScorer) Stats() map[string]int64 {
	return map[string]int64{
		"cache_hits":      ts.cacheHits.Load(),
		"llm_calls":       ts.llmCalls.Load(),
		"heuristic_calls": ts.heuristicCalls.Load(),
		"fallbacks":       ts.fallbacks.Load(),
		"total_scored":    ts.totalScored.Load(),
	}
}

// ResetForGeneration resets budget and per-generation stats.
// Call this at the start of each evolution generation.
func (ts *TieredScorer) ResetForGeneration() {
	ts.budget.Reset()
	ts.cache.NewGeneration()
	ts.cacheHits.Store(0)
	ts.llmCalls.Store(0)
	ts.heuristicCalls.Store(0)
	ts.fallbacks.Store(0)
	ts.totalScored.Store(0)
}
