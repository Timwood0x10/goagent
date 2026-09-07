package aresrecovery

// Change attribution: estimates how much each change in a
// generation contributed to the score delta versus the previous generation.
// The attribution is deliberately simple and auditable — it does not pretend
// to run counterfactuals: explicit Impact values (when the evolution system
// provides them) win; otherwise the delta is distributed equally across the
// changes of the newer generation. The dashboard renders the result as
// "change → impact" so operators can spot breakthroughs and regressions.

// ChangeAttributor computes per-change impact estimates between two
// consecutive generation snapshots.
type ChangeAttributor struct{}

// NewChangeAttributor creates an attributor.
func NewChangeAttributor() *ChangeAttributor {
	return &ChangeAttributor{}
}

// Attribute estimates the impact of each change introduced in `after`
// relative to `before`.
//
// Rules (documented, no hidden assumptions):
//   - when `after` has no changes, the result is empty (nothing to attribute);
//   - a change with an explicit non-zero Impact keeps that value (evolution
//     supplied a counterfactual/estimate);
//   - all other changes share the REMAINING score delta equally — each
//     receives (delta - explicitImpacts) / n where n is the number of changes
//     without an explicit impact;
//   - a zero remaining delta yields zero for every shared change.
//
// Args:
//   - before: the previous generation (may be nil → delta is measured from 0).
//   - after: the newer generation whose changes are attributed (must not be
//     nil; an empty Changes slice yields an empty result).
//
// Returns:
//   - map[string]float64: strategy id → estimated impact of its change.
func (ChangeAttributor) Attribute(before, after *GenerationSnapshot) map[string]float64 {
	if after == nil || len(after.Changes) == 0 {
		return nil
	}
	prev := 0.0
	if before != nil {
		prev = before.BestScore
	}
	delta := after.BestScore - prev

	// Partition: explicit impacts vs. changes needing equal attribution.
	out := make(map[string]float64, len(after.Changes))
	shared := make([]string, 0, len(after.Changes))
	explicitSum := 0.0
	for _, c := range after.Changes {
		if c.Impact != 0 {
			out[c.StrategyID] = c.Impact
			explicitSum += c.Impact
			continue
		}
		shared = append(shared, c.StrategyID)
	}
	remaining := delta - explicitSum
	if len(shared) == 0 || remaining == 0 {
		// No movement left to explain, or every change has an explicit impact.
		return out
	}
	share := remaining / float64(len(shared))
	for _, id := range shared {
		out[id] = share
	}
	return out
}

// AttributeTrajectory returns the per-change attribution for every adjacent
// generation pair in a recorded trajectory (oldest first). The result maps
// generation number → strategy id → impact, ready for the dashboard.
//
// Args:
//   - snaps: recorded generation snapshots (from EvolutionTracer.Snapshot).
//
// Returns:
//   - []map[string]float64: one entry per generation (index i attributes
//     generation i+1's changes); empty when fewer than two snapshots exist.
func (ChangeAttributor) AttributeTrajectory(snaps []GenerationSnapshot) []map[string]float64 {
	if len(snaps) < 2 {
		return nil
	}
	out := make([]map[string]float64, 0, len(snaps)-1)
	for i := 1; i < len(snaps); i++ {
		out = append(out, ChangeAttributor{}.Attribute(&snaps[i-1], &snaps[i]))
	}
	return out
}
