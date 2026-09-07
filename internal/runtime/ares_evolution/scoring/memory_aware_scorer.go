// Package scoring provides memory-aware scoring that extends the tiered scorer
// with evidence-based bonuses and cost/latency penalties derived from past
// experiences stored in the experience repository.
package scoring

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// DefaultTaskType is the fallback task type when no task type information is found.
const DefaultTaskType = "default"

// ExperienceProvider defines the interface for retrieving similar past
// experiences that can inform strategy scoring. Implementations may query
// a vector database, keyword index, or other experience store.
type ExperienceProvider interface {
	// FindSimilar returns the count of similar experiences for the given
	// task type along with a confidence factor (0-1) indicating how well
	// the matched experiences align with the current context.
	//
	// Args:
	//
	//	ctx - operation context.
	//	taskType - the type of task being evaluated.
	//	limit - maximum number of similar experiences to consider.
	//
	// Returns:
	//
	//	int - count of similar experiences found.
	//	float64 - confidence factor in [0, 1].
	//	error - non-nil if the query fails.
	FindSimilar(ctx context.Context, taskType string, limit int) (int, float64, error)
}

// EvidenceProvider defines the interface for retrieving multi-dimensional
// evidence that can inform strategy scoring. This interface provides
// aggregated statistics (success rate, latency, error rate, etc.) rather
// than simple count and confidence values.
type EvidenceProvider interface {
	// GetEvidence returns multi-dimensional evidence for a specific strategy.
	//
	// Args:
	//
	//	ctx - operation context.
	//	strategyID - the identifier of the strategy to retrieve evidence for.
	//
	// Returns:
	//
	//	Evidence - multi-dimensional aggregated statistics.
	//	error - non-nil if the query fails.
	GetEvidence(ctx context.Context, strategyID string) (experience.Evidence, error)

	// GetEvidenceByTaskType returns multi-dimensional evidence aggregated
	// across all strategies for a specific task type.
	//
	// Args:
	//
	//	ctx - operation context.
	//	taskType - the type of task to retrieve evidence for.
	//
	// Returns:
	//
	//	Evidence - multi-dimensional aggregated statistics.
	//	error - non-nil if the query fails.
	GetEvidenceByTaskType(ctx context.Context, taskType string) (experience.Evidence, error)
}

// MemoryAwareScoringConfig holds configuration for memory-aware scoring.
type MemoryAwareScoringConfig struct {
	// Enabled enables memory-aware scoring adjustments.
	Enabled bool `json:"enabled"`

	// MemoryWeight controls the contribution of memory evidence bonus to
	// the final score (default 0.2).
	MemoryWeight float64 `json:"memory_weight"`

	// CostWeight controls the penalty multiplier for strategy cost
	// (default 0.1).
	CostWeight float64 `json:"cost_weight"`

	// LatencyWeight controls the penalty multiplier for strategy latency
	// in seconds (default 0.05).
	LatencyWeight float64 `json:"latency_weight"`

	// RegressionWeight controls the penalty multiplier for score regression
	// compared to a known baseline (default 0.1).
	RegressionWeight float64 `json:"regression_weight"`

	// MinEvidenceBonus is the minimum memory evidence bonus (default 0.0).
	MinEvidenceBonus float64 `json:"min_evidence_bonus"`

	// MaxEvidenceBonus is the maximum memory evidence bonus (default 20.0).
	MaxEvidenceBonus float64 `json:"max_evidence_bonus"`

	// ExperienceLookupLimit is the maximum number of similar experiences to
	// retrieve per scoring call (default 10).
	ExperienceLookupLimit int `json:"experience_lookup_limit"`

	// SuccessRateBonusScale controls the bonus multiplier for success rate
	// in evidence-based scoring (default 10.0).
	// Formula: success_rate * confidence * SuccessRateBonusScale.
	SuccessRateBonusScale float64 `json:"success_rate_bonus_scale"`

	// LatencyPenaltyScale controls the penalty multiplier for latency_p50
	// in evidence-based scoring (default 1.0).
	// Formula: (latency_p50 / 10000) * LatencyPenaltyScale.
	LatencyPenaltyScale float64 `json:"latency_penalty_scale"`

	// ErrorRatePenaltyScale controls the penalty multiplier for error rate
	// in evidence-based scoring (default 1.0).
	// Formula: error_rate * confidence * ErrorRatePenaltyScale.
	ErrorRatePenaltyScale float64 `json:"error_rate_penalty_scale"`
}

// DefaultMemoryAwareScoringConfig returns sensible defaults for memory-aware
// scoring configuration.
func DefaultMemoryAwareScoringConfig() MemoryAwareScoringConfig {
	return MemoryAwareScoringConfig{
		Enabled:               false,
		MemoryWeight:          0.2,
		CostWeight:            0.1,
		LatencyWeight:         0.05,
		RegressionWeight:      0.1,
		MinEvidenceBonus:      0.0,
		MaxEvidenceBonus:      20.0,
		ExperienceLookupLimit: 10,
		SuccessRateBonusScale: 10.0,
		LatencyPenaltyScale:   1.0,
		ErrorRatePenaltyScale: 1.0,
	}
}

// ScoreDetail provides a breakdown of the individual components that
// contributed to a strategy's final fitness score.
type ScoreDetail struct {
	// QualityScore is the base score from the underlying scorer pipeline.
	QualityScore float64 `json:"quality_score"`

	// MemoryEvidenceBonus is the bonus from matching past experiences.
	MemoryEvidenceBonus float64 `json:"memory_evidence_bonus"`

	// CostPenalty is the penalty applied for strategy execution cost.
	CostPenalty float64 `json:"cost_penalty"`

	// LatencyPenalty is the penalty applied for strategy latency.
	LatencyPenalty float64 `json:"latency_penalty"`

	// RegressionPenalty is the penalty for score regression.
	RegressionPenalty float64 `json:"regression_penalty"`

	// FinalScore is the sum: quality + memory - cost - latency - regression.
	FinalScore float64 `json:"final_score"`

	// ExperienceCount is the number of similar experiences found.
	ExperienceCount int `json:"experience_count"`

	// Confidence is the confidence factor in [0, 1].
	Confidence float64 `json:"confidence"`

	// SuccessRateEvidence is the success rate from multi-dimensional evidence.
	// Range: [0.0, 1.0]. Higher values indicate better historical performance.
	SuccessRateEvidence float64 `json:"success_rate_evidence"`

	// LatencyEvidence is the P50 latency in milliseconds from multi-dimensional evidence.
	// Lower values indicate better (faster) historical performance.
	LatencyEvidence int64 `json:"latency_evidence"`

	// ErrorRateEvidence is the error rate from multi-dimensional evidence.
	// Range: [0.0, 1.0]. Lower values indicate better historical performance.
	ErrorRateEvidence float64 `json:"error_rate_evidence"`

	// SampleCountEvidence is the number of samples aggregated in the evidence.
	// Higher values indicate more reliable statistics.
	SampleCountEvidence int64 `json:"sample_count_evidence"`
}

// MemoryAwareScorer extends a TieredScorer with experience-driven bonuses
// and cost/latency penalties. It wraps the tiered scorer pipeline and adjusts
// scores based on evidence from past experiences.
//
// When the ExperienceProvider is nil or the scorer is disabled, it behaves
// exactly like the underlying tiered scorer with no adjustments.
//
// The scorer supports two modes:
//  1. Legacy mode (ExperienceProvider only): uses simple count and confidence
//     for memory bonus calculation.
//  2. Evidence mode (EvidenceProvider available): uses multi-dimensional
//     evidence (success_rate, latency_p50, error_rate) for more nuanced
//     scoring adjustments.
type MemoryAwareScorer struct {
	tiered           *TieredScorer
	exp              ExperienceProvider
	evidenceProvider EvidenceProvider
	cfg              MemoryAwareScoringConfig

	mu           sync.Mutex
	adjustments  int64 // total number of adjustments applied
	bonusTotal   float64
	penaltyTotal float64
}

// NewMemoryAwareScorer creates a new memory-aware scorer wrapping a tiered
// scorer.
//
// Args:
//
//	ts - the tiered scorer pipeline to wrap (must not be nil).
//	exp - the experience provider (may be nil, in which case the scorer
//	  behaves like a regular tiered scorer).
//	cfg - the memory-aware scoring configuration (use
//	  DefaultMemoryAwareScoringConfig() for defaults).
//
// Returns:
//
//	*MemoryAwareScorer - the configured scorer.
//	error - non-nil if tiered scorer is nil or configuration is invalid.
func NewMemoryAwareScorer(ts *TieredScorer, exp ExperienceProvider, cfg MemoryAwareScoringConfig) (*MemoryAwareScorer, error) {
	if ts == nil {
		return nil, errors.New("tiered scorer must not be nil")
	}
	if cfg.MemoryWeight < 0 {
		return nil, fmt.Errorf("memory weight must be non-negative, got %f", cfg.MemoryWeight)
	}
	if cfg.CostWeight < 0 {
		return nil, fmt.Errorf("cost weight must be non-negative, got %f", cfg.CostWeight)
	}
	if cfg.LatencyWeight < 0 {
		return nil, fmt.Errorf("latency weight must be non-negative, got %f", cfg.LatencyWeight)
	}
	if cfg.RegressionWeight < 0 {
		return nil, fmt.Errorf("regression weight must be non-negative, got %f", cfg.RegressionWeight)
	}
	if cfg.MaxEvidenceBonus < cfg.MinEvidenceBonus {
		return nil, fmt.Errorf("max evidence bonus %f must be >= min evidence bonus %f",
			cfg.MaxEvidenceBonus, cfg.MinEvidenceBonus)
	}
	if cfg.ExperienceLookupLimit <= 0 {
		cfg.ExperienceLookupLimit = 10
	}

	return &MemoryAwareScorer{
		tiered: ts,
		exp:    exp,
		cfg:    cfg,
	}, nil
}

// SetEvidenceProvider sets the evidence provider for multi-dimensional
// scoring adjustments. This allows the scorer to use EvidenceProvider
// in addition to or instead of the legacy ExperienceProvider.
//
// Args:
//
//	ep - the evidence provider (may be nil).
func (ms *MemoryAwareScorer) SetEvidenceProvider(ep EvidenceProvider) {
	ms.evidenceProvider = ep
}

// Score evaluates a strategy through the tiered pipeline and applies
// memory-aware adjustments. The final fitness is computed as:
//
//	fitness = quality_score + memory_evidence_bonus - cost_penalty
//	          - latency_penalty - regression_penalty
//
// If the experience provider is nil or the scorer is not enabled, this
// delegates directly to the underlying tiered scorer.
//
// When EvidenceProvider is available, the scorer uses multi-dimensional
// evidence (success_rate, latency_p50, error_rate) for more nuanced
// adjustments. Otherwise, it falls back to legacy ExperienceProvider.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//
// Returns:
//
//	float64 - the adjusted fitness score.
//	*ScoreDetail - breakdown of score components (nil if not enabled or
//	  experience provider is nil).
//	error - non-nil if scoring fails.
func (ms *MemoryAwareScorer) Score(ctx context.Context, s *mutation.Strategy) (float64, *ScoreDetail, error) {
	// If not enabled or no providers, delegate directly.
	if !ms.cfg.Enabled || (ms.exp == nil && ms.evidenceProvider == nil) {
		score, _, err := ms.tiered.Score(ctx, s)
		if err != nil {
			return 0, nil, fmt.Errorf("memory-aware scorer: %w", err)
		}
		return score, nil, nil
	}

	// Get base quality score from tiered pipeline.
	qualityScore, _, err := ms.tiered.Score(ctx, s)
	if err != nil {
		return 0, nil, fmt.Errorf("memory-aware scorer: %w", err)
	}

	// Build detail with quality component.
	detail := &ScoreDetail{
		QualityScore: qualityScore,
		FinalScore:   qualityScore,
	}

	// Try EvidenceProvider first (multi-dimensional evidence mode).
	if ms.evidenceProvider != nil {
		return ms.scoreWithEvidence(ctx, s, qualityScore, detail)
	}

	// Fall back to legacy ExperienceProvider (simple count/confidence mode).
	return ms.scoreWithLegacyExperience(ctx, s, qualityScore, detail)
}

// scoreWithEvidence applies adjustments using multi-dimensional evidence.
// This method uses EvidenceProvider to get success_rate, latency_p50,
// error_rate, and sample_count for more nuanced scoring.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//	qualityScore - the base quality score from tiered pipeline.
//	detail - the score detail to populate.
//
// Returns:
//
//	float64 - the adjusted fitness score.
//	*ScoreDetail - breakdown of score components.
//	error - non-nil if scoring fails.
func (ms *MemoryAwareScorer) scoreWithEvidence(ctx context.Context, s *mutation.Strategy, qualityScore float64, detail *ScoreDetail) (float64, *ScoreDetail, error) {
	// Try to get evidence by strategy ID first, then by task type.
	var ev experience.Evidence
	var err error

	if s.ID != "" {
		ev, err = ms.evidenceProvider.GetEvidence(ctx, s.ID)
		if err != nil {
			log.Debug("memory-aware scorer: evidence lookup by strategy_id failed, trying task_type",
				"strategy_id", s.ID, "error", err)
		}
	}

	if err != nil || ev.IsEmpty() {
		taskType := taskTypeFromStrategy(s)
		ev, err = ms.evidenceProvider.GetEvidenceByTaskType(ctx, taskType)
		if err != nil {
			log.Warn("memory-aware scorer: evidence lookup failed",
				"strategy_id", s.ID, "task_type", taskType, "error", err)
			detail.FinalScore = qualityScore
			return qualityScore, detail, nil
		}
	}

	// Populate evidence fields in detail.
	detail.SuccessRateEvidence = ev.SuccessRate
	detail.LatencyEvidence = ev.LatencyP50
	detail.ErrorRateEvidence = ev.ErrorRate
	detail.SampleCountEvidence = ev.SampleCount
	detail.Confidence = ev.Confidence
	detail.ExperienceCount = int(ev.SampleCount)

	// Compute multi-dimensional adjustments.
	bonus := ms.computeEvidenceBasedBonus(ev)
	detail.MemoryEvidenceBonus = bonus

	// Compute cost penalty (from strategy params or default).
	cost := strategyCost(s)
	costPenalty := cost * ms.cfg.CostWeight
	detail.CostPenalty = costPenalty

	// Compute latency penalty (from strategy params or default).
	latency := strategyLatency(s)
	latencyPenalty := latency * ms.cfg.LatencyWeight
	detail.LatencyPenalty = latencyPenalty

	// Compute regression penalty (from strategy params or default).
	regression := strategyRegression(s)
	regressionPenalty := regression * ms.cfg.RegressionWeight
	detail.RegressionPenalty = regressionPenalty

	// Apply adjustments.
	finalScore := qualityScore + bonus - costPenalty - latencyPenalty - regressionPenalty
	detail.FinalScore = finalScore

	// Track aggregate stats.
	ms.mu.Lock()
	ms.adjustments++
	ms.bonusTotal += bonus
	ms.penaltyTotal += costPenalty + latencyPenalty + regressionPenalty
	ms.mu.Unlock()

	log.Debug("memory-aware score computed (evidence mode)",
		"strategy_id", s.ID,
		"quality", qualityScore,
		"bonus", bonus,
		"cost_penalty", costPenalty,
		"latency_penalty", latencyPenalty,
		"regression_penalty", regressionPenalty,
		"final", finalScore,
		"success_rate", ev.SuccessRate,
		"latency_p50", ev.LatencyP50,
		"error_rate", ev.ErrorRate,
		"sample_count", ev.SampleCount,
		"confidence", ev.Confidence,
	)

	return finalScore, detail, nil
}

// scoreWithLegacyExperience applies adjustments using legacy ExperienceProvider.
// This method uses simple count and confidence for memory bonus calculation.
//
// Args:
//
//	ctx - operation context.
//	s - the strategy to score.
//	qualityScore - the base quality score from tiered pipeline.
//	detail - the score detail to populate.
//
// Returns:
//
//	float64 - the adjusted fitness score.
//	*ScoreDetail - breakdown of score components.
//	error - non-nil if scoring fails.
func (ms *MemoryAwareScorer) scoreWithLegacyExperience(ctx context.Context, s *mutation.Strategy, qualityScore float64, detail *ScoreDetail) (float64, *ScoreDetail, error) {
	// Query experience provider for similar past experiences.
	expCount, confidence, err := ms.exp.FindSimilar(ctx, taskTypeFromStrategy(s), ms.cfg.ExperienceLookupLimit)
	if err != nil {
		// Experience lookup failure is non-fatal; log and continue with
		// unadjusted score.
		log.Warn("memory-aware scorer: experience lookup failed",
			"strategy_id", s.ID, "error", err)
		detail.FinalScore = qualityScore
		return qualityScore, detail, nil
	}

	// Compute memory evidence bonus.
	bonus := ms.computeMemoryBonus(expCount, confidence)
	detail.MemoryEvidenceBonus = bonus
	detail.ExperienceCount = expCount
	detail.Confidence = confidence

	// Compute cost penalty (from strategy params or default).
	cost := strategyCost(s)
	costPenalty := cost * ms.cfg.CostWeight
	detail.CostPenalty = costPenalty

	// Compute latency penalty (from strategy params or default).
	latency := strategyLatency(s)
	latencyPenalty := latency * ms.cfg.LatencyWeight
	detail.LatencyPenalty = latencyPenalty

	// Compute regression penalty (from strategy params or default).
	regression := strategyRegression(s)
	regressionPenalty := regression * ms.cfg.RegressionWeight
	detail.RegressionPenalty = regressionPenalty

	// Apply adjustments.
	finalScore := qualityScore + bonus - costPenalty - latencyPenalty - regressionPenalty
	detail.FinalScore = finalScore

	// Track aggregate stats.
	ms.mu.Lock()
	ms.adjustments++
	ms.bonusTotal += bonus
	ms.penaltyTotal += costPenalty + latencyPenalty + regressionPenalty
	ms.mu.Unlock()

	log.Debug("memory-aware score computed",
		"strategy_id", s.ID,
		"quality", qualityScore,
		"bonus", bonus,
		"cost_penalty", costPenalty,
		"latency_penalty", latencyPenalty,
		"regression_penalty", regressionPenalty,
		"final", finalScore,
		"experiences", expCount,
		"confidence", confidence,
	)

	return finalScore, detail, nil
}

// ScoreAsScorerFunc returns a genome.ScorerFunc that wraps the MemoryAwareScorer.
// This allows the memory-aware scorer to be used wherever a ScorerFunc is
// expected (e.g., in genome.Population.ScoreAgents).
//
// When the scorer is enabled and has an experience provider, the score detail
// is logged rather than returned (since ScorerFunc only returns a float64).
//
// Returns:
//
//	genome.ScorerFunc - function that scores strategies with memory awareness.
func (ms *MemoryAwareScorer) ScoreAsScorerFunc() genome.ScorerFunc {
	return func(s *mutation.Strategy) float64 {
		score, detail, err := ms.Score(context.Background(), s)
		if err != nil {
			log.Warn("memory-aware scorer failed, using baseline",
				"strategy_id", s.ID, "error", err)
			return 50.0
		}
		if detail != nil && ms.cfg.Enabled && ms.exp != nil {
			log.Debug("score detail",
				"strategy_id", s.ID,
				"quality", detail.QualityScore,
				"memory_bonus", detail.MemoryEvidenceBonus,
				"cost_penalty", detail.CostPenalty,
				"latency_penalty", detail.LatencyPenalty,
				"regression_penalty", detail.RegressionPenalty,
				"final", detail.FinalScore,
			)
		}
		return score
	}
}

// Stats returns scoring statistics since creation or last reset.
//
// Returns:
//
//	map[string]float64 - statistics including adjustments count, avg_bonus,
//	  avg_penalty, and delegate tiered scorer stats.
func (ms *MemoryAwareScorer) Stats() map[string]float64 {
	ms.mu.Lock()
	adj := ms.adjustments
	bonusTotal := ms.bonusTotal
	penaltyTotal := ms.penaltyTotal
	ms.mu.Unlock()

	stats := map[string]float64{
		"adjustments":   float64(adj),
		"bonus_total":   bonusTotal,
		"penalty_total": penaltyTotal,
	}

	if adj > 0 {
		stats["avg_bonus"] = bonusTotal / float64(adj)
		stats["avg_penalty"] = penaltyTotal / float64(adj)
	}

	return stats
}

// ResetStats resets the memory-aware scorer statistics.
func (ms *MemoryAwareScorer) ResetStats() {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.adjustments = 0
	ms.bonusTotal = 0
	ms.penaltyTotal = 0
}

// computeMemoryBonus calculates the evidence bonus from past experiences.
//
// Formula:
//
//	bonus = min(count * confidence * 5.0, MaxEvidenceBonus)
//	bonus = max(bonus, MinEvidenceBonus)
func (ms *MemoryAwareScorer) computeMemoryBonus(expCount int, confidence float64) float64 {
	bonus := float64(expCount) * confidence * 5.0
	if bonus > ms.cfg.MaxEvidenceBonus {
		bonus = ms.cfg.MaxEvidenceBonus
	}
	if bonus < ms.cfg.MinEvidenceBonus {
		bonus = ms.cfg.MinEvidenceBonus
	}
	return bonus
}

// computeEvidenceBasedBonus calculates the evidence bonus using multi-dimensional
// evidence. This method considers success_rate, latency_p50, and error_rate
// to provide more nuanced scoring adjustments.
//
// Formula:
//
//	success_bonus = success_rate * confidence * 10.0
//	latency_penalty_factor = max(0, min(1.0, latency_p50 / 10000.0))
//	error_penalty_factor = error_rate * confidence
//	bonus = success_bonus * (1.0 - latency_penalty_factor) * (1.0 - error_penalty_factor)
//	bonus = min(bonus, MaxEvidenceBonus)
//	bonus = max(bonus, MinEvidenceBonus)
//
// The formula rewards high success rates and penalizes high latency and error rates.
// The confidence factor scales the adjustments based on sample reliability.
func (ms *MemoryAwareScorer) computeEvidenceBasedBonus(ev experience.Evidence) float64 {
	// If no samples, return minimum bonus.
	if !ev.HasSamples() {
		return ms.cfg.MinEvidenceBonus
	}

	// Success rate bonus: higher success rate → higher bonus.
	// Max contribution: 1.0 * 1.0 * 10.0 = 10.0.
	successBonus := ev.SuccessRate * ev.Confidence * 10.0

	// Latency penalty factor: normalize latency to [0, 1] range.
	// Lower latency (better) → lower penalty factor → higher bonus.
	// Reference: 10000ms (10 seconds) as max acceptable latency.
	maxLatencyMs := 10000.0
	latencyPenaltyFactor := float64(ev.LatencyP50) / maxLatencyMs
	if latencyPenaltyFactor > 1.0 {
		latencyPenaltyFactor = 1.0
	}
	if latencyPenaltyFactor < 0 {
		latencyPenaltyFactor = 0
	}

	// Error rate penalty factor: higher error rate → lower bonus.
	errorPenaltyFactor := ev.ErrorRate * ev.Confidence
	if errorPenaltyFactor > 1.0 {
		errorPenaltyFactor = 1.0
	}

	// Combine factors: bonus is scaled by (1 - latency_penalty) and (1 - error_penalty).
	bonus := successBonus * (1.0 - latencyPenaltyFactor) * (1.0 - errorPenaltyFactor)

	// Clamp to configured bounds.
	if bonus > ms.cfg.MaxEvidenceBonus {
		bonus = ms.cfg.MaxEvidenceBonus
	}
	if bonus < ms.cfg.MinEvidenceBonus {
		bonus = ms.cfg.MinEvidenceBonus
	}

	return bonus
}

// taskTypeFromStrategy extracts a task type identifier from the strategy's
// metadata or params. Returns "default" if no task type information is found.
func taskTypeFromStrategy(s *mutation.Strategy) string {
	if s == nil {
		return DefaultTaskType
	}
	if s.Name != "" {
		return s.Name
	}
	if s.Params != nil {
		if t, ok := s.Params["task_type"].(string); ok && t != "" {
			return t
		}
	}
	return DefaultTaskType
}

// strategyCost extracts the cost value from a strategy's params.
// Returns 0 if no cost data is available.
func strategyCost(s *mutation.Strategy) float64 {
	if s == nil || s.Params == nil {
		return 0
	}
	if c, ok := s.Params["cost"].(float64); ok && c > 0 {
		return c
	}
	return 0
}

// strategyLatency extracts the latency value (in seconds) from a strategy's
// params. Returns 0 if no latency data is available.
func strategyLatency(s *mutation.Strategy) float64 {
	if s == nil || s.Params == nil {
		return 0
	}
	if l, ok := s.Params["latency"].(float64); ok && l > 0 {
		return l
	}
	return 0
}

// strategyRegression extracts the regression value from a strategy's params.
// Returns 0 if no regression data is available.
func strategyRegression(s *mutation.Strategy) float64 {
	if s == nil || s.Params == nil {
		return 0
	}
	if r, ok := s.Params["regression"].(float64); ok && r > 0 {
		return r
	}
	return 0
}
