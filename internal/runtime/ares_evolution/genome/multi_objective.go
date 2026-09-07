package genome

import (
	"context"
	"math"
	"sort"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// DimDirection indicates whether a dimension should be maximized or minimized.
type DimDirection int

const (
	// Maximize means higher values are better (default).
	Maximize DimDirection = iota
	// Minimize means lower values are better.
	Minimize
)

// DefaultDimensionDirections maps dimension names to their optimization direction.
// cost and latency are minimized; success_rate and quality are maximized.
var DefaultDimensionDirections = map[string]DimDirection{
	"success_rate": Maximize,
	"quality":      Maximize,
	"cost":         Minimize,
	"latency":      Minimize,
}

// DefaultDimensionWeights are the default weights for aggregating multi-objective
// fitness dimensions into a single Score. These should be calibrated per domain.
// Keys: dimension name, Values: weight (must sum to 1.0).
var DefaultDimensionWeights = map[string]float64{
	"success_rate": 0.40,
	"quality":      0.25,
	"cost":         0.20,
	"latency":      0.15,
}

// ParetoDominance returns true if a Pareto-dominates b across all dimensions.
// a dominates b iff a is strictly better in at least one dimension and
// no worse in all others. Dimension direction (maximize/minimize) is respected.
func ParetoDominance(a, b *mutation.Strategy) bool {
	if a.DimensionScores == nil || b.DimensionScores == nil {
		if (a.DimensionScores == nil) != (b.DimensionScores == nil) {
			el.WarnContext(context.Background(), "ParetoDominance: mixed multi/single-objective strategies, falling back to Score",
				"a_dim", a.DimensionScores != nil, "b_dim", b.DimensionScores != nil,
				"a_score", a.Score, "b_score", b.Score,
			)
		}
		return a.Score > b.Score
	}

	better := false
	for k, va := range a.DimensionScores {
		vb, ok := b.DimensionScores[k]
		if !ok {
			continue
		}
		dir := dimensionDirection(k)
		// For maximize: a > b is better; for minimize: a < b is better.
		if dir == Maximize {
			if va < vb {
				return false
			}
			if va > vb {
				better = true
			}
		} else {
			if va > vb {
				return false
			}
			if va < vb {
				better = true
			}
		}
	}
	return better
}

// ParetoFront returns the subset of strategies that are not dominated by any other.
// Rank 0 = Pareto-optimal front.
func ParetoFront(strategies []*mutation.Strategy) []*mutation.Strategy {
	n := len(strategies)
	if n == 0 {
		return nil
	}
	dominated := make([]bool, n)
	for i := 0; i < n; i++ {
		if dominated[i] {
			continue
		}
		for j := i + 1; j < n; j++ {
			if dominated[j] {
				continue
			}
			//nolint:gosec // G602: Index bounds are guaranteed by loop conditions i < n and j < n
			if ParetoDominance(strategies[i], strategies[j]) {
				dominated[j] = true
			} else if ParetoDominance(strategies[j], strategies[i]) { //nolint:gosec // G602: bounds guaranteed
				dominated[i] = true
				break
			}
		}
	}
	front := make([]*mutation.Strategy, 0, n)
	for i, s := range strategies {
		if !dominated[i] {
			front = append(front, s)
		}
	}
	return front
}

// ParetoRank assigns each strategy a Pareto rank (0 = best front).
// Uses non-dominated sorting (NSGA-II style).
func ParetoRank(strategies []*mutation.Strategy) []int {
	n := len(strategies)
	ranks := make([]int, n)
	if n == 0 {
		return ranks
	}
	remaining := make([]int, n)
	for i := range remaining {
		remaining[i] = i
	}
	rank := 0
	for len(remaining) > 0 {
		// Find Pareto front among remaining.
		candidates := make([]*mutation.Strategy, len(remaining))
		for i, idx := range remaining {
			candidates[i] = strategies[idx]
		}
		front := ParetoFront(candidates)
		frontSet := make(map[*mutation.Strategy]struct{}, len(front))
		for _, s := range front {
			frontSet[s] = struct{}{}
		}
		// Assign rank and filter.
		var next []int
		for _, idx := range remaining {
			if _, ok := frontSet[strategies[idx]]; ok {
				ranks[idx] = rank
			} else {
				next = append(next, idx)
			}
		}
		remaining = next
		rank++
	}
	return ranks
}

// CrowdingDistance computes the NSGA-II crowding distance for each strategy
// in a Pareto front subset. Higher values = more isolated in objective space.
func CrowdingDistance(strategies []*mutation.Strategy) []float64 {
	n := len(strategies)
	dist := make([]float64, n)
	if n < 3 {
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		return dist
	}

	// Collect all dimension names present across strategies.
	dims := collectDimensionNames(strategies)
	if len(dims) == 0 {
		return dist
	}

	for _, dim := range dims {
		// Sort by this dimension.
		sorted := make([]int, n)
		for i := range sorted {
			sorted[i] = i
		}
		sort.Slice(sorted, func(i, j int) bool {
			vi := strategies[sorted[i]].DimensionScores[dim]
			vj := strategies[sorted[j]].DimensionScores[dim]
			return vi < vj
		})
		// Boundary points get infinite distance.
		dist[sorted[0]] = math.Inf(1)
		dist[sorted[n-1]] = math.Inf(1)
		// Interior points get normalized distance.
		minVal := strategies[sorted[0]].DimensionScores[dim]
		maxVal := strategies[sorted[n-1]].DimensionScores[dim]
		norm := maxVal - minVal
		if norm < 1e-10 {
			continue
		}
		for i := 1; i < n-1; i++ {
			idx := sorted[i]
			prev := strategies[sorted[i-1]].DimensionScores[dim]
			next := strategies[sorted[i+1]].DimensionScores[dim]
			dist[idx] += (next - prev) / norm
		}
	}
	return dist
}

// AggregateDimensions computes a weighted sum of dimension scores.
// Minimized dimensions are inverted (1 - normalized) so higher is always better.
// Uses DefaultDimensionWeights for dimensions not in the provided weights.
func AggregateDimensions(dims map[string]float64, weights map[string]float64) float64 {
	if len(dims) == 0 {
		return 0
	}
	w := mergeWeights(weights)
	var total float64
	for k, v := range dims {
		val := v
		// Invert minimized dimensions so higher is always better in the aggregate.
		if dimensionDirection(k) == Minimize {
			val = -val
		}
		total += val * w[k]
	}
	return total
}

// dimensionDirection returns the optimization direction for a dimension.
// Defaults to Maximize if the dimension is not in the default directions map.
func dimensionDirection(name string) DimDirection {
	if dir, ok := DefaultDimensionDirections[name]; ok {
		return dir
	}
	return Maximize
}

// NormalizeDimensions normalizes dimension values to [0, 1] range using given bounds.
// bounds maps dimension name to [min, max]. Unknown dimensions are passed through.
func NormalizeDimensions(dims map[string]float64, bounds map[string][2]float64) map[string]float64 {
	result := make(map[string]float64, len(dims))
	for k, v := range dims {
		b, ok := bounds[k]
		if !ok || b[1]-b[0] < 1e-10 {
			result[k] = v
			continue
		}
		result[k] = mutation.Clamp((v-b[0])/(b[1]-b[0]), 0, 1)
	}
	return result
}

// collectDimensionNames returns sorted unique dimension names.
func collectDimensionNames(strategies []*mutation.Strategy) []string {
	seen := make(map[string]struct{})
	for _, s := range strategies {
		if s.DimensionScores == nil {
			continue
		}
		for k := range s.DimensionScores {
			seen[k] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// mergeWeights returns a complete weight map starting with DefaultDimensionWeights,
// then overlays caller-supplied weights on top. This ensures all dimensions
// always have a weight value for scoring.
func mergeWeights(weights map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(DefaultDimensionWeights))
	for k, v := range DefaultDimensionWeights {
		result[k] = v
	}
	for k, v := range weights {
		result[k] = v
	}
	return result
}

// DefaultDimensionBounds returns typical [min, max] bounds for known dimensions.
// Used by NormalizeDimensions when no caller-provided bounds exist.
func DefaultDimensionBounds() map[string][2]float64 {
	return map[string][2]float64{
		"success_rate": {0, 100},
		"quality":      {0, 100},
		"cost":         {0, 100},
		"latency":      {0, 100},
	}
}
