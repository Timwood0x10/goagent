package genome

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// EvolutionKnowledge captures a reusable insight from evolution history.
// It represents a pattern that was observed, what was done about it,
// and what the outcome was — enabling the system to learn from past
// evolution cycles.
type EvolutionKnowledge struct {
	// ID uniquely identifies this knowledge entry.
	ID string `json:"id"`

	// Pattern describes the observed condition (e.g. "tool_timeout", "low_diversity").
	Pattern string `json:"pattern"`

	// Mutation describes what mutation was applied (e.g. "increase_timeout", "inject_mutants").
	Mutation string `json:"mutation"`

	// Outcome describes the result (e.g. "success_rate+12%", "diversity+0.3").
	Outcome string `json:"outcome"`

	// ScoreDelta is the actual fitness improvement observed.
	ScoreDelta float64 `json:"score_delta"`

	// Confidence is how reliable this knowledge is [0, 1].
	Confidence float64 `json:"confidence"`

	// ObservationCount is how many times this pattern has been observed.
	ObservationCount int `json:"observation_count"`

	// SuccessCount is how many times the mutation led to improvement.
	SuccessCount int `json:"success_count"`

	// CreatedAt is when this knowledge was first recorded.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when this knowledge was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// KnowledgeBase stores and retrieves evolution knowledge.
// Thread-safe for concurrent use during evolution cycles.
type KnowledgeBase struct {
	mu      sync.RWMutex
	entries map[string]*EvolutionKnowledge
}

// NewKnowledgeBase creates an empty knowledge base.
func NewKnowledgeBase() *KnowledgeBase {
	return &KnowledgeBase{
		entries: make(map[string]*EvolutionKnowledge),
	}
}

// knowledgeConfidence computes confidence using add-one smoothing
// (Laplace smoothing) to avoid 0.0 or 1.0 extremes on sparse data:
//
//	confidence = (successCount + 1) / (observationCount + 2)
//
// This gives conservative starting values (positive: 2/3≈0.67, negative: 1/3≈0.33)
// that converge toward the true rate as more observations accumulate.
func knowledgeConfidence(successCount, observationCount int) float64 {
	if observationCount <= 0 {
		return 0.5
	}
	return float64(successCount+1) / float64(observationCount+2)
}

// Record stores or updates a knowledge entry by pattern+mutation key.
// If an entry with the same pattern+mutation already exists, it updates
// the confidence, counts, and timestamp.
func (kb *KnowledgeBase) Record(pattern, mutation, outcome string, scoreDelta float64) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	key := pattern + "::" + mutation
	now := time.Now()

	existing, ok := kb.entries[key]
	if ok {
		existing.ObservationCount++
		if scoreDelta > 0 {
			existing.SuccessCount++
		}
		existing.ScoreDelta = (existing.ScoreDelta*float64(existing.ObservationCount-1) + scoreDelta) / float64(existing.ObservationCount)
		existing.Confidence = knowledgeConfidence(existing.SuccessCount, existing.ObservationCount)
		existing.Outcome = outcome
		existing.UpdatedAt = now
		return
	}

	successCount := 0
	if scoreDelta > 0 {
		successCount = 1
	}

	kb.entries[key] = &EvolutionKnowledge{
		ID:               key,
		Pattern:          pattern,
		Mutation:         mutation,
		Outcome:          outcome,
		ScoreDelta:       scoreDelta,
		Confidence:       knowledgeConfidence(successCount, 1),
		ObservationCount: 1,
		SuccessCount:     successCount,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// Lookup returns knowledge entries matching the given pattern, ordered by
// confidence descending. Returns nil if no matches.
func (kb *KnowledgeBase) Lookup(pattern string) []*EvolutionKnowledge {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	var results []*EvolutionKnowledge
	for _, e := range kb.entries {
		if e.Pattern == pattern {
			results = append(results, e)
		}
	}
	if len(results) == 0 {
		return nil
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Confidence > results[j].Confidence
	})
	return results
}

// All returns all stored knowledge entries, ordered by confidence descending.
func (kb *KnowledgeBase) All() []*EvolutionKnowledge {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	results := make([]*EvolutionKnowledge, 0, len(kb.entries))
	for _, e := range kb.entries {
		results = append(results, e)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Confidence > results[j].Confidence
	})
	return results
}

// Count returns the number of stored knowledge entries.
func (kb *KnowledgeBase) Count() int {
	kb.mu.RLock()
	defer kb.mu.RUnlock()
	return len(kb.entries)
}

// KnowledgeDistiller converts evolution history into structured knowledge.
// It identifies recurring patterns and their outcomes, building a reusable
// knowledge base that guides future evolution decisions.
type KnowledgeDistiller struct {
	base *KnowledgeBase
}

// NewKnowledgeDistiller creates a distiller backed by the given KnowledgeBase.
func NewKnowledgeDistiller(base *KnowledgeBase) *KnowledgeDistiller {
	return &KnowledgeDistiller{base: base}
}

// DistillFromHistory analyzes generation history and extracts knowledge.
// Currently identifies:
// - Stagnation patterns (no improvement for many generations)
// - Diversity recovery patterns (injecting mutants helped)
// - Score improvement patterns (specific mutation types led to gains)
func (kd *KnowledgeDistiller) DistillFromHistory(history []GenerationHistoryEntry) {
	for i := 1; i < len(history); i++ {
		prev := history[i-1]
		curr := history[i]

		// Stagnation detection.
		if curr.BestScore <= prev.BestScore && curr.BestScore >= prev.BestScore*0.99 {
			kd.base.Record(
				"stagnation",
				"adaptive_mutation_boost",
				"no_improvement",
				0,
			)
		}

		// Diversity recovery.
		if curr.Diversity > prev.Diversity*1.2 && prev.Diversity < 0.2 {
			kd.base.Record(
				"low_diversity",
				"inject_fresh_mutants",
				fmt.Sprintf("diversity_recovery_%.0f%%", (curr.Diversity/prev.Diversity-1)*100),
				curr.BestScore-prev.BestScore,
			)
		}

		// Score improvement.
		delta := curr.BestScore - prev.BestScore
		if delta > 0.05 {
			kd.base.Record(
				"score_improvement",
				"evolution_cycle",
				fmt.Sprintf("+%.2f", delta),
				delta,
			)
		}
	}
}

// KnowledgeAdapter bridges knowledge base lookups into mutation guidance.
// It provides hints based on known patterns, similar to how LLMHintProvider
// generates hints from LLM analysis but using accumulated evidence instead.
type KnowledgeAdapter struct {
	base          *KnowledgeBase
	minConfidence float64
}

// NewKnowledgeAdapter creates a knowledge-guided hint adapter.
func NewKnowledgeAdapter(base *KnowledgeBase, minConfidence float64) *KnowledgeAdapter {
	if minConfidence <= 0 {
		minConfidence = 0.4
	}
	return &KnowledgeAdapter{base: base, minConfidence: minConfidence}
}

// SuggestMutation returns the best-known mutation for a given pattern.
// Returns empty string if no knowledge exceeds the confidence threshold.
func (ka *KnowledgeAdapter) SuggestMutation(pattern string) (string, float64) {
	entries := ka.base.Lookup(pattern)
	if len(entries) == 0 {
		return "", 0
	}
	for _, e := range entries {
		if e.Confidence >= ka.minConfidence {
			return e.Mutation, e.Confidence
		}
	}
	return "", 0
}
