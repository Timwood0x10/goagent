// genome_wiring_genealogy.go contains the GenomeMutatorAdapter (wraps a
// genome mutator for population use), the ScoreRollingWindow helper, the
// PopulationGenealogyRecorder (records strategy lineage from genome evolution),
// and the RecordPopulationLineage extraction function.
package evolution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// GenomeMutatorAdapter wraps a genome.MutatorInterface-compatible mutator
// to implement genome.MutatorInterface. This enables genome.Population to
// use both the production mutator and the experience-guided mutator.
type GenomeMutatorAdapter struct {
	mutator genome.MutatorInterface
}

// NewGenomeMutatorAdapter creates a genome-compatible mutator adapter.
// The provided mutator must implement the genome.MutatorInterface (both
// *mutation.Mutator and *mutation.ExperienceGuidedMutator satisfy this).
//
// Args:
//
//	m - the mutator to wrap (must not be nil).
//
// Returns:
//
//	*GenomeMutatorAdapter - the adapter instance.
//	error - non-nil if mutator is nil.
func NewGenomeMutatorAdapter(m genome.MutatorInterface) (*GenomeMutatorAdapter, error) {
	if m == nil {
		return nil, errors.New("mutator must not be nil")
	}
	return &GenomeMutatorAdapter{mutator: m}, nil
}

// Mutate delegates to the wrapped mutator.
// The signature matches genome.MutatorInterface (uses *mutation.Strategy).
//
// Args:
//
//	ctx - operation context for cancellation.
//	parent - the parent strategy to mutate.
//	n - number of children to generate.
//
// Returns:
//
//	[]*mutation.Strategy - the generated child strategies.
//	error - delegation error from the wrapped mutator.
func (a *GenomeMutatorAdapter) Mutate(
	ctx context.Context,
	parent *mutation.Strategy,
	n int,
) ([]*mutation.Strategy, error) {
	children, err := a.mutator.Mutate(ctx, parent, n)
	if err != nil {
		return nil, fmt.Errorf("genome mutator adapter: %w", err)
	}
	return children, nil
}

// ScoreRollingWindow maintains a sliding window of recent scores for an agent.
// It provides a rolling mean that smooths out noise in fitness evaluations.
type ScoreRollingWindow struct {
	scores  []float64
	maxSize int
}

// newScoreRollingWindow creates a rolling window with the given capacity.
func newScoreRollingWindow(maxSize int) *ScoreRollingWindow {
	return &ScoreRollingWindow{
		scores:  make([]float64, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add appends a score and evicts the oldest if at capacity.
func (w *ScoreRollingWindow) Add(score float64) {
	if w == nil {
		return
	}
	w.scores = append(w.scores, score)
	if len(w.scores) > w.maxSize {
		w.scores = w.scores[1:]
	}
}

// Mean returns the rolling average of all scores in the window.
// Returns 0 if the window is empty.
func (w *ScoreRollingWindow) Mean() float64 {
	if w == nil || len(w.scores) == 0 {
		return 0
	}
	var sum float64
	for _, s := range w.scores {
		sum += s
	}
	return sum / float64(len(w.scores))
}

// PopulationGenealogyRecorder records strategy lineage from genome evolution
// into the evolution package's genealogy system. It implements GenealogyRecorder
// by extracting lineage data from population state after each evolution cycle.
type PopulationGenealogyRecorder struct {
	mu          sync.RWMutex
	lineages    []StrategyLineage
	maxLineages int // Maximum number of lineage records; 0 = unlimited (default 10000).

	// scoreHistory tracks per-agent rolling windows for noise-robust
	// improvement computation. Keyed by agent ID.
	scoreHistory map[string]*ScoreRollingWindow
}

// NewPopulationGenealogyRecorder creates a new genealogy recorder.
//
// Returns:
//
//	*PopulationGenealogyRecorder - the recorder instance.
func NewPopulationGenealogyRecorder() *PopulationGenealogyRecorder {
	return &PopulationGenealogyRecorder{
		lineages:     make([]StrategyLineage, 0),
		maxLineages:  10000,
		scoreHistory: make(map[string]*ScoreRollingWindow),
	}
}

// RecordScore adds an agent's score to its rolling window for noise-robust
// improvement computation. The window retains the most recent scores.
func (r *PopulationGenealogyRecorder) RecordScore(agentID string, score float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	win, ok := r.scoreHistory[agentID]
	if !ok {
		win = newScoreRollingWindow(3) // window of 3 matches ImprovementWindow in promotion
		r.scoreHistory[agentID] = win
	}
	win.Add(score)
}

// RollingMeanScore returns the rolling mean of the last N scores for an agent.
// Returns 0 if no history exists for the given agent ID.
func (r *PopulationGenealogyRecorder) RollingMeanScore(agentID string) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	win, ok := r.scoreHistory[agentID]
	if !ok {
		return 0
	}
	return win.Mean()
}

// Record persists a strategy lineage entry from genome evolution results.
// It extracts parent-child relationships from evolved population agents.
//
// Args:
//
//	ctx - operation context.
//	lineage - the lineage record to persist.
//
// Returns:
//
//	error - always nil for in-memory implementation.
func (r *PopulationGenealogyRecorder) Record(ctx context.Context, lineage StrategyLineage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lineages = append(r.lineages, lineage)

	// Trim oldest records if exceeding max capacity.
	if r.maxLineages > 0 && len(r.lineages) > r.maxLineages {
		trimCount := len(r.lineages) - r.maxLineages
		r.lineages = r.lineages[trimCount:]
	}

	log.DebugContext(ctx, "lineage recorded", "method", "Record", "parent_id", lineage.ParentID,
		"child_id", lineage.ChildID,
		"mutation_type", lineage.MutationType,
	)

	return nil
}

// Lineages returns all recorded lineage entries (thread-safe).
//
// Returns:
//
//	[]StrategyLineage - copy of recorded lineages.
func (r *PopulationGenealogyRecorder) Lineages() []StrategyLineage {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]StrategyLineage, len(r.lineages))
	copy(result, r.lineages)
	return result
}

// Count returns the number of recorded lineage entries.
//
// Returns:
//
//	int - number of lineages.
func (r *PopulationGenealogyRecorder) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.lineages)
}

// resolveParentScore looks up a parent's score from the pre-evolution snapshot,
// handling crossover parents whose ParentID is encoded as "parentA\u00d7parentB".
// For crossover parents, returns the average of the two parent scores.
func resolveParentScore(parentID string, parentScores map[string]float64) (float64, bool) {
	if score, ok := parentScores[parentID]; ok {
		return score, true
	}
	// Handle crossover: ParentID may contain "\u00d7" separator.
	parts := strings.Split(parentID, "\u00d7")
	if len(parts) != 2 {
		return 0, false
	}
	ps1, ok1 := parentScores[parts[0]]
	if !ok1 {
		return 0, false
	}
	ps2, ok2 := parentScores[parts[1]]
	if !ok2 {
		return 0, false
	}
	return (ps1 + ps2) / 2, true
}

// RecordPopulationLineage extracts parent-child relationships from a genome
// population after evolution and records them into the genealogy system.
// This bridges genome.Population's ParentID tracking with evolution.GenealogyRecorder.
//
// Args:
//
//	ctx - operation context.
//	pop - the post-evolution population to extract lineage from.
//	parentSnapshot - pre-evolution snapshot for parent score lookup (may be nil).
//	prevGeneration - the generation number before evolution (for filtering).
//
// Returns:
//
//	int - number of new lineage records created.
//	error - non-nil if recording fails.
func RecordPopulationLineage(
	ctx context.Context,
	pop *genome.Population,
	recorder GenealogyRecorder,
	parentSnapshot []*mutation.Strategy,
	prevGeneration int,
) (int, error) {
	if pop == nil || recorder == nil {
		return 0, nil
	}

	// Snapshot provides a thread-safe locked read of all agents and generation.
	agents, generation := pop.Snapshot()

	// Build parent score lookup from pre-evolution snapshot.
	parentScores := make(map[string]float64, len(parentSnapshot))
	for _, p := range parentSnapshot {
		parentScores[p.ID] = p.Score
	}

	// Type-assert recorder to update rolling score history when possible.
	historyRecorder, useRolling := recorder.(*PopulationGenealogyRecorder)
	if useRolling {
		for _, p := range parentSnapshot {
			historyRecorder.RecordScore(p.ID, p.Score)
		}
	}

	count := 0
	seen := make(map[string]bool, len(agents))
	for _, agent := range agents {
		if agent.ParentID == "" {
			continue
		}
		if agent.Version <= 1 {
			continue
		}

		key := agent.ParentID + "->" + agent.ID
		if seen[key] {
			continue
		}
		seen[key] = true

		// Compute score improvement using rolling mean when available,
		// falling back to single-point parent score for backward compatibility.
		// A rolling mean smooths out noise variance and prevents transient
		// fitness fluctuations from inflating the improvement rate.
		parentScore, ok := resolveParentScore(agent.ParentID, parentScores)

		// Use rolling mean as the baseline if available.
		baselineScore := parentScore
		if useRolling {
			if rolling := historyRecorder.RollingMeanScore(agent.ParentID); rolling > 0 {
				baselineScore = rolling
			}
		}

		scoreDelta := 0.0
		winRate := 0.0
		if ok {
			scoreDelta = agent.Score - baselineScore
			if scoreDelta > 0 {
				winRate = 1.0
			}
		}

		lineage := StrategyLineage{
			ParentID:         agent.ParentID,
			ChildID:          agent.ID,
			MutationType:     agent.StrategyMutationType.String(),
			WinRate:          winRate,
			ScoreImprovement: scoreDelta,
			ParentScore:      parentScore,
			ChildScore:       agent.Score,
			Timestamp:        agent.CreatedAt.Unix(),
		}

		if err := recorder.Record(ctx, lineage); err != nil {
			return count, fmt.Errorf("genealogy.RecordPopulationLineage: record lineage for agent %s: %w", agent.ID, err)
		}
		count++
	}

	if count > 0 {
		log.InfoContext(ctx, "recorded", "method", "RecordPopulationLineage", "new_records", count,
			"generation", generation,
		)
	}

	return count, nil
}
