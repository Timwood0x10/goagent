package scoring

import (
	"context"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

// ExperienceStoreProvider implements ExperienceProvider by bridging to an
// ExperienceStore. It queries historical execution data and computes
// confidence scores from actual strategy performance.
type ExperienceStoreProvider struct {
	store experience.ExperienceStore
}

// NewExperienceStoreProvider creates an ExperienceStoreProvider.
//
// Args:
//
//	store - the experience store to query (must not be nil).
//
// Returns:
//
//	*ExperienceStoreProvider - the initialized provider.
func NewExperienceStoreProvider(store experience.ExperienceStore) *ExperienceStoreProvider {
	return &ExperienceStoreProvider{store: store}
}

// FindSimilar queries the experience store for strategies that ran the same
// task type and returns the match count plus an average-score-based confidence.
//
// Confidence is the mean NormalizedExperience.Score across matching records
// (range [0, 1] — higher means the store has more consistently good results
// for this task type).
func (p *ExperienceStoreProvider) FindSimilar(ctx context.Context, taskType string, limit int) (int, float64, error) {
	exps, err := p.store.QueryByTaskType(ctx, taskType, limit)
	if err != nil {
		return 0, 0, err
	}
	if len(exps) == 0 {
		log.Debug("ExperienceStoreProvider: no experiences found for task type",
			"task_type", taskType)
		return 0, 0, nil
	}

	var totalScore float64
	for _, e := range exps {
		totalScore += e.Score
	}
	confidence := totalScore / float64(len(exps))
	if confidence > 1.0 {
		confidence = 1.0
	}

	log.Debug("ExperienceStoreProvider: found similar experiences",
		"task_type", taskType,
		"count", len(exps),
		"confidence", confidence,
	)

	return len(exps), confidence, nil
}

// EvidenceAggregatorProvider implements EvidenceProvider by bridging to an
// EvidenceAggregator. It returns multi-dimensional aggregated evidence
// (success_rate, latency_p50, error_rate, etc.) for more nuanced scoring.
type EvidenceAggregatorProvider struct {
	aggregator experience.EvidenceAggregator
}

// NewEvidenceAggregatorProvider creates an EvidenceAggregatorProvider.
//
// Args:
//
//	aggregator - the evidence aggregator to query (must not be nil).
//
// Returns:
//
//	*EvidenceAggregatorProvider - the initialized provider.
func NewEvidenceAggregatorProvider(aggregator experience.EvidenceAggregator) *EvidenceAggregatorProvider {
	return &EvidenceAggregatorProvider{aggregator: aggregator}
}

// GetEvidence returns multi-dimensional evidence for a specific strategy.
func (p *EvidenceAggregatorProvider) GetEvidence(ctx context.Context, strategyID string) (experience.Evidence, error) {
	return p.aggregator.Aggregate(ctx, strategyID)
}

// GetEvidenceByTaskType returns multi-dimensional evidence aggregated across
// all strategies for a specific task type.
func (p *EvidenceAggregatorProvider) GetEvidenceByTaskType(ctx context.Context, taskType string) (experience.Evidence, error) {
	return p.aggregator.AggregateByTaskType(ctx, taskType)
}
