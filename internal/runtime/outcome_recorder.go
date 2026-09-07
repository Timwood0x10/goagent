package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

// OutcomeExperienceRecorder implements OutcomeRecorder by converting execution
// outcomes into normalized experiences and persisting them in an ExperienceStore.
// This bridges the runtime layer (ExecutionOutcome) to the evolution layer
// (NormalizedExperience) so the GA has access to real execution data.
type OutcomeExperienceRecorder struct {
	normalizer experience.Normalizer
	store      experience.ExperienceStore
}

// NewOutcomeExperienceRecorder creates an OutcomeExperienceRecorder.
// Returns an error if normalizer or store is nil.
func NewOutcomeExperienceRecorder(normalizer experience.Normalizer, store experience.ExperienceStore) (*OutcomeExperienceRecorder, error) {
	if normalizer == nil {
		return nil, errors.New("normalizer must not be nil")
	}
	if store == nil {
		return nil, errors.New("store must not be nil")
	}
	return &OutcomeExperienceRecorder{
		normalizer: normalizer,
		store:      store,
	}, nil
}

// RecordOutcome implements OutcomeRecorder. It converts the ExecutionOutcome
// to a RawExperience, normalizes it, and appends it to the ExperienceStore.
// Filtered (noise/outlier) experiences are silently dropped with a debug log.
func (r *OutcomeExperienceRecorder) RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error {
	if r == nil {
		return errors.New("outcome recorder is nil")
	}

	raw := r.outcomeToRaw(outcome)

	normalized, err := r.normalizer.Normalize(ctx, raw)
	if err != nil {
		return fmt.Errorf("normalize outcome: %w", err)
	}

	if normalized.IsFiltered {
		slog.DebugContext(ctx, "outcome filtered by normalizer",
			"execution_id", outcome.ExecutionID,
			"reason", normalized.FilterReason,
		)
		return nil
	}

	if err := r.store.Append(ctx, normalized); err != nil {
		return fmt.Errorf("store outcome: %w", err)
	}

	slog.DebugContext(ctx, "outcome recorded as experience",
		"execution_id", outcome.ExecutionID,
		"strategy_id", normalized.StrategyID,
	)
	return nil
}

// outcomeToRaw maps an ExecutionOutcome to a RawExperience.
// Fields that are zero-valued in the outcome are left as nil so the
// normalizer applies configured defaults.
func (r *OutcomeExperienceRecorder) outcomeToRaw(outcome ExecutionOutcome) experience.RawExperience {
	raw := experience.RawExperience{
		StrategyID: outcome.ExecutionID,
		TaskType:   outcome.WorkflowID,
		Timestamp:  time.Now(),
		Metadata: map[string]interface{}{
			"execution_id":     outcome.ExecutionID,
			"workflow_id":      outcome.WorkflowID,
			"route_count":      outcome.RouteCount,
			"tool_count":       outcome.ToolCount,
			"memory_hit_count": outcome.MemoryHitCount,
			"interrupt_count":  outcome.InterruptCount,
		},
	}

	if outcome.Status != "" {
		raw.Success = outcome.Status == "success" || outcome.Status == "completed"
	}

	if outcome.Duration > 0 {
		raw.Latency = outcome.Duration
		raw.WallTime = float64(outcome.Duration) / 1000.0
	}

	if outcome.TotalSteps > 0 {
		raw.ErrorRate = float64(outcome.FailedSteps) / float64(outcome.TotalSteps)
	}

	raw.Score = computeOutcomeScore(outcome)

	return raw
}

// computeOutcomeScore derives a [0,1] score from the execution outcome. The
// result is clamped to [0,1] so failed steps / errors can never push the score
// negative (violating the documented contract).
func computeOutcomeScore(outcome ExecutionOutcome) float64 {
	if outcome.Status == "" {
		return 0.0
	}
	var score float64
	if outcome.Status == "success" || outcome.Status == "completed" {
		if outcome.TotalSteps == 0 {
			score = 1.0
		} else {
			score = 1.0 - float64(outcome.FailedSteps+outcome.ErrorCount)/float64(outcome.TotalSteps)
		}
	} else {
		score = 1.0 - float64(outcome.FailedSteps+outcome.ErrorCount+outcome.InterruptCount)/float64(outcome.TotalSteps+1)
	}
	if score < 0 {
		return 0.0
	}
	if score > 1 {
		return 1.0
	}
	return score
}
