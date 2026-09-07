// Package genome provides population management for genetic algorithm evolution.
// This file contains guard methods for validation, diversity recovery, and reporting.
package genome

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// ensureEvaluatedBeforeSelection validates that all individuals in the population
// have been evaluated before selection proceeds. Returns an error if any agent
// has an unevaluated score.
//
// This implements Phase 1 Item 2 from the GA Hardening Plan: "Selection never
// operates on unevaluated individuals."
func (p *Population) ensureEvaluatedBeforeSelection() error {
	for i, a := range p.Agents {
		if !IsScoreEvaluated(a.Score) {
			return fmt.Errorf("agent %d (%s) has unevaluated score %.1f; "+
				"score all agents before calling Evolve", i, a.ID, a.Score)
		}
	}
	return nil
}

// DiversityStats returns a detailed diversity report for the current population.
// Thread-safe: acquires read lock.
//
// Returns:
//
//	DiversityReport - detailed breakdown of numeric, categorical, and lineage diversity.
func (p *Population) DiversityStats() DiversityReport {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.measureDiversityReportLocked()
}

// injectFreshMutantsLocked replaces the bottom portion of the population with
// randomly perturbed clones of elite strategies to restore diversity.
//
// Args:
//
//	eliteCount - number of leading agents protected from replacement.
func (p *Population) injectFreshMutantsLocked(eliteCount int) {
	n := len(p.Agents)
	if n <= eliteCount+1 {
		// Population too small to inject without risking elites. Skip.
		return
	}

	// Determine how many agents to replace (bottom portion).
	// Never replace more than half of non-elite agents to preserve stability.
	nonEliteCount := n - eliteCount
	maxReplace := max(1, nonEliteCount/2) // At most 50% of non-elites
	replaceCount := min(maxReplace, n/3)  // But also cap at 33% of total
	if replaceCount < 1 {
		replaceCount = 1
	}

	startIdx := n - replaceCount

	// Safety: ensure we don't touch the elite region.
	// Elite region is [0, eliteCount). Injection region is [startIdx, n).
	// Requirement: startIdx >= eliteCount (no overlap).
	if startIdx < eliteCount {
		// Elite count is too large for safe injection at default rate.
		// Reduce replacement count until regions don't overlap.
		replaceCount = n - eliteCount
		if replaceCount < 1 {
			return // Can't safely inject any agents.
		}
		startIdx = n - replaceCount
	}

	for i := startIdx; i < n; i++ {
		// Pick a random template from the protected (elite) region.
		var templateIdx int
		if eliteCount > 0 {
			templateIdx = p.rng.Intn(eliteCount)
		} else {
			templateIdx = p.rng.Intn(n)
		}
		template := p.Agents[templateIdx].Clone()

		// Apply wider perturbation with selective parameter mixing.
		// Each param has a 40% chance to remain unchanged, preserving good
		// alleles while introducing targeted variation in the rest.
		for k, v := range template.Params {
			if p.rng.Float64() < 0.4 {
				continue // keep original value
			}
			if f, ok := v.(float64); ok {
				// Perturb by ±80%: range [0.2x, 1.8x].
				perturbation := f * (0.2 + p.rng.Float64()*1.6)
				template.Params[k] = perturbation
			} else if iVal, ok := v.(int); ok {
				// Perturb int values: ensure the random range is at least 2
				// so that iVal=0 can still get a non-zero delta.
				base := max(iVal, 1)
				if base < 1 {
					base = 1
				}
				delta := p.rng.Intn(base*2+1) - base
				template.Params[k] = iVal + delta
			}
		}

		// Randomly swap prompt template with another agent's to introduce variation.
		if p.rng.Float64() < 0.3 && len(p.Agents) > 1 {
			otherIdx := p.rng.Intn(len(p.Agents))
			if otherIdx != i {
				template.PromptTemplate = p.Agents[otherIdx].PromptTemplate
			}
		}

		template.Score = ScoreUnevaluated
		template.ID = fmt.Sprintf("fresh-mut-%d-gen%d", i, p.Generation)
		template.ParentID = p.Agents[templateIdx].ID
		template.Version++
		template.StrategyMutationType = mutation.MutationParameter
		template.MutationDesc = "fresh mutant injection for diversity recovery"

		p.Agents[i] = template
	}

	el.Debug(context.Background(), "injectFreshMutantsLocked", "fresh mutants injected",
		"generation", p.Generation,
		"replace_count", replaceCount,
		"start_index", startIdx,
		"elite_count", eliteCount,
	)
	p.recordRecoveryActionLocked("fresh_injection", 1)
}

// preserveElites copies the top EliteCount survivors without modification.
// When PerLineageElites is enabled, first reserves top-1 per unique lineage,
// then fills remaining slots from global top performers.
// Elites are deep-cloned to prevent shared state across generations.
//
// Args:
//
//	survivors - sorted survivor strategies (highest score first).
//
// Returns:
//
//	[]*mutation.Strategy - deep-cloned elite strategies.
func (p *Population) preserveElites(survivors []*mutation.Strategy) []*mutation.Strategy {
	if p.cfg.PerLineageElites {
		return p.preservePerLineageElites(survivors)
	}

	eliteCount := min(p.cfg.EliteCount, len(survivors))
	if eliteCount <= 0 {
		return []*mutation.Strategy{}
	}

	elites := make([]*mutation.Strategy, 0, eliteCount)
	for i := 0; i < eliteCount; i++ {
		//nolint:gosec // G602: Index bounds guaranteed by eliteCount <= len(survivors) from min() call
		elites = append(elites, survivors[i].Clone())
	}

	return elites
}

// preservePerLineageElites implements per-lineage elite preservation.
// First selects the best individual from each unique ParentID, then fills
// remaining elite slots from global top performers.
func (p *Population) preservePerLineageElites(survivors []*mutation.Strategy) []*mutation.Strategy {
	if len(survivors) == 0 {
		return []*mutation.Strategy{}
	}

	// Find the best index for each unique lineage.
	lineageBest := make(map[string]int)
	for i, s := range survivors {
		pid := s.ParentID
		if pid == "" {
			pid = "(root)"
		}
		existingIdx, ok := lineageBest[pid]
		if !ok || s.Score > survivors[existingIdx].Score {
			lineageBest[pid] = i
		}
	}

	targetCount := p.cfg.EliteCount
	if targetCount <= 0 {
		targetCount = 1
	}

	// First pass: reserve top-1 per active lineage.
	reserved := make(map[int]bool, len(survivors))
	elites := make([]*mutation.Strategy, 0, targetCount)

	for _, idx := range lineageBest {
		if len(elites) >= targetCount {
			break
		}
		elites = append(elites, survivors[idx].Clone())
		reserved[idx] = true
	}

	// Fill remaining slots from global top performers.
	for i := 0; i < len(survivors) && len(elites) < targetCount; i++ {
		if reserved[i] {
			continue
		}
		elites = append(elites, survivors[i].Clone())
		reserved[i] = true
	}

	el.Debug(context.Background(), "preservePerLineageElites", "per-lineage elites preserved",
		"total_elites", len(elites),
		"unique_lineages", len(lineageBest),
		"elite_count_config", p.cfg.EliteCount,
	)

	return elites
}

// preservePromptDiversityLocked checks if all elites use the same prompt template.
// If they do, and the current population contains an alternative template individual
// with a score above the floor threshold, that individual is force-retained as an
// exploration seed. This prevents categorical collapse where all individuals converge
// to the same prompt template.
//
// The method is called after elite preservation and before offspring generation.
// It modifies the elite set in-place by appending a prompt diversity seed when needed.
//
// Args:
//
//	elites - the current set of elite strategies (may be modified).
//	population - the full sorted population (used to check for alternatives).
//
// Returns:
//
//	[]*mutation.Strategy - potentially expanded elite set with diversity seed.
func (p *Population) preservePromptDiversityLocked(elites []*mutation.Strategy, population []*mutation.Strategy) []*mutation.Strategy {
	if p.cfg.DisablePromptDiversityGuard || len(elites) == 0 || len(population) <= len(elites) {
		return elites
	}

	// Check if all elites use one prompt template.
	firstTemplate := elites[0].PromptTemplate
	allSame := true
	for _, e := range elites {
		if e.PromptTemplate != firstTemplate {
			allSame = false
			break
		}
	}
	if !allSame {
		return elites
	}

	// All elites use the same template. Look for an alternative in the population.
	const promptDiversityScoreFloor = -0.5 // Allow negative scores but not extremely bad ones.
	for _, s := range population {
		if s.PromptTemplate != firstTemplate && IsScoreEvaluated(s.Score) && s.Score >= promptDiversityScoreFloor {
			clone := s.Clone()
			clone.MutationDesc = "prompt_diversity_seed"
			clone.GenerationCreated = p.Generation + 1

			// If elites already fill the population, replace the weakest elite
			// with the diversity seed instead of appending beyond p.Size.
			if p.Size > 0 && len(elites) >= p.Size {
				// Find the weakest (lowest score) elite to replace.
				weakestIdx := 0
				for i := 1; i < len(elites); i++ {
					//nolint:gosec // G602: Bound check ensured by len(elites) >= p.Size > 0
					if elites[i].Score < elites[weakestIdx].Score {
						weakestIdx = i
					}
				}
				replacedID := elites[weakestIdx].ID
				elites[weakestIdx] = clone
				el.Debug(context.Background(), "preservePromptDiversityLocked", "seed replaced weakest elite",
					"template", s.PromptTemplate,
					"replaced_id", replacedID,
					"agent_id", s.ID,
				)
			} else {
				elites = append(elites, clone)
				el.Debug(context.Background(), "preservePromptDiversityLocked", "diversity seed force-retained",
					"template", s.PromptTemplate,
					"score", s.Score,
					"agent_id", s.ID,
					"elite_count_before", len(elites)-1,
				)
			}
			return elites
		}
	}

	return elites
}

// ---------------------------------------------------------------------------
// Performance characteristics of applyFitnessSharing
// ---------------------------------------------------------------------------
//
// Time complexity:
//   - Small populations (m <= FitnessSharingSampleLimit): O(m² × k) where m is
//     the number of scored agents and k is the number of parameter keys per agent.
//     The distance matrix is computed once in O(m²/2) paramDistance calls (upper
//     triangular only), then looked up in O(1) during penalty accumulation.
//   - Large populations (m > FitnessSharingSampleLimit): O(m × s × k) where
//     s = FitnessSharingSampleSize. Each agent checks s random neighbors instead
//     of all m-1 agents, reducing quadratic to linear scaling.
//
// Space complexity:
//   - With distance matrix cache: O(m²) for the full distance matrix.
//   - With sampling mode: O(s) per agent for neighbor index storage (no matrix).
//
// Recommended max population size for real-time evolution: ~200 agents. Beyond
// this, consider enabling spatial indexing (grid-based KD-tree or similar) to
// achieve sub-linear neighbor queries.
//
// ---------------------------------------------------------------------------

// applyFitnessSharing reduces scores of agents in crowded regions of parameter space.
// This prevents all agents from converging to the same local optimum by penalizing
// similarity — agents that occupy the same niche share their fitness.
//
// Agents with IsScoreEvaluated() == false (unevaluated) are excluded from both the distance
// calculation and penalty, preventing fitness sharing from operating on
// meaningless default scores that would distort diversity metrics.
//
// Args:
//
//	eliteCount - number of leading agents (already sorted by score) protected from penalty.
func (p *Population) applyFitnessSharing(eliteCount int) {
	n := len(p.Agents)
	if n < 2 {
		return
	}

	const (
		shareSigma  = FitnessSharingSigma // sharing coefficient
		nicheRadius = FitnessNicheRadius  // distance threshold for "same niche"
	)

	// Build a filtered index of scored agents only (IsScoreEvaluated()).
	// Unevaluated agents (Score=ScoreUnevaluated) should not participate in fitness sharing
	// because their score is meaningless and would distort the shared fitness signal.
	scoredIdx := make([]int, 0, n)
	for i, a := range p.Agents {
		if IsScoreEvaluated(a.Score) {
			scoredIdx = append(scoredIdx, i)
		}
	}

	m := len(scoredIdx)
	if m < 2 {
		return
	}

	// Build temp slice for key/range collection from scored agents only.
	scored := make([]*mutation.Strategy, m)
	for k, idx := range scoredIdx {
		scored[k] = p.Agents[idx]
	}
	keys := collectAgentParamKeys(scored)
	ranges := computeParamRanges(scored, keys)

	// PERF: Choose computation strategy based on population size.
	// Small populations use exact pairwise distances with matrix caching;
	// large populations use randomized neighbor sampling to bound cost;
	// very large populations use grid-based spatial indexing to achieve
	// sub-linear neighbor lookup. Thresholds are configurable.
	limit := p.cfg.FitnessSharingSampleLimit
	spatial := p.cfg.SpatialIndexThreshold
	if limit <= 0 {
		limit = m + 1 // Disable sampling: always use exact mode
	}
	switch {
	case spatial > 0 && m > spatial:
		p.applyFitnessSharingSpatial(scoredIdx, scored, keys, ranges, eliteCount, nicheRadius, shareSigma)
	case m <= limit:
		p.applyFitnessSharingExact(scoredIdx, scored, keys, ranges, eliteCount, nicheRadius, shareSigma)
	default:
		p.applyFitnessSharingSampled(scoredIdx, scored, keys, ranges, eliteCount, nicheRadius, shareSigma)
	}
}

// applyFitnessSharingExact computes fitness sharing penalties using an exact
// pairwise distance matrix. The upper-triangular distance matrix is computed
// once and reused for all pair lookups, eliminating redundant paramDistance calls.
func (p *Population) applyFitnessSharingExact(
	scoredIdx []int,
	scored []*mutation.Strategy,
	keys []string,
	ranges map[string]float64,
	eliteCount int,
	nicheRadius float64,
	shareSigma float64,
) {
	m := len(scoredIdx)

	// PERF: Pre-compute full distance matrix (upper-triangular mirrored).
	// paramDistance(a,b) == paramDistance(b,a), so we compute each unique pair
	// once and mirror it. This cuts paramDistance calls from m*(m-1) to m*(m-1)/2,
	// providing ~2x speedup for the distance computation phase.
	distMatrix := make([]float64, m*m)
	for ki := 0; ki < m; ki++ {
		for kj := ki + 1; kj < m; kj++ {
			dist := paramDistance(scored[ki], scored[kj], keys, ranges)
			distMatrix[ki*m+kj] = dist
			distMatrix[kj*m+ki] = dist
		}
	}

	// Accumulate penalties using cached distances.
	for ki, i := range scoredIdx {
		if i < eliteCount {
			continue // skip elites
		}
		crowdCount := 0
		for kj := 0; kj < m; kj++ {
			if ki == kj {
				continue
			}
			if distMatrix[ki*m+kj] < nicheRadius {
				crowdCount++
			}
		}
		if crowdCount > 0 {
			penalty := shareSigma * float64(crowdCount)
			if p.Agents[i].SelectionScore == 0 {
				p.Agents[i].SelectionScore = p.Agents[i].Score
			}
			p.Agents[i].SelectionScore /= (1.0 + penalty)
		}
	}
}

// applyFitnessSharingSampled computes fitness sharing penalties using randomized
// neighbor sampling. When the scored population exceeds FitnessSharingSampleLimit,
// checking all O(m²) pairs is prohibitively expensive. Instead, each non-elite
// agent checks against FitnessSharingSampleSize randomly chosen neighbors,
// bounding total work to O(m × FitnessSharingSampleSize × k).
//
// PERF: Uses reservoir sampling instead of full Fisher-Yates permutation to
// select random neighbors. Each agent picks sampleSize random distinct indices
// in O(m) time with O(k) memory instead of O(m²) time with O(m) memory.
func (p *Population) applyFitnessSharingSampled(
	scoredIdx []int,
	scored []*mutation.Strategy,
	keys []string,
	ranges map[string]float64,
	eliteCount int,
	nicheRadius float64,
	shareSigma float64,
) {
	m := len(scoredIdx)
	sampleSize := min(p.cfg.FitnessSharingSampleSize, m-1)

	// PERF: Pre-allocate reservoir once and reuse across agents to avoid
	// per-agent allocation in the hot loop.
	reservoir := make([]int, sampleSize)

	for ki, i := range scoredIdx {
		if i < eliteCount {
			continue // skip elites
		}

		// Reservoir sampling: select sampleSize random distinct indices from [0, m)
		// in O(m) time with O(k) memory. The first sampleSize elements are seeded
		// sequentially, then each subsequent element has a decreasing probability
		// of replacing an existing reservoir element.
		for idx := 0; idx < sampleSize; idx++ {
			reservoir[idx] = idx
		}
		for idx := sampleSize; idx < m; idx++ {
			j := p.rng.Intn(idx + 1)
			if j < sampleSize {
				reservoir[j] = idx
			}
		}

		crowdCount := 0
		for s := 0; s < sampleSize; s++ {
			kj := reservoir[s]
			if kj == ki {
				continue
			}
			dist := paramDistance(scored[ki], scored[kj], keys, ranges)
			if dist < nicheRadius {
				crowdCount++
			}
		}

		if crowdCount > 0 {
			penalty := shareSigma * float64(crowdCount)
			if p.Agents[i].SelectionScore == 0 {
				p.Agents[i].SelectionScore = p.Agents[i].Score
			}
			p.Agents[i].SelectionScore /= (1.0 + penalty)
		}
	}
}

// applyFitnessSharingSpatial computes fitness sharing penalties using grid-based
// spatial indexing. For very large populations (configurable via SpatialIndexThreshold),
// this scales sub-linearly by only comparing each agent against neighbors in the
// same or adjacent grid cells rather than random sampling or the full population.
//
// The spatial index is built on the top-N highest-variance float parameters
// (capped at maxSpatialDims=6 to keep cell enumeration tractable). Distance
// calculations still use the full parameter space.
func (p *Population) applyFitnessSharingSpatial(
	scoredIdx []int,
	scored []*mutation.Strategy,
	keys []string,
	ranges map[string]float64,
	eliteCount int,
	nicheRadius float64,
	shareSigma float64,
) {
	m := len(scoredIdx)
	if m < 2 {
		return
	}

	sidx := newSpatialIndex(scoredIdx, scored, keys, ranges, nicheRadius)
	if sidx == nil {
		// Fallback to sampled mode when spatial indexing doesn't apply.
		p.applyFitnessSharingSampled(scoredIdx, scored, keys, ranges, eliteCount, nicheRadius, shareSigma)
		return
	}

	for ki, i := range scoredIdx {
		if i < eliteCount {
			continue
		}

		neighbors := sidx.neighborsWithin(ki)
		crowdCount := 0
		for _, nk := range neighbors {
			if nk == ki {
				continue
			}
			dist := paramDistance(scored[ki], scored[nk], keys, ranges)
			if dist < nicheRadius {
				crowdCount++
			}
		}

		if crowdCount > 0 {
			penalty := shareSigma * float64(crowdCount)
			if p.Agents[i].SelectionScore == 0 {
				p.Agents[i].SelectionScore = p.Agents[i].Score
			}
			p.Agents[i].SelectionScore /= (1.0 + penalty)
		}
	}
}
