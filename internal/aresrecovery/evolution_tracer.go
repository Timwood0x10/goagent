package aresrecovery

import (
	"sync"
)

// Evolution trajectory recording: captures a per-generation
// snapshot of the evolution system — generation number, best score, top
// strategies and the changes that produced them — so the Dashboard can render
// the optimization path (best-strategy trajectory / breakthrough changes /
// regressions). The tracer is a pure recording surface: the evolution system
// calls Record after each generation; the dashboard reads Snapshot.

// GenerationChange is one mutation/change applied in a generation.
type GenerationChange struct {
	// StrategyID is the changed strategy.
	StrategyID string `json:"strategy_id"`
	// Description is the mutation description (e.g. "param temperature 0.7→0.4").
	Description string `json:"description"`
	// Impact is the estimated score impact of this change (attribution; may
	// be filled by attribution).
	Impact float64 `json:"impact"`
}

// GenerationSnapshot is an immutable per-generation trace record.
type GenerationSnapshot struct {
	// Generation is the 1-based generation number.
	Generation int `json:"generation"`
	// BestScore is the best strategy score of this generation.
	BestScore float64 `json:"best_score"`
	// TopStrategies are the top strategy ids of this generation (best first).
	TopStrategies []string `json:"top_strategies"`
	// Changes are the changes applied to produce this generation.
	Changes []GenerationChange `json:"changes"`
	// Breakthrough marks a score jump over the previous generation (> 20%).
	Breakthrough bool `json:"breakthrough"`
	// Regression marks a score drop over the previous generation.
	Regression bool `json:"regression"`
}

// EvolutionTracer records generation snapshots and serves them to the
// Dashboard. Thread-safe; unbounded history is capped by
// WithMaxGenerations (default keeps all).
type EvolutionTracer struct {
	mu        sync.Mutex
	snapshots []GenerationSnapshot
	maxGen    int // 0 = unlimited
}

// NewEvolutionTracer creates an empty tracer.
func NewEvolutionTracer() *EvolutionTracer {
	return &EvolutionTracer{}
}

// WithMaxGenerations caps the retained history to the most recent n
// snapshots (0 = unlimited). Returns the tracer for chaining.
func (t *EvolutionTracer) WithMaxGenerations(n int) *EvolutionTracer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxGen = n
	if n > 0 && len(t.snapshots) > n {
		t.snapshots = append([]GenerationSnapshot(nil), t.snapshots[len(t.snapshots)-n:]...)
	}
	return t
}

// Record appends one generation snapshot and marks breakthrough/regression
// relative to the previous best score.
//
// Args:
//   - generation: the 1-based generation number.
//   - bestScore: this generation's best score.
//   - topStrategies: top strategy ids (best first; may be empty).
//   - changes: the changes that produced this generation (may be nil).
func (t *EvolutionTracer) Record(generation int, bestScore float64, topStrategies []string, changes []GenerationChange) {
	snap := GenerationSnapshot{
		Generation:    generation,
		BestScore:     bestScore,
		TopStrategies: append([]string(nil), topStrategies...),
		Changes:       append([]GenerationChange(nil), changes...),
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev := len(t.snapshots); prev > 0 {
		last := t.snapshots[prev-1].BestScore
		if last > 0 {
			delta := (bestScore - last) / last
			snap.Breakthrough = delta > 0.20
			snap.Regression = delta < 0
		}
	}
	t.snapshots = append(t.snapshots, snap)
	if t.maxGen > 0 && len(t.snapshots) > t.maxGen {
		t.snapshots = append([]GenerationSnapshot(nil), t.snapshots[len(t.snapshots)-t.maxGen:]...)
	}
}

// Snapshot returns a deep copy of the recorded generations (oldest first) so
// callers can never mutate the tracer's internal state through the result.
func (t *EvolutionTracer) Snapshot() []GenerationSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]GenerationSnapshot, len(t.snapshots))
	for i, s := range t.snapshots {
		out[i] = cloneSnapshot(s)
	}
	return out
}

// Latest returns a deep copy of the most recent snapshot, or nil when nothing
// is recorded.
func (t *EvolutionTracer) Latest() *GenerationSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.snapshots) == 0 {
		return nil
	}
	last := cloneSnapshot(t.snapshots[len(t.snapshots)-1])
	return &last
}

// cloneSnapshot deep-copies a snapshot so slice fields never alias the
// tracer's internal storage.
func cloneSnapshot(s GenerationSnapshot) GenerationSnapshot {
	s.TopStrategies = append([]string(nil), s.TopStrategies...)
	if len(s.Changes) > 0 {
		changes := make([]GenerationChange, len(s.Changes))
		copy(changes, s.Changes)
		s.Changes = changes
	}
	return s
}

// GenerationCount returns the number of recorded generations.
func (t *EvolutionTracer) GenerationCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.snapshots)
}

// TrajectoryViews returns the recorded generations as JSON-friendly values
// (oldest first), ready for the Dashboard's /evolution/trajectory endpoint.
// Each entry mirrors GenerationSnapshot. Returns nil when
// nothing is recorded.
func (t *EvolutionTracer) TrajectoryViews() []map[string]any {
	snaps := t.Snapshot()
	if len(snaps) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(snaps))
	for _, s := range snaps {
		changes := make([]map[string]any, 0, len(s.Changes))
		for _, c := range s.Changes {
			changes = append(changes, map[string]any{
				"strategy_id": c.StrategyID,
				"description": c.Description,
				"impact":      c.Impact,
			})
		}
		out = append(out, map[string]any{
			"generation":     s.Generation,
			"best_score":     s.BestScore,
			"top_strategies": s.TopStrategies,
			"changes":        changes,
			"breakthrough":   s.Breakthrough,
			"regression":     s.Regression,
		})
	}
	return out
}
