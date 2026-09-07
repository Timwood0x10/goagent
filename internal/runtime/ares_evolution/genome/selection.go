// Package genome provides selection operators for the genetic algorithm evolution engine.
// It implements tournament selection for choosing parent strategies during evolution.
package genome

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// Selection defines the strategy for selecting individuals from a population.
//
// Implementations determine which strategies are chosen as parents for the next
// generation based on their fitness scores and the selection algorithm's bias.
type Selection interface {
	// Select chooses n individuals from the population for reproduction.
	// Higher-scoring individuals should have higher probability of being selected,
	// depending on the specific algorithm implementation.
	//
	// Args:
	//
	//	ctx - operation context (used for cancellation).
	//	population - candidate strategies to select from (must not be nil).
	//	n - number of individuals to select (must be > 0).
	//
	// Returns:
	//
	//	[]*mutation.Strategy - selected individuals for reproduction.
	//	error - non-nil if population is empty, n is invalid, or context is cancelled.
	Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error)
}

// Error definitions for selection operations are in errors.go.

// TournamentSelection selects individuals via tournament.
// Randomly pick k individuals, return the best among them. Repeat n times.
// Larger tournament sizes increase selection pressure toward higher-scoring individuals.
type TournamentSelection struct {
	tournamentSize int        // Number of competitors per tournament (default 3).
	rng            *rand.Rand // Deterministic randomness source.
}

// NewTournamentSelection creates a new tournament selector with options.
//
// Default configuration:
//   - tournamentSize: 3
//   - rng: seeded with current time (non-deterministic)
//
// Args:
//
//	opts - optional configuration functions (WithTournamentSize, WithTournamentSeed).
//
// Returns:
//
//	*TournamentSelection - the configured tournament selector instance.
//	error - non-nil if any option fails validation.
func NewTournamentSelection(opts ...TournamentOption) (*TournamentSelection, error) {
	ts := &TournamentSelection{
		tournamentSize: 3,
		rng:            rand.New(rand.NewSource(rand.Int63())), // #nosec G404 - selection doesn't need crypto rand
	}

	for _, opt := range opts {
		if err := opt(ts); err != nil {
			return nil, fmt.Errorf("apply tournament option: %w", err)
		}
	}

	if err := ts.Validate(); err != nil {
		return nil, fmt.Errorf("validate tournament selection: %w", err)
	}

	return ts, nil
}

// TournamentOption configures TournamentSelection.
type TournamentOption func(*TournamentSelection) error

// WithTournamentSize sets the number of competitors per tournament.
// Larger values increase selection pressure (bias toward high-fitness individuals).
// Minimum valid value is 2.
//
// Args:
//
//	k - number of competitors per tournament (must be >= 2).
//
// Returns:
//
//	TournamentOption - configuration function.
//	error - ErrInvalidTournamentSize if k < 2.
func WithTournamentSize(k int) TournamentOption {
	return func(ts *TournamentSelection) error {
		if k < 2 {
			return fmt.Errorf("%w: got %d", ErrInvalidTournamentSize, k)
		}
		ts.tournamentSize = k
		return nil
	}
}

// WithTournamentSeed sets the random seed for deterministic tournament selection.
// Useful for reproducible experiments and testing.
//
// Args:
//
//	seed - random seed value.
//
// Returns:
//
//	TournamentOption - configuration function.
func WithTournamentSeed(seed int64) TournamentOption {
	return func(ts *TournamentSelection) error {
		ts.rng = rand.New(rand.NewSource(seed)) // #nosec G404 - deterministic selection for reproducibility
		return nil
	}
}

// Validate checks the internal configuration state of TournamentSelection
// and returns an error if any invariant is violated. This is a defensive
// check that complements option-level validation in WithTournamentSize().
//
// Validated invariants:
//   - tournamentSize must be >= 2 (tournament requires at least 2 competitors).
//   - rng must not be nil (required for random competitor selection).
//
// Returns:
//
//	error - non-nil if configuration is invalid, nil if all invariants hold.
func (ts *TournamentSelection) Validate() error {
	if ts == nil {
		return errors.New("tournament selection instance is nil")
	}
	if ts.tournamentSize < 2 {
		return fmt.Errorf("%w: got %d", ErrInvalidTournamentSize, ts.tournamentSize)
	}
	if ts.rng == nil {
		return errors.New("tournament selection: rng must not be nil, ensure WithTournamentSeed was called or construction succeeded")
	}
	return nil
}

// Select runs n tournaments and returns the winners.
// Each tournament randomly selects k individuals and returns the one with
// the highest score. The same individual may win multiple tournaments.
//
// Args:
//
//	ctx - operation context (used for cancellation).
//	population - candidate strategies (must not be nil or empty).
//	n - number of tournaments to run (must be > 0).
//
// Returns:
//
//	[]*mutation.Strategy - n tournament winners.
//	error - non-nil if inputs are invalid or context is cancelled.
func (ts *TournamentSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}

	winners := make([]*mutation.Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return winners, fmt.Errorf("tournament %d: %w", i, ctx.Err())
		default:
		}

		winner, err := ts.runTournament(population)
		if err != nil {
			return nil, fmt.Errorf("run tournament %d: %w", i, err)
		}
		winners = append(winners, winner)
	}

	return winners, nil
}

// runTournament executes a single tournament round.
// Randomly selects k competitors and returns the highest-scoring one.
func (ts *TournamentSelection) runTournament(population []*mutation.Strategy) (*mutation.Strategy, error) {
	k := ts.tournamentSize
	if k > len(population) {
		k = len(population)
	}

	indices := ts.pickUniqueIndices(len(population), k)
	bestIdx := indices[0]
	for _, idx := range indices[1:] {
		if effectiveScore(population[idx]) > effectiveScore(population[bestIdx]) {
			bestIdx = idx
		}
	}

	el.DebugContext(context.Background(), "runTournament completed",
		"competitors", k,
		"winner_score", effectiveScore(population[bestIdx]))

	return population[bestIdx], nil
}

// pickUniqueIndices selects n unique random indices from [0, poolSize).
// Uses Fisher-Yates partial shuffle for efficiency.
func (ts *TournamentSelection) pickUniqueIndices(poolSize, n int) []int {
	indices := make([]int, poolSize)
	for i := range indices {
		indices[i] = i
	}

	for i := 0; i < n && i < poolSize; i++ {
		j := i + ts.rng.Intn(poolSize-i)
		indices[i], indices[j] = indices[j], indices[i]
	}

	if n > poolSize {
		n = poolSize
	}
	return indices[:n]
}

// SortByScore sorts a slice of strategies in descending order by score.
// Unevaluated strategies (IsScoreEvaluated() == false) go to the end of the slice.
// Uses stable sort to preserve original ordering among equal-scored strategies.
//
// Args:
//
//	strategies - strategies to sort (modified in-place, may be nil or empty).
func SortByScore(strategies []*mutation.Strategy) {
	sort.SliceStable(strategies, func(i, j int) bool {
		si := strategies[i].Score
		if strategies[i].SelectionScore != 0 {
			si = strategies[i].SelectionScore
		}
		sj := strategies[j].Score
		if strategies[j].SelectionScore != 0 {
			sj = strategies[j].SelectionScore
		}

		// Unevaluated strategies (score == -1) always sort last.
		if !IsScoreEvaluated(si) && !IsScoreEvaluated(sj) {
			return false
		}
		if !IsScoreEvaluated(si) {
			return false
		}
		if !IsScoreEvaluated(sj) {
			return true
		}

		return si > sj
	})
}

// --- Helper functions ---
// effectiveScore returns the score used for selection decisions: the
// selection-adjusted SelectionScore when set, otherwise the canonical Score.
// This lets fitness sharing (which writes SelectionScore) influence every
// selection operator, not just the sort-based ones, without polluting the
// canonical Score field used for reporting and history.
func effectiveScore(s *mutation.Strategy) float64 {
	if s.SelectionScore != 0 {
		return s.SelectionScore
	}
	return s.Score
}

// validateSelectInputs performs common input validation for all Select methods.
func validateSelectInputs(ctx context.Context, population []*mutation.Strategy, n int) error {
	if ctx == nil {
		return errors.New("context must not be nil")
	}
	if len(population) == 0 {
		return ErrSelectionEmptyPopulation
	}
	if n <= 0 {
		return fmt.Errorf("%w: got %d", ErrInvalidSelectionSize, n)
	}
	return nil
}

// findMinScore returns the minimum Score value in the population.
func findMinScore(population []*mutation.Strategy) float64 {
	min := effectiveScore(population[0])
	for i := 1; i < len(population); i++ {
		if effectiveScore(population[i]) < min {
			min = effectiveScore(population[i])
		}
	}
	return min
}

// RankSelection selects individuals using linear ranking selection.
// Individuals are sorted by score and assigned selection probability proportional
// to rank (not raw score), reducing the impact of score distribution shape
// and preventing super-individuals from dominating early generations.
type RankSelection struct {
	rng *rand.Rand
}

// NewRankSelection creates a rank-based selector.
func NewRankSelection() *RankSelection {
	return &RankSelection{
		rng: rand.New(rand.NewSource(rand.Int63())), // #nosec G404
	}
}

// Select picks n individuals with probability proportional to linear rank.
// The best individual gets the highest selection probability; the worst gets
// the lowest. Selection pressure is controlled by the rank distribution
// (linear by default).
func (rs *RankSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}

	sorted := make([]*mutation.Strategy, len(population))
	copy(sorted, population)
	SortByScore(sorted)

	// Assign linear rank weights (best = N, worst = 1).
	totalWeight := float64(len(sorted) * (len(sorted) + 1) / 2)
	weights := make([]float64, len(sorted))
	for i := range weights {
		weights[i] = float64(len(sorted) - i) // best = N, worst = 1
	}

	result := make([]*mutation.Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("rank select %d: %w", i, ctx.Err())
		default:
		}

		spin := rs.rng.Float64() * totalWeight
		cumulative := 0.0
		for j, w := range weights {
			cumulative += w
			if spin <= cumulative {
				result = append(result, sorted[j])
				break
			}
		}
	}
	return result, nil
}

// SUSSelection implements Stochastic Universal Sampling.
// Instead of independent spins, it uses a single random start + evenly spaced
// picks, ensuring minimum selection spread across the fitness distribution.
// This reduces genetic drift compared to standard roulette wheel selection.
type SUSSelection struct {
	rng *rand.Rand
}

// NewSUSSelection creates a Stochastic Universal Sampling selector.
func NewSUSSelection() *SUSSelection {
	return &SUSSelection{
		rng: rand.New(rand.NewSource(rand.Int63())), // #nosec G404
	}
}

// Select picks n individuals using SUS: one random pointer + evenly spaced picks.
// Guarantees that an individual with above-average fitness is selected at least
// floor(fitness/avg_fitness) times, reducing sampling variance.
func (sus *SUSSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}

	// Filter to evaluated strategies only.
	var scored []*mutation.Strategy
	for _, s := range population {
		if IsScoreEvaluated(s.Score) {
			scored = append(scored, s)
		}
	}
	if len(scored) == 0 {
		return nil, ErrSelectionEmptyPopulation
	}

	// Shift scores to non-negative range.
	minScore := findMinScore(scored)
	totalWeight := 0.0
	weights := make([]float64, len(scored))
	for i, s := range scored {
		w := effectiveScore(s) - minScore + 1e-9
		weights[i] = w
		totalWeight += w
	}

	if totalWeight < 1e-12 {
		// All scores equal — pick uniformly.
		result := make([]*mutation.Strategy, n)
		for i := range result {
			result[i] = scored[sus.rng.Intn(len(scored))]
		}
		return result, nil
	}

	distance := totalWeight / float64(n)
	start := sus.rng.Float64() * distance

	result := make([]*mutation.Strategy, 0, n)
	cumulative := 0.0
	idx := 0
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("sus select %d: %w", i, ctx.Err())
		default:
		}

		pointer := start + float64(i)*distance
		for cumulative < pointer && idx < len(weights) {
			cumulative += weights[idx]
			idx++
		}
		pick := idx - 1
		if pick < 0 {
			pick = 0
		}
		if pick >= len(scored) {
			pick = len(scored) - 1
		}
		result = append(result, scored[pick])
	}
	return result, nil
}

// LineageRankSelection extends rank selection with lineage diversity awareness.
// It sorts by score like rank selection, then applies a lineage diversity penalty:
// if an individual's parent lineage is overrepresented (> threshold), its effective
// rank is reduced, giving underrepresented lineages a better chance.
// This prevents lineage collapse in wired mode where tournament selection would
// otherwise increase selection pressure and collapse lineages.
type LineageRankSelection struct {
	rng              *rand.Rand
	penaltyThreshold float64 // Lineage share above this gets penalized (default 0.4).
	penaltyStrength  float64 // How much to reduce selection weight (default 0.5).
}

// LineageRankOption configures LineageRankSelection.
type LineageRankOption func(*LineageRankSelection) error

// NewLineageRankSelection creates a lineage-aware rank selector with options.
//
// Default configuration:
//   - penaltyThreshold: 0.4
//   - penaltyStrength: 0.5
//   - rng: seeded with current time (non-deterministic)
//
// Args:
//
//	opts - optional configuration functions (WithLineageRankSeed,
//	  WithLineagePenaltyThreshold, WithLineagePenaltyStrength).
//
// Returns:
//
//	*LineageRankSelection - the configured selector instance.
//	error - non-nil if any option fails validation.
func NewLineageRankSelection(opts ...LineageRankOption) (*LineageRankSelection, error) {
	ls := &LineageRankSelection{
		rng:              rand.New(rand.NewSource(rand.Int63())), // #nosec G404
		penaltyThreshold: 0.4,
		penaltyStrength:  0.5,
	}
	for _, opt := range opts {
		if err := opt(ls); err != nil {
			return nil, fmt.Errorf("apply lineage rank option: %w", err)
		}
	}
	return ls, nil
}

// WithLineageRankSeed sets the random seed for deterministic behavior.
// Useful for reproducible experiments and testing.
//
// Args:
//
//	seed - random seed value.
//
// Returns:
//
//	LineageRankOption - configuration function.
func WithLineageRankSeed(seed int64) LineageRankOption {
	return func(ls *LineageRankSelection) error {
		ls.rng = rand.New(rand.NewSource(seed)) // #nosec G404
		return nil
	}
}

// WithLineagePenaltyThreshold sets the lineage share threshold above which
// selection weight is penalized. Default 0.4.
//
// Args:
//
//	threshold - lineage share in [0, 1] that triggers penalty (must be in [0, 1]).
//
// Returns:
//
//	LineageRankOption - configuration function.
//	error - non-nil if threshold is out of range.
func WithLineagePenaltyThreshold(threshold float64) LineageRankOption {
	return func(ls *LineageRankSelection) error {
		if threshold < 0 || threshold > 1 {
			return fmt.Errorf("lineage penalty threshold must be in [0, 1], got %f", threshold)
		}
		ls.penaltyThreshold = threshold
		return nil
	}
}

// WithLineagePenaltyStrength sets how much to reduce selection weight for
// overrepresented lineages. Default 0.5.
//
// Args:
//
//	strength - penalty multiplier in [0, 1] (must be in [0, 1]).
//
// Returns:
//
//	LineageRankOption - configuration function.
//	error - non-nil if strength is out of range.
func WithLineagePenaltyStrength(strength float64) LineageRankOption {
	return func(ls *LineageRankSelection) error {
		if strength < 0 || strength > 1 {
			return fmt.Errorf("lineage penalty strength must be in [0, 1], got %f", strength)
		}
		ls.penaltyStrength = strength
		return nil
	}
}

// Select picks n individuals using lineage-aware rank weighting.
// First sorts by score descending, then computes lineage distribution across
// the population. For each individual, if its parent lineage share exceeds
// the penalty threshold, its effective rank weight is reduced proportionally.
// This penalizes individuals from already-dominant lineages, giving
// underrepresented lineages a better chance of selection.
func (ls *LineageRankSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}

	sorted := make([]*mutation.Strategy, len(population))
	copy(sorted, population)
	SortByScore(sorted)

	weights, totalWeight := computeLineageRankWeights(sorted, ls.penaltyThreshold, ls.penaltyStrength)

	if totalWeight < 1e-12 {
		// All weights zero — pick uniformly.
		result := make([]*mutation.Strategy, n)
		for i := range result {
			result[i] = sorted[ls.rng.Intn(len(sorted))]
		}
		return result, nil
	}

	result := make([]*mutation.Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("lineage rank select %d: %w", i, ctx.Err())
		default:
		}

		spin := ls.rng.Float64() * totalWeight
		cumulative := 0.0
		for j, w := range weights {
			cumulative += w
			if spin <= cumulative {
				result = append(result, sorted[j])
				break
			}
		}
	}
	return result, nil
}

// computeLineageRankWeights computes the effective selection weights for the
// population after applying lineage diversity penalty. The input slice MUST
// be pre-sorted by SortByScore (descending score). The returned weights slice
// is parallel to the input slice: weights[i] is the effective weight for
// sorted[i]. totalWeight is the sum of all weights for use in roulette-style
// selection.
//
// Weight assignment:
//   - Base rank weight: best (index 0) gets N, worst gets 1, where N = len(sorted).
//   - For each individual, if its lineage share (count / total) exceeds the
//     penalty threshold, the rank weight is reduced proportionally to the
//     overshoot beyond the threshold.
//   - Lineage is identified by ParentID; empty ParentID is treated as "(root)".
//
// Args:
//
//	sorted - population pre-sorted by SortByScore descending (must not be empty).
//	penaltyThreshold - lineage share in [0, 1] above which penalty applies.
//	penaltyStrength - penalty multiplier in [0, 1] (fraction of weight to remove).
//
// Returns:
//
//	weights - effective weight per individual (parallel to sorted).
//	totalWeight - sum of weights (always >= 0; 0 only if all weights collapse).
func computeLineageRankWeights(sorted []*mutation.Strategy, penaltyThreshold, penaltyStrength float64) (weights []float64, totalWeight float64) {
	// Count lineage distribution across the sorted population.
	lineageCount := make(map[string]int)
	for _, s := range sorted {
		pid := s.ParentID
		if pid == "" {
			pid = "(root)"
		}
		lineageCount[pid]++
	}
	total := float64(len(sorted))

	// Compute effective weights with lineage penalty applied.
	weights = make([]float64, len(sorted))
	totalWeight = 0.0
	for i, s := range sorted {
		pid := s.ParentID
		if pid == "" {
			pid = "(root)"
		}
		share := float64(lineageCount[pid]) / total
		// Rank weight: best (index 0) gets N, worst gets 1.
		rankWeight := float64(len(sorted) - i)

		if share > penaltyThreshold {
			// Apply lineage penalty proportional to overshoot.
			excess := (share - penaltyThreshold) / (1.0 - penaltyThreshold)
			penalty := penaltyStrength * excess
			rankWeight *= (1.0 - penalty)
		}
		weights[i] = rankWeight
		totalWeight += rankWeight
	}
	return weights, totalWeight
}

// RouletteWheelSelection selects individuals with probability proportional to fitness.
// Higher-scoring individuals have proportionally higher selection probability.
// Note: this is sensitive to score distribution — a super-individual can dominate.
type RouletteWheelSelection struct {
	rng *rand.Rand
}

// NewRouletteWheelSelection creates a fitness-proportional selector.
func NewRouletteWheelSelection(opts ...RouletteOption) (*RouletteWheelSelection, error) {
	rw := &RouletteWheelSelection{
		rng: rand.New(rand.NewSource(rand.Int63())), // #nosec G404
	}
	for _, opt := range opts {
		if err := opt(rw); err != nil {
			return nil, fmt.Errorf("apply roulette option: %w", err)
		}
	}
	return rw, nil
}

// RouletteOption configures RouletteWheelSelection.
type RouletteOption func(*RouletteWheelSelection) error

// WithRouletteSeed sets the random seed for deterministic selection.
func WithRouletteSeed(seed int64) RouletteOption {
	return func(rw *RouletteWheelSelection) error {
		rw.rng = rand.New(rand.NewSource(seed)) // #nosec G404
		return nil
	}
}

// Select picks n individuals with probability proportional to shifted fitness.
// Scores are shifted to be non-negative. Returns error on empty population.
func (rw *RouletteWheelSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}

	var scored []*mutation.Strategy
	for _, s := range population {
		if IsScoreEvaluated(s.Score) {
			scored = append(scored, s)
		}
	}
	if len(scored) == 0 {
		return nil, ErrSelectionEmptyPopulation
	}

	minScore := findMinScore(scored)
	weights := make([]float64, len(scored))
	totalWeight := 0.0
	for i, s := range scored {
		w := effectiveScore(s) - minScore + 1e-9
		weights[i] = w
		totalWeight += w
	}

	result := make([]*mutation.Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return result, fmt.Errorf("roulette select %d: %w", i, ctx.Err())
		default:
		}

		spin := rw.rng.Float64() * totalWeight
		cumulative := 0.0
		for j, w := range weights {
			cumulative += w
			if spin <= cumulative {
				result = append(result, scored[j])
				break
			}
		}
	}
	return result, nil
}

// TruncationSelection selects the top n individuals by score (elite selection).
// This is a deterministic selector useful for baseline comparisons and
// hybrid strategies where strict elite preservation is needed before applying
// other selection pressure.
type TruncationSelection struct{}

// NewTruncationSelection creates a truncation-based selector.
func NewTruncationSelection() *TruncationSelection {
	return &TruncationSelection{}
}

// Select returns the top n highest-scoring individuals. If n exceeds population
// size, returns the full population. Unevaluated individuals sort last.
func (ts *TruncationSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if err := validateSelectInputs(ctx, population, n); err != nil {
		return nil, err
	}
	sorted := make([]*mutation.Strategy, len(population))
	copy(sorted, population)
	SortByScore(sorted)
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n], nil
}

// PickParent selects a single parent from the population using the given selector and RNG.
func PickParent(ctx context.Context, population []*mutation.Strategy, sel Selection, rng *rand.Rand) (*mutation.Strategy, error) {
	if sel == nil {
		var err error
		sel, err = NewTournamentSelection()
		if err != nil {
			return nil, fmt.Errorf("create default tournament selector: %w", err)
		}
	}
	selected, err := sel.Select(ctx, population, 1)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, errors.New("no parent selected")
	}
	// Apply RNG-based index selection for reproducibility in tests.
	idx := rng.Intn(len(selected))
	return selected[idx], nil
}

// ── NSGA-II Nondominated Sorting Selection ─────

// NondominatedSortingSelection implements NSGA-II's selection strategy:
// non-dominated sorting (Pareto rank) + crowding distance as tiebreaker.
// Selects individuals from the best fronts first; within the same front,
// those with larger crowding distance (more isolated) are preferred.
//
// This selection requires DimensionScores to be set on each strategy.
// If no DimensionScores are available, it falls back to single-objective
// tournament selection.
type NondominatedSortingSelection struct {
	rng *rand.Rand
}

// NewNondominatedSortingSelection creates an NSGA-II selection operator.
//
// Args:
//
//	seed - random seed for deterministic behavior in tests.
//
// Returns:
//
//	*NondominatedSortingSelection - the initialized selector.
func NewNondominatedSortingSelection(seed int64) *NondominatedSortingSelection {
	return &NondominatedSortingSelection{
		rng: rand.New(rand.NewSource(seed)), // #nosec G404
	}
}

// Select implements the Selection interface using NSGA-II's crowded tournament.
// It first partitions the population into Pareto fronts, then selects from the
// best fronts. Within the same front, individuals with higher crowding distance
// (more diversity) are preferred.
func (s *NondominatedSortingSelection) Select(ctx context.Context, population []*mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if len(population) == 0 {
		return nil, errors.New("empty population for NSGA-II selection")
	}
	if n <= 0 {
		return nil, fmt.Errorf("invalid selection count: %d", n)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Check if multi-objective data is available.
	hasMultiObjective := false
	for _, p := range population {
		if len(p.DimensionScores) > 0 {
			hasMultiObjective = true
			break
		}
	}

	if !hasMultiObjective {
		// Fall back to tournament selection for single-objective cases.
		ts, err := NewTournamentSelection(WithTournamentSeed(s.rng.Int63()))
		if err != nil {
			return nil, fmt.Errorf("nsga2 fallback tournament: %w", err)
		}
		return ts.Select(ctx, population, n)
	}

	// Compute Pareto ranks for all individuals.
	ranks := ParetoRank(population)

	// Find the maximum rank.
	maxRank := 0
	for _, r := range ranks {
		if r > maxRank {
			maxRank = r
		}
	}

	// Group individuals by front (rank).
	fronts := make([][]*mutation.Strategy, maxRank+1)
	for i, agent := range population {
		fronts[ranks[i]] = append(fronts[ranks[i]], agent)
	}

	// Select from fronts in order.
	result := make([]*mutation.Strategy, 0, n)
	for _, front := range fronts {
		if len(result)+len(front) <= n {
			result = append(result, front...)
			continue
		}
		// Need to select a subset of this front.
		// NSGA-II computes crowding distance WITHIN the front (not across the
		// whole population): boundary points of the front get infinite distance,
		// and normalization uses the front-local min/max. Computing it globally
		// distorts the tiebreak because interior points of this front would be
		// normalized against other fronts' ranges.
		remaining := n - len(result)
		frontCD := CrowdingDistance(front)
		// Sort an index permutation, not `front` directly: sorting `front` in
		// place while the less-func indexes the parallel `frontCD` slice
		// scrambles the pairing after the first swap (classic parallel-array
		// sort bug). Order indices by descending crowding distance, then
		// materialize the top `remaining` in that order.
		order := make([]int, len(front))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			return frontCD[order[a]] > frontCD[order[b]]
		})
		for _, idx := range order[:remaining] {
			result = append(result, front[idx])
		}
		break
	}

	return result, nil
}
