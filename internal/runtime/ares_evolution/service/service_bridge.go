// Package evolution — bridge types and conversion helpers.
package evolution

import (
	"context"

	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

type apiGuidanceBridge struct {
	provider GuidanceProvider
}

func (b *apiGuidanceBridge) HintsForTask(ctx context.Context, taskType string, limit int) ([]evolution.EvolutionHint, error) {
	hints, err := b.provider.HintsForTask(ctx, taskType, limit)
	if err != nil || hints == nil {
		return nil, err
	}
	out := make([]evolution.EvolutionHint, len(hints))
	for i, h := range hints {
		out[i] = evolution.EvolutionHint{
			TaskType: h.TaskType, Problem: h.Problem, Solution: h.Solution,
			Constraints: h.Constraints,
		}
	}
	return out, nil
}

// RecordStrategyOutcome forwards the strategy outcome to the API-layer
// GuidanceProvider. Previously this was a no-op, silently discarding every
// strategy outcome and breaking the experience learning feedback loop
// (outcome → future mutation bias). The provider is responsible for
// persisting the outcome; if no provider is wired the call is a safe no-op
// (REVIEW #52).
func (b *apiGuidanceBridge) RecordStrategyOutcome(ctx context.Context, outcome evolution.StrategyOutcome) error {
	if b == nil || b.provider == nil {
		return nil
	}
	apiOutcome := StrategyOutcome{
		StrategyID:    outcome.StrategyID,
		TaskType:      outcome.TaskType,
		Success:       outcome.Success,
		Score:         outcome.Score,
		Cost:          outcome.Cost,
		LatencyMs:     outcome.LatencyMs,
		MutationType:  outcome.MutationType,
		ExperienceIDs: outcome.ExperienceIDs,
		Timestamp:     outcome.Timestamp,
	}
	return b.provider.RecordStrategyOutcome(ctx, apiOutcome)
}

type apiMemoryBridge struct {
	provider MemoryExperienceProvider
}

func (b *apiMemoryBridge) FindSimilar(ctx context.Context, taskType string, limit int) (int, float64, error) {
	if b.provider != nil {
		return b.provider.FindSimilar(ctx, taskType, limit)
	}
	return 0, 0, nil
}

type llmClientAdapter struct {
	inner interface {
		Generate(ctx context.Context, prompt string) (string, error)
	}
}

func (a *llmClientAdapter) Generate(ctx context.Context, prompt string) (string, error) {
	return a.inner.Generate(ctx, prompt)
}

func toAPIStrategy(s *mutation.Strategy) *Strategy {
	if s == nil {
		return nil
	}
	return &Strategy{
		ID: s.ID, Name: s.Name, Version: s.Version, Score: s.Score,
		ParentID: s.ParentID, PromptTemplate: s.PromptTemplate,
		MutationType:    s.StrategyMutationType.String(),
		DimensionScores: cloneDimensionScores(s.DimensionScores),
		Params:          cloneParams(s.Params),
		CreatedAt:       s.CreatedAt,
	}
}

// ToAPIStrategy is the exported wrapper for toAPIStrategy. It converts a
// mutation.Strategy to the service-layer Strategy type used by LLMScorer and
// DeterministicScore. Returns nil when the input is nil.
func ToAPIStrategy(s *mutation.Strategy) *Strategy {
	return toAPIStrategy(s)
}

func toInternalStrategy(s *Strategy) *mutation.Strategy {
	if s == nil {
		return nil
	}
	return &mutation.Strategy{
		ID: s.ID, Name: s.Name, Version: s.Version, Score: s.Score,
		ParentID: s.ParentID, PromptTemplate: s.PromptTemplate,
		StrategyMutationType: mutation.ParseMutationType(s.MutationType),
		DimensionScores:      cloneDimensionScores(s.DimensionScores),
		Params:               cloneParams(s.Params),
		CreatedAt:            s.CreatedAt,
	}
}

func cloneParams(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneDimensionScores(src map[string]float64) map[string]float64 {
	if src == nil {
		return nil
	}
	dst := make(map[string]float64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// toAPILineage converts an internal evolution.StrategyLineage into the
// service-layer StrategyLineage type. The internal type uses ScoreImprovement
// while the API type uses ScoreDelta; both represent the same concept (child
// score minus parent score). Previously this was a stub returning an empty
// struct, causing the Lineages() API to return N zero-value records (REVIEW #51).
func toAPILineage(l interface{}) StrategyLineage {
	if l == nil {
		return StrategyLineage{}
	}
	internal, ok := l.(evolution.StrategyLineage)
	if !ok {
		return StrategyLineage{}
	}
	return StrategyLineage{
		ParentID:               internal.ParentID,
		ChildID:                internal.ChildID,
		MutationType:           internal.MutationType,
		WinRate:                internal.WinRate,
		ScoreDelta:             internal.ScoreImprovement,
		ParentScore:            internal.ParentScore,
		ChildScore:             internal.ChildScore,
		ImprovementSignificant: internal.ImprovementSignificant,
		Timestamp:              internal.Timestamp,
	}
}
