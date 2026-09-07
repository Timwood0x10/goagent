package genome

import (
	"context"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// DiversityReport provides a detailed breakdown of population diversity metrics.
// It splits overall diversity into numeric, categorical, and lineage components
// for targeted intervention decisions.
type DiversityReport struct {
	// Overall is the weighted average of all diversity components [0, 1].
	Overall float64

	// Numeric is the average pairwise normalized parameter distance [0, 1].
	Numeric float64

	// Categorical measures differences in prompt templates, tool configs, etc. [0, 1].
	Categorical float64

	// Lineage measures parent ID concentration; 1.0 = all different parents, 0.0 = same parent.
	Lineage float64

	// DominantLineageShare is the fraction of population sharing the most common ParentID [0, 1].
	// Values above configured threshold (default 0.6) indicate lineage collapse.
	DominantLineageShare float64

	// EffectiveWeights is the actual weight configuration used to compute Overall.
	// Useful for observability and debugging custom weight configurations.
	EffectiveWeights DiversityWeightConfig

	// PromptTemplateDistribution maps each prompt template string to the number
	// of agents using it. Empty-string template is included as a key if any agent
	// has no prompt template set.
	PromptTemplateDistribution map[string]int
}

// DiversityMetricVersion indicates the current diversity metric version.
// v2 (introduced 0.1.1): Weighted components (numeric 40%, categorical 40%, lineage 20%).
// v1 (legacy): Single aggregate paramDistance including numeric + categorical.
const DiversityMetricVersion = 2

// AdaptiveConfig holds configurable parameters for adaptive mutation rate tuning.
// These replace the previously hard-coded constants to allow runtime configuration.
type AdaptiveConfig struct {
	// EmergencyDiversityThreshold is the diversity level below which
	// emergency maximum mutation rate is forced (default 0.05).
	EmergencyDiversityThreshold float64 `json:"emergency_diversity_threshold"`

	// LowDiversityBoostFactor is the base multiplier applied when diversity
	// is below the configured threshold. Range extends to 2.5x via deficit scaling (default 1.5).
	LowDiversityBoostFactor float64 `json:"low_diversity_boost_factor"`

	// HighDecayRate is the decay factor applied when diversity is very high (>3x threshold) (default 0.95).
	HighDecayRate float64 `json:"high_decay_rate"`

	// ModerateDecayRate is the gentle decay factor when current rate is far above base (default 0.98).
	ModerateDecayRate float64 `json:"moderate_decay_rate"`

	// DiversityFloorThreshold is the diversity level above which the minimum
	// mutation rate floor is relaxed to the configured MinMutationRate (default 0.3).
	DiversityFloorThreshold float64 `json:"diversity_floor_threshold"`

	// MinMutationFloor is the absolute minimum mutation rate enforced when
	// diversity is below DiversityFloorThreshold (default 0.15).
	MinMutationFloor float64 `json:"min_mutation_floor"`
}

// DefaultAdaptiveConfig returns an AdaptiveConfig with the standard defaults.
//
// Returns:
//   - *AdaptiveConfig: configuration with default values applied.
func DefaultAdaptiveConfig() *AdaptiveConfig {
	return &AdaptiveConfig{
		EmergencyDiversityThreshold: 0.05,
		LowDiversityBoostFactor:     1.5,
		HighDecayRate:               0.95,
		ModerateDecayRate:           0.98,
		DiversityFloorThreshold:     0.3,
		MinMutationFloor:            0.15,
	}
}

// computeBestScoreLocked returns the highest score in the current agents.
// Caller must hold at least a read lock.
func (p *Population) computeBestScoreLocked() float64 {
	best := -1.0
	for _, a := range p.Agents {
		if a.Score > best {
			best = a.Score
		}
	}
	return best
}

// measureDiversityReportLocked computes a detailed diversity breakdown of the population.
// Splits diversity into numeric (parameter distance), categorical (prompt/tools/model),
// and lineage (parent concentration) components.
// Caller must hold at least a read lock on p.mu.
//
// NOTE: The weighted Overall formula (v2) may produce different values than the
// legacy paramDistance-based metric (v1). Adaptive thresholds configured for
// v1 may need recalibration. See DiversityMetricVersion.
func (p *Population) measureDiversityReportLocked() DiversityReport {
	n := len(p.Agents)
	report := DiversityReport{
		Overall:                    1.0,
		Numeric:                    1.0,
		Categorical:                1.0,
		Lineage:                    1.0,
		PromptTemplateDistribution: p.countPromptTemplateDistributionLocked(),
	}

	if n < 2 {
		return report
	}

	// Numeric diversity: average pairwise normalized parameter distance.
	report.Numeric = p.measureNumericDiversityLocked()

	// Categorical diversity: differences in prompt templates, tool configs, etc.
	report.Categorical = p.measureCategoricalDiversityLocked()

	// Lineage diversity: parent ID concentration analysis.
	report.Lineage, report.DominantLineageShare = p.measureLineageDiversityLocked()

	// Weighted overall using configured diversity weights (defaults: N=0.4, C=0.4, L=0.2).
	weights := p.cfg.DiversityWeights.normalize()
	report.Overall = report.Numeric*weights.Numeric +
		report.Categorical*weights.Categorical +
		report.Lineage*weights.Lineage

	// Store effective weights for observability.
	report.EffectiveWeights = weights

	return report
}

// measureDiversityLocked returns the overall diversity score for backward compatibility.
// Prefer measureDiversityReportLocked for detailed analysis.
func (p *Population) measureDiversityLocked() float64 {
	return p.measureDiversityReportLocked().Overall
}

// measureNumericDiversityLocked computes average pairwise normalized parameter distance.
// Only numeric parameters participate. Returns [0, 1].
//
// PERF: When population exceeds DiversitySampleSize, estimates diversity by comparing
// each agent against DiversitySampleSize random neighbors instead of all O(n²) pairs.
// This bounds the cost to O(n × DiversitySampleSize) instead of O(n²).
func (p *Population) measureNumericDiversityLocked() float64 {
	n := len(p.Agents)
	if n < 2 {
		return 1.0
	}

	allKeys := collectAgentParamKeys(p.Agents)
	if len(allKeys) == 0 {
		return 1.0
	}

	ranges := computeParamRanges(p.Agents, allKeys)

	sampleSize := p.cfg.DiversitySampleSize
	if sampleSize <= 0 || n <= sampleSize {
		// Exact mode: compute all O(n²/2) pairwise distances.
		var totalDist float64
		var pairCount int
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				dist := numericParamDistance(p.Agents[i], p.Agents[j], allKeys, ranges)
				totalDist += dist
				pairCount++
			}
		}
		if pairCount == 0 {
			return 1.0
		}
		return totalDist / float64(pairCount)
	}

	// Sampled mode: each agent compares against sampleSize random neighbors.
	// Uses rejection sampling with linear dedup scan over a small reusable slice.
	// For sampleSize ≈ 200, scanning 200 ints per rejection is cheaper than
	// allocating and GCing a map per agent. Total: O(n × DiversitySampleSize).
	var totalDist float64
	var sampleCount int
	neighbors := make([]int, 0, sampleSize)

	for i := 0; i < n; i++ {
		neighbors = neighbors[:0]

		for len(neighbors) < sampleSize {
			j := p.rng.Intn(n)
			if j == i {
				continue
			}
			// Linear scan dedup over small slice — faster than map allocation.
			dup := false
			for _, nj := range neighbors {
				if nj == j {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			neighbors = append(neighbors, j)
		}

		for _, j := range neighbors {
			dist := numericParamDistance(p.Agents[i], p.Agents[j], allKeys, ranges)
			totalDist += dist
			sampleCount++
		}
	}

	if sampleCount == 0 {
		return 1.0
	}
	return totalDist / float64(sampleCount)
}

// numericParamDistance computes normalized distance using only numeric parameters.
// Returns the average normalized difference across all shared numeric keys [0, 1].
func numericParamDistance(a, b *mutation.Strategy, keys []string, ranges map[string]float64) float64 {
	var totalDist float64
	var count int
	for _, k := range keys {
		va, okA := a.Params[k]
		vb, okB := b.Params[k]
		if !okA || !okB {
			continue
		}
		fa, okA := toFloat64(va)
		fb, okB := toFloat64(vb)
		if !okA || !okB {
			continue
		}
		r := ranges[k]
		if r < 1e-10 {
			r = 1.0
		}
		totalDist += absFloat(fa-fb) / r
		count++
	}
	if count == 0 {
		return 0.0
	}
	return totalDist / float64(count)
}

// measureCategoricalDiversityLocked computes diversity based on categorical attributes
// (prompt template, tools configuration). Returns [0, 1].
func (p *Population) measureCategoricalDiversityLocked() float64 {
	n := len(p.Agents)
	if n < 2 {
		return 1.0
	}

	var totalDist float64
	var pairCount int
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			dist := 0.0
			count := 0

			// Prompt template difference.
			if p.Agents[i].PromptTemplate != p.Agents[j].PromptTemplate {
				dist += 1.0
			}
			count++

			// Tools configuration difference.
			ta, okA := p.Agents[i].Params["tools"].(string)
			tb, okB := p.Agents[j].Params["tools"].(string)
			if okA && okB && ta != tb {
				dist += 1.0
			} else if okA != okB {
				dist += 1.0
			}
			count++

			if count > 0 {
				totalDist += dist / float64(count)
			}
			pairCount++
		}
	}
	if pairCount == 0 {
		return 1.0
	}
	return totalDist / float64(pairCount)
}

// countPromptTemplateDistributionLocked returns the count of each prompt template
// in the current population. Useful for detecting categorical convergence where
// all agents use the same prompt template.
//
// Caller must hold at least a read lock on p.mu.
//
// Returns:
//
//	map[string]int - mapping of prompt template to count of agents using it.
func (p *Population) countPromptTemplateDistributionLocked() map[string]int {
	dist := make(map[string]int)
	for _, a := range p.Agents {
		dist[a.PromptTemplate]++
	}
	return dist
}

// measureLineageDiversityLocked computes lineage (parent ID) diversity.
//
// Returns:
//
//	float64: lineage diversity score [0, 1] where 1 = all unique parents.
//	float64: dominant lineage share [0, 1] fraction of population from most common parent.
func (p *Population) measureLineageDiversityLocked() (float64, float64) {
	n := len(p.Agents)
	if n < 2 {
		return 1.0, 1.0
	}

	// Count occurrences of each parent ID.
	parentCount := make(map[string]int, n)
	for _, a := range p.Agents {
		pid := a.ParentID
		if pid == "" {
			pid = "(root)" // Treat empty ParentID as root lineage.
		}
		parentCount[pid]++
	}

	// Find dominant lineage.
	maxCount := 0
	for _, c := range parentCount {
		if c > maxCount {
			maxCount = c
		}
	}
	dominantShare := float64(maxCount) / float64(n)

	// Lineage diversity: unique parent ratio.
	uniqueParents := len(parentCount)
	lineageDiv := float64(uniqueParents) / float64(n)

	return lineageDiv, dominantShare
}

// collectAgentParamKeys returns the union of all parameter keys across agents.
func collectAgentParamKeys(agents []*mutation.Strategy) []string {
	seen := make(map[string]struct{})
	for _, a := range agents {
		for k := range a.Params {
			seen[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// computeParamRanges returns the range (max - min) for each numeric parameter
// across all agents. A default of 1.0 is used when the range is zero
// (all values identical) to avoid division by zero in distance normalization.
func computeParamRanges(agents []*mutation.Strategy, keys []string) map[string]float64 {
	ranges := make(map[string]float64, len(keys))
	for _, k := range keys {
		var minVal, maxVal float64
		first := true
		for _, a := range agents {
			v, ok := a.Params[k]
			if !ok {
				continue
			}
			f, ok := toFloat64(v)
			if !ok {
				continue
			}
			if first {
				minVal = f
				maxVal = f
				first = false
			} else {
				if f < minVal {
					minVal = f
				}
				if f > maxVal {
					maxVal = f
				}
			}
		}
		if first {
			ranges[k] = 1.0
		} else {
			diff := maxVal - minVal
			if diff < 1e-10 {
				ranges[k] = 1.0
			} else {
				ranges[k] = diff
			}
		}
	}
	return ranges
}

// paramDistance returns the normalized distance between two agents in shared
// parameter space. Non-numeric or missing parameters are skipped.
func paramDistance(a, b *mutation.Strategy, keys []string, ranges map[string]float64) float64 {
	var totalDist float64
	var count int
	for _, k := range keys {
		va, okA := a.Params[k]
		vb, okB := b.Params[k]
		if !okA || !okB {
			continue
		}
		fa, okA := toFloat64(va)
		fb, okB := toFloat64(vb)
		if !okA || !okB {
			continue
		}
		r := ranges[k]
		if r < 1e-10 {
			r = 1.0
		}
		totalDist += absFloat(fa-fb) / r
		count++
	}

	// Categorical distance for PromptTemplate: different template = max distance (1.0).
	if a.PromptTemplate != b.PromptTemplate {
		totalDist += 1.0
		count++
	}

	// Categorical distance for tools: different tool configuration = max distance (1.0).
	if ta, okA := a.Params["tools"].(string); okA {
		if tb, okB := b.Params["tools"].(string); okB && ta != tb {
			totalDist += 1.0
			count++
		}
	}

	if count == 0 {
		return 0.0
	}
	return totalDist / float64(count)
}

// toFloat64 attempts to convert an arbitrary numeric value to float64.
func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint64:
		return float64(val), true
	case uint32:
		return float64(val), true
	default:
		return 0, false
	}
}

// absFloat returns the absolute value of a float64.
func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// getAdaptiveConfigLocked returns the effective adaptive config, using defaults
// if none was configured. Caller must hold at least a read lock.
func (p *Population) getAdaptiveConfigLocked() *AdaptiveConfig {
	if p.cfg.AdaptiveConfig != nil {
		return p.cfg.AdaptiveConfig
	}
	return DefaultAdaptiveConfig()
}

// adjustMutationRateLocked updates currentMutationRate based on population diversity.
// Called after each generation to prevent premature convergence.
//
// Key behaviors:
//   - Emergency mode: critically low diversity (< EmergencyDiversityThreshold) forces max mutation rate.
//   - Low diversity: aggressive boost proportional to deficit below threshold.
//   - High diversity: gentle decay allowed.
//   - Moderate diversity: maintain current rate (no drift toward base).
//   - Floor protection: never drop below MinMutationFloor unless diversity is genuinely high (> DiversityFloorThreshold).
func (p *Population) adjustMutationRateLocked() {
	ac := p.getAdaptiveConfigLocked()
	div := p.measureDiversityLocked()

	// Emergency mode: critically low diversity — force maximum exploration.
	if div < ac.EmergencyDiversityThreshold {
		el.Warn(context.Background(), "adjustMutationRateLocked", "emergency mutation rate boost",
			"diversity", div,
			"generation", p.Generation,
			"mutation_rate", p.cfg.MaxMutationRate,
		)
		p.currentMutationRate = p.cfg.MaxMutationRate
		p.recordRecoveryActionLocked("mutation_rate_boost", 1)
		return
	}

	// Low diversity: aggressive boost proportional to how far below threshold.
	switch {
	case div < p.cfg.DiversityThreshold:
		deficit := p.cfg.DiversityThreshold - div
		boostFactor := ac.LowDiversityBoostFactor + (deficit/p.cfg.DiversityThreshold)*1.0 // range: 1.5x – 2.5x
		p.currentMutationRate = minFloat(p.currentMutationRate*boostFactor, p.cfg.MaxMutationRate)
		p.recordRecoveryActionLocked("mutation_rate_boost", 1)
	case div > p.cfg.DiversityThreshold*3:
		// Very high diversity: allow gentle decay toward floor.
		p.currentMutationRate = maxFloat(p.currentMutationRate*ac.HighDecayRate, p.cfg.MinMutationRate)
	default:
		// Moderate diversity: maintain current rate — only drift down if
		// significantly above base to avoid unnecessary reduction.
		if p.currentMutationRate > p.cfg.MutationRate*2 {
			p.currentMutationRate = maxFloat(p.currentMutationRate*ac.ModerateDecayRate, p.cfg.MutationRate*ac.LowDiversityBoostFactor)
		}
	}

	// Floor: keep minimum at 0.15 unless diversity is genuinely high.
	effectiveMin := p.cfg.MinMutationRate
	if div < ac.DiversityFloorThreshold {
		effectiveMin = maxFloat(ac.MinMutationFloor, p.cfg.MinMutationRate)
	}
	p.currentMutationRate = mutation.Clamp(p.currentMutationRate, effectiveMin, p.cfg.MaxMutationRate)

	el.Debug(context.Background(), "adjustMutationRateLocked", "adjusted",
		"diversity", div,
		"mutation_rate", p.currentMutationRate,
		"effective_min", effectiveMin,
		"generation", p.Generation,
	)
}

// handleStagnationLocked checks for best-score stagnation and resets bottom
// performers if the threshold has been exceeded. Instead of copying top performers
// exactly, it injects strongly perturbed clones to introduce novel genetic material.
func (p *Population) handleStagnationLocked() {
	if p.cfg.MaxStagnantGenerations <= 0 {
		return
	}

	currentBest := p.computeBestScoreLocked()
	if currentBest > p.bestScore {
		p.bestScore = currentBest
		p.stagnantGens = 0
		return
	}

	p.stagnantGens++
	if p.stagnantGens < p.cfg.MaxStagnantGenerations {
		return
	}

	stagnantGens := p.stagnantGens
	resetCount := max(1, len(p.Agents)/3)
	resetCount = min(resetCount, len(p.Agents)-p.cfg.EliteCount)
	if resetCount <= 0 {
		p.stagnantGens = 0
		el.Warn(context.Background(), "handleStagnationLocked", "stagnation but no agents to reset",
			"population_size", len(p.Agents),
			"elite_count", p.cfg.EliteCount,
		)
		return
	}

	SortByScore(p.Agents)
	startIdx := len(p.Agents) - resetCount

	// Inject random mutations from elites instead of exact copies.
	// Each reset agent is a heavily perturbed clone of a random elite,
	// ensuring genuinely novel individuals enter the population.
	for i := startIdx; i < len(p.Agents); i++ {
		template := p.Agents[p.rng.Intn(startIdx)]
		clone := template.Clone()

		// Apply wider perturbation with selective parameter mixing.
		// 40% chance to keep a param unchanged, preserving good alleles.
		for k, v := range clone.Params {
			if p.rng.Float64() < 0.4 {
				continue // keep original value
			}
			if f, ok := v.(float64); ok {
				// Perturb by ±80%: range [0.2x, 1.8x].
				perturbation := f * (0.2 + p.rng.Float64()*1.6)
				clone.Params[k] = perturbation
			} else if iVal, ok := v.(int); ok {
				// Safe perturbation for int params: use a symmetric delta
				// range around iVal. The previous form rng.Intn(max(iVal,1)+iVal)
				// panics when iVal <= -1 because the argument becomes <= 0.
				// Use abs(iVal) as the base so the argument is always positive,
				// then shift the result to center around iVal (REVIEW #57).
				base := iVal
				if base < 0 {
					base = -base
				}
				if base == 0 {
					base = 1 // ensure non-zero argument for Intn
				}
				delta := p.rng.Intn(base*2+1) - base
				clone.Params[k] = iVal + delta
			}
		}

		clone.Score = -1
		p.Agents[i] = clone
	}

	p.stagnantGens = 0
	el.Warn(context.Background(), "handleStagnationLocked", "stagnation reset injected",
		"reset_count", resetCount,
		"stagnant_generations", stagnantGens,
		"generation", p.Generation,
	)
	p.recordRecoveryActionLocked("stagnation_reset", 1)
}

// recordRecoveryActionLocked increments the recovery action counter for the
// current generation. Caller must hold p.mu write lock.
func (p *Population) recordRecoveryActionLocked(action string, count int) {
	if p.recoveryActions == nil {
		p.recoveryActions = make(map[string]int)
	}
	p.recoveryActions[action] += count
}

// minFloat returns the smaller of two float64 values.
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// maxFloat returns the larger of two float64 values.
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
