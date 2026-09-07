// Package experience provides experience ranking service.
package experience

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"
)

// RankingService provides multi-signal ranking for experiences.
// This implements a lightweight bandit system using usage and recency signals.
type RankingService struct {
	mu     sync.RWMutex
	logger *slog.Logger
	// Default weights
	usageWeight   float64
	recencyWeight float64
	recencyDays   float64
}

// NewRankingService creates a new RankingService instance.
// Args:
// Returns new RankingService instance with default weights.
func NewRankingService() *RankingService {
	return &RankingService{
		logger:        slog.Default(),
		usageWeight:   0.05, // 5% boost per log usage
		recencyWeight: 0.05, // 5% boost for recent experiences
		recencyDays:   30.0, // 30-day half-life
	}
}

// RankingWeights defines the weights for ranking signals.
type RankingWeights struct {
	// UsageWeight is the weight for usage count signal.
	UsageWeight float64 `json:"usage_weight"`

	// RecencyWeight is the weight for recency signal.
	RecencyWeight float64 `json:"recency_weight"`

	// RecencyDays is the half-life in days for recency decay.
	RecencyDays float64 `json:"recency_days"`
}

// DefaultRankingWeights returns the default ranking weights.
func DefaultRankingWeights() *RankingWeights {
	return &RankingWeights{
		UsageWeight:   0.05,
		RecencyWeight: 0.05,
		RecencyDays:   30.0,
	}
}

// Configure updates the ranking weights.
// Thread-safe via mu write lock.
// Args:
// weights - new ranking weights configuration.
// Returns error if weights are invalid.
func (s *RankingService) Configure(weights *RankingWeights) error {
	if weights == nil {
		return nil
	}

	if weights.UsageWeight < 0 || weights.UsageWeight > 1.0 {
		return fmt.Errorf("usage weight must be between 0 and 1, got %f", weights.UsageWeight)
	}

	if weights.RecencyWeight < 0 || weights.RecencyWeight > 1.0 {
		return fmt.Errorf("recency weight must be between 0 and 1, got %f", weights.RecencyWeight)
	}

	if weights.RecencyDays <= 0 {
		return fmt.Errorf("recency days must be positive, got %f", weights.RecencyDays)
	}

	s.mu.Lock()
	s.usageWeight = weights.UsageWeight
	s.recencyWeight = weights.RecencyWeight
	s.recencyDays = weights.RecencyDays
	s.mu.Unlock()

	s.logger.Info("Ranking weights configured",
		"usage_weight", s.usageWeight,
		"recency_weight", s.recencyWeight,
		"recency_days", s.recencyDays,
	)

	return nil
}

// Rank ranks experiences using multi-signal scoring.
// This implements the formula:
// FinalScore = SemanticScore + UsageBoost + RecencyBoost
//
// Where:
// - UsageBoost = min(log(1 + usage_count) * weight, 0.2)
// - RecencyBoost = exp(-age_days / recency_days) * weight
//
// Args:
// ctx - operation context.
// experiences - experiences to rank.
// baseScores - base semantic similarity scores (from vector search).
// Returns ranked experiences with scores, or error if input lengths mismatch.
func (s *RankingService) Rank(ctx context.Context, experiences []*Experience, baseScores []float64) ([]*RankedExperience, error) {
	if len(experiences) == 0 {
		return []*RankedExperience{}, nil
	}

	if len(experiences) != len(baseScores) {
		return nil, fmt.Errorf("experience count %d does not match base scores count %d",
			len(experiences), len(baseScores))
	}

	now := time.Now()

	s.mu.RLock()
	usageWeight := s.usageWeight
	recencyWeight := s.recencyWeight
	recencyDays := s.recencyDays
	s.mu.RUnlock()

	// Calculate scores for each experience
	ranked := make([]*RankedExperience, len(experiences))
	for i, exp := range experiences {
		semanticScore := baseScores[i]

		// Calculate usage boost
		usageBoost := s.calculateUsageBoost(exp.GetUsageCount(), usageWeight)

		// Calculate recency boost
		recencyBoost := s.calculateRecencyBoost(exp.CreatedAt, now, recencyWeight, recencyDays)

		// Final score: includes semantic match, usage frequency, recency,
		// and the persisted Score (bandit feedback signal from RecordFailure).
		finalScore := semanticScore + usageBoost + recencyBoost + exp.Score

		ranked[i] = &RankedExperience{
			Experience:      exp,
			FinalScore:      finalScore,
			SemanticScore:   semanticScore,
			UsageBoost:      usageBoost,
			RecencyBoost:    recencyBoost,
			ConflictChecked: false,
		}
	}

	// Sort by final score (descending)
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].FinalScore > ranked[j].FinalScore
	})

	s.logger.Debug("Experiences ranked",
		"total", len(ranked),
		"top_score", ranked[0].FinalScore,
		"bottom_score", ranked[len(ranked)-1].FinalScore,
	)

	return ranked, nil
}

// calculateUsageBoost calculates the usage count boost.
// This uses log(1 + count) to prevent explosion.
// The boost is capped at 0.2 to prevent old experiences from dominating.
// Args:
// usageCount - number of times the experience was successfully used.
// usageWeight - weight multiplier for usage signal.
// Returns usage boost score.
func (s *RankingService) calculateUsageBoost(usageCount int, usageWeight float64) float64 {
	if usageCount <= 0 {
		return 0.0
	}

	// Logarithmic scaling: log(1 + count)
	boost := math.Log1p(float64(usageCount)) * usageWeight

	// Cap at 0.2 to prevent dominance
	maxBoost := 0.2
	if boost > maxBoost {
		boost = maxBoost
	}

	return boost
}

// calculateRecencyBoost calculates the recency boost.
// This uses exponential decay based on age.
// Args:
// createdAt - time when the experience was created.
// now - current time.
// recencyWeight - weight multiplier for recency signal.
// recencyDays - half-life in days for decay.
// Returns recency boost score.
func (s *RankingService) calculateRecencyBoost(createdAt time.Time, now time.Time, recencyWeight, recencyDays float64) float64 {
	if createdAt.IsZero() {
		return 0.0
	}

	// Calculate age in days
	ageHours := now.Sub(createdAt).Hours()
	ageDays := ageHours / 24.0

	// Exponential decay: exp(-age / half_life)
	decay := math.Exp(-ageDays / recencyDays)

	// Apply weight
	boost := decay * recencyWeight

	return boost
}
