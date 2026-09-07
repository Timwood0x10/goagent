package arena

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// maxParallelRuns limits concurrent Scorer.Score calls in runStrategy.
const maxParallelRuns = 15

// Common errors for regression testing.
var (
	ErrNilArena        = errors.New("arena: arena service is nil")
	ErrNilScorer       = errors.New("arena: scorer is nil")
	ErrNilStrategy     = errors.New("arena: strategy is nil")
	ErrInvalidRuns     = errors.New("arena: number of runs must be positive")
	ErrConfidenceRange = errors.New("arena: confidence level must be between 0 and 1")
)

// RegressionResult holds the comparison result between two strategies.
type RegressionResult struct {
	OldStrategyID string    // old strategy identifier
	NewStrategyID string    // new strategy identifier
	OldAvg        float64   // average score of old strategy runs
	NewAvg        float64   // average score of new strategy runs
	OldScores     []float64 // individual run scores for old strategy
	NewScores     []float64 // individual run scores for new strategy
	WinRate       float64   // fraction where new >= old (0.0 to 1.0)
	Confident     bool      // statistically significant (p < 0.05 via Welch's t-test)
	NewBetter     bool      // win rate at or above the configured MinWinRate
	Samples       int       // number of sample runs per strategy actually scored
	PValue        float64   // computed p-value from statistical test
	TestedAt      time.Time // when this regression was run
}

// TestCaseInput wraps a strategy with its test case context for scoring.
// When RegressionConfig.TestCases is set, the Scorer receives this instead
// of the raw strategy. Scorers should type-assert input to TestCaseInput
// to access both the strategy and the specific test case.
type TestCaseInput struct {
	Strategy any
	TestCase any
	Index    int // iteration index, shared between baseline and compare
}

// RegressionConfig configures a regression test between two strategies.
type RegressionConfig struct {
	OldStrategy  any     // old strategy (baseline)
	NewStrategy  any     // new strategy (strategy under test)
	BaselineRuns int     // number of runs for baseline (old) strategy, default 5
	CompareRuns  int     // number of runs for new strategy, default 5
	TestSuite    string  // test suite name or identifier
	Confidence   float64 // significance level for statistical test, default 0.05
	MinWinRate   float64 // minimum win rate to consider new strategy better, default 0.55
	// MinAdaptiveRuns is the minimum runs before early stopping kicks in (adaptive mode only, default 10).
	// Adaptive mode scores in batches and stops early when statistical significance is reached.
	MinAdaptiveRuns int
	// AdaptiveBatchSize is the number of scores to collect per batch in adaptive mode, default 5.
	AdaptiveBatchSize int
	// MaxAdaptiveRuns overrides BaselineRuns/CompareRuns as the upper bound for
	// adaptive mode, default 0 (use BaselineRuns/CompareRuns).
	MaxAdaptiveRuns int
	// TestCases provides a fixed list of test cases for paired scoring.
	// When set, iteration i of both baseline and compare receives TestCaseInput
	// with the same TestCase and Index, ensuring fair paired comparison.
	// Length should cover max(BaselineRuns, CompareRuns).
	TestCases []any
}

// DefaultRegressionConfig returns a RegressionConfig with sensible defaults.
func DefaultRegressionConfig() RegressionConfig {
	return RegressionConfig{
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
		MinWinRate:   0.55,
	}
}

// Scorer defines how to score a single strategy or its execution result.
type Scorer interface {
	// Score evaluates a strategy and returns a numeric score.
	// The input can be a strategy object, execution result, or any relevant data.
	// The context allows slow (e.g., LLM-backed) scorers to observe
	// cancellation and timeout instead of hanging the caller (M4).
	Score(ctx context.Context, input any) (float64, error)
}

// BatchScorer optionally collapses many scoring calls into one request. When a
// RegressionTester's scorer implements this interface, runStrategy uses it to
// score all runs of a strategy in a single call — collapsing many LLM
// round-trips into one, which is critical for rate-limited providers (e.g.
// low-rpm remote APIs). ScoreBatch must return exactly count scores.
type BatchScorer interface {
	Scorer
	// ScoreBatch scores count runs of strategy over testCases (cycling when the
	// suite is shorter than count) and returns exactly count scores.
	ScoreBatch(ctx context.Context, strategy any, count int, testCases []any) ([]float64, error)
}

// RegressionTester performs A/B style comparison tests on strategies.
type RegressionTester struct {
	arena  *Service
	scorer Scorer

	// fingerprint computes a stable environment key from a config; nil disables
	// result caching. When set, Run returns the cached result for an unchanged
	// environment instead of re-running the expensive scoring (primitive 4:
	// environment fingerprint — skip re-runs when the environment is unchanged).
	fingerprint func(RegressionConfig) string
	// resultCache memoizes RegressionResult by fingerprint.
	resultCache map[string]*RegressionResult
	// cacheOrder tracks fingerprint insertion order so the cache can be capped
	// (oldest entries evicted first) instead of growing without bound.
	cacheOrder []string
	// cacheMu guards resultCache and cacheOrder (fingerprint itself is set once
	// at construction).
	cacheMu sync.Mutex
}

// maxCacheEntries caps the fingerprint result cache so a long-running service
// with many distinct environments does not grow without bound.
const maxCacheEntries = 128

// RegressionTesterOption configures a RegressionTester.
type RegressionTesterOption func(*RegressionTester)

// WithFingerprint enables result caching keyed by the provided fingerprint
// function. When Run is called with a config whose fingerprint matches a
// previously completed run, the cached result is returned without re-running
// the scorer. The caller is responsible for producing a stable key that
// covers everything that affects the outcome (candidate ID, baseline ID, run
// counts, test cases, configuration).
func WithFingerprint(fn func(RegressionConfig) string) RegressionTesterOption {
	return func(rt *RegressionTester) {
		rt.fingerprint = fn
		rt.resultCache = make(map[string]*RegressionResult)
		rt.cacheOrder = make([]string, 0, maxCacheEntries)
	}
}

// NewRegressionTester creates a new regression tester.
// Args:
//   - arena: arena service for running scenarios, must not be nil.
//   - scorer: scoring function interface, must not be nil.
//
// Returns:
//   - *RegressionTester: the configured tester.
//   - error: ErrNilArena or ErrNilScorer if arguments are nil.
func NewRegressionTester(arena *Service, scorer Scorer, opts ...RegressionTesterOption) (*RegressionTester, error) {
	if arena == nil {
		return nil, ErrNilArena
	}
	if scorer == nil {
		return nil, ErrNilScorer
	}
	rt := &RegressionTester{
		arena:  arena,
		scorer: scorer,
	}
	for _, opt := range opts {
		opt(rt)
	}
	return rt, nil
}

// NewRegressionTesterWithScorer creates a regression tester that only requires a
// scorer and no arena Service. The regression run path (runStrategy +
// significance test) never touches the arena, so a scorer-only tester is safe
// for contexts where no live runtime is available — e.g. the evolution
// candidate gate 3 preserved-case check. When a scorer is nil, ErrNilScorer is
// returned.
func NewRegressionTesterWithScorer(scorer Scorer, opts ...RegressionTesterOption) (*RegressionTester, error) {
	if scorer == nil {
		return nil, ErrNilScorer
	}
	rt := &RegressionTester{
		scorer: scorer,
	}
	for _, opt := range opts {
		opt(rt)
	}
	return rt, nil
}

// Run executes the regression test and returns comparison results.
// It runs oldStrategy baselineRuns times and newStrategy compareRuns times,
// then computes statistical significance using Welch's t-test approximation.
//
// When cfg.AdaptiveBatchSize > 0, runs in batches with early stopping:
//   - Scores are collected in batches of AdaptiveBatchSize for both strategies
//   - After each batch, Welch's t-test is computed
//   - Stops early if p < confidence (winner found) or if p > 0.5 after MinAdaptiveRuns
//   - Caps at MaxAdaptiveRuns if set, otherwise at BaselineRuns/CompareRuns
//
// Args:
//   - ctx: context for cancellation and timeout.
//   - cfg: configuration for the regression test.
//
// Returns:
//   - *RegressionResult: detailed comparison results.
//   - error: validation error, context cancellation, or scoring failure.
func (rt *RegressionTester) Run(ctx context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	// Validate configuration.
	if err := validateRegressionConfig(cfg); err != nil {
		return nil, err
	}

	// Environment fingerprint cache (primitive 4): if a fingerprint function is
	// configured and this environment (candidate/baseline/test-cases) was
	// already evaluated, return the memoized result instead of re-running the
	// expensive scoring — skip re-runs when the environment is unchanged.
	if rt.fingerprint != nil {
		key := rt.fingerprint(cfg)
		rt.cacheMu.Lock()
		cached, ok := rt.resultCache[key]
		rt.cacheMu.Unlock()
		if ok && cached != nil {
			return cached, nil
		}
		result, err := rt.runUncached(ctx, cfg)
		if err == nil && result != nil {
			rt.cacheMu.Lock()
			rt.resultCache[key] = result
			rt.cacheOrder = append(rt.cacheOrder, key)
			// Evict oldest entries once the cache exceeds its cap so it cannot
			// grow without bound in a long-running service.
			if len(rt.cacheOrder) > maxCacheEntries {
				evict := len(rt.cacheOrder) - maxCacheEntries
				for _, k := range rt.cacheOrder[:evict] {
					delete(rt.resultCache, k)
				}
				rt.cacheOrder = append([]string(nil), rt.cacheOrder[evict:]...)
			}
			rt.cacheMu.Unlock()
		}
		return result, err
	}
	return rt.runUncached(ctx, cfg)
}

// runUncached executes the regression test without consulting the fingerprint
// cache. It is the body of Run; the cache-aware wrapper lives in Run.
func (rt *RegressionTester) runUncached(ctx context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	// Apply defaults where needed.
	if cfg.BaselineRuns <= 0 {
		cfg.BaselineRuns = 5
	}
	if cfg.CompareRuns <= 0 {
		cfg.CompareRuns = 5
	}
	if cfg.Confidence <= 0 {
		cfg.Confidence = 0.05
	}
	if cfg.MinWinRate <= 0 {
		cfg.MinWinRate = 0.55
	}
	if cfg.MinAdaptiveRuns <= 0 {
		cfg.MinAdaptiveRuns = 10
	}
	if cfg.AdaptiveBatchSize <= 0 {
		cfg.AdaptiveBatchSize = 5
	}
	if cfg.AdaptiveBatchSize >= cfg.BaselineRuns && cfg.AdaptiveBatchSize >= cfg.CompareRuns {
		// Batch size covers all runs, no adaptive benefit; fall through to standard mode.
		cfg.AdaptiveBatchSize = 0
	}
	if cfg.MaxAdaptiveRuns <= 0 {
		cfg.MaxAdaptiveRuns = cfg.BaselineRuns
		if cfg.CompareRuns > cfg.MaxAdaptiveRuns {
			cfg.MaxAdaptiveRuns = cfg.CompareRuns
		}
	}

	// Adaptive batched mode with early stopping.
	if cfg.AdaptiveBatchSize > 0 && cfg.MaxAdaptiveRuns > 0 {
		return rt.runAdaptive(ctx, cfg)
	}

	// Pre-sample test cases for paired scoring.
	testCases := cfg.TestCases
	if len(testCases) == 0 {
		// Generate a deterministic sequence of nil test cases so both strategies
		// at least agree on the iteration index (Index field in TestCaseInput).
		totalRuns := cfg.BaselineRuns
		if cfg.CompareRuns > totalRuns {
			totalRuns = cfg.CompareRuns
		}
		testCases = make([]any, totalRuns)
	}

	// Standard mode: run both strategies concurrently using errgroup.
	var oldScores, newScores []float64
	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		scores, err := rt.runStrategy(gCtx, cfg.OldStrategy, cfg.BaselineRuns, testCases)
		if err != nil {
			return fmt.Errorf("arena: run old strategy: %w", err)
		}
		oldScores = scores
		return nil
	})

	g.Go(func() error {
		scores, err := rt.runStrategy(gCtx, cfg.NewStrategy, cfg.CompareRuns, testCases)
		if err != nil {
			return fmt.Errorf("arena: run new strategy: %w", err)
		}
		newScores = scores
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Build result with statistical analysis.
	result := rt.buildResult(cfg, oldScores, newScores)
	return result, nil
}

// runAdaptive runs regression in batches with early stopping via sequential testing.
// Both strategies are scored in parallel batches. After each batch, significance is
// computed. Stops early if the outcome is already clear.
func (rt *RegressionTester) runAdaptive(ctx context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	var oldScores, newScores []float64
	maxRuns := cfg.MaxAdaptiveRuns
	batchSize := cfg.AdaptiveBatchSize
	minRuns := cfg.MinAdaptiveRuns

	// Pre-sample test cases for paired scoring across all batches.
	testCases := cfg.TestCases
	if len(testCases) == 0 {
		testCases = make([]any, maxRuns)
	}

	for len(oldScores) < maxRuns || len(newScores) < maxRuns {
		// Determine next batch size (last batch may be smaller).
		offset := len(oldScores)
		if len(newScores) < offset {
			offset = len(newScores)
		}
		oldRemaining := maxRuns - len(oldScores)
		newRemaining := maxRuns - len(newScores)
		thisBatch := batchSize
		if oldRemaining < thisBatch {
			thisBatch = oldRemaining
		}
		if newRemaining < thisBatch {
			thisBatch = newRemaining
		}
		if thisBatch <= 0 {
			break
		}

		// Both strategies score the same test case slice (paired by index).
		// Keep the slice in bounds: when offset exceeds the test case count,
		// wrap around so runs can cycle through a suite shorter than maxRuns.
		if len(testCases) == 0 {
			break
		}
		caseOffset := offset % len(testCases)
		avail := len(testCases) - caseOffset
		if thisBatch > avail {
			thisBatch = avail
		}
		if thisBatch <= 0 {
			break
		}
		batchTestCases := testCases[caseOffset : caseOffset+thisBatch]

		// Run one batch for both strategies in parallel.
		var batchOld, batchNew []float64
		g, gCtx := errgroup.WithContext(ctx)

		g.Go(func() error {
			scores, err := rt.runStrategy(gCtx, cfg.OldStrategy, thisBatch, batchTestCases)
			if err != nil {
				return fmt.Errorf("arena: run old strategy: %w", err)
			}
			batchOld = scores
			return nil
		})

		g.Go(func() error {
			scores, err := rt.runStrategy(gCtx, cfg.NewStrategy, thisBatch, batchTestCases)
			if err != nil {
				return fmt.Errorf("arena: run new strategy: %w", err)
			}
			batchNew = scores
			return nil
		})

		if err := g.Wait(); err != nil {
			return nil, err
		}

		oldScores = append(oldScores, batchOld...)
		newScores = append(newScores, batchNew...)

		// Check for early stopping after reaching minimum runs.
		n := len(oldScores)
		if len(newScores) < n {
			n = len(newScores)
		}
		if n >= minRuns && n >= 2 {
			_, pVal := computeSignificance(oldScores[:n], newScores[:n], cfg.Confidence)
			// Stop if significant (p < confidence) or hopeless (p > 0.5).
			if pVal < cfg.Confidence || pVal > 0.5 {
				break
			}
		}
	}

	// Trim to equal length for win rate calculation.
	n := len(oldScores)
	if len(newScores) < n {
		n = len(newScores)
	}

	result := rt.buildResult(cfg, oldScores[:n], newScores[:n])
	return result, nil
}

// runStrategy executes a single strategy multiple times and collects scores.
// Each execution is scored via the configured Scorer interface.
// When testCases is provided, the Scorer receives TestCaseInput wrapping both
// the strategy and the specific test case for that iteration.
// Scoring runs in parallel with bounded concurrency (maxParallelRuns) to
// accelerate LLM-based scorers.
func (rt *RegressionTester) runStrategy(ctx context.Context, strategy any, n int, testCases []any) ([]float64, error) {
	if strategy == nil {
		return nil, ErrNilStrategy
	}
	if n <= 0 {
		return nil, ErrInvalidRuns
	}

	// Batch path: when the scorer implements BatchScorer, collapse all n runs
	// into a single call (one LLM round-trip instead of n), respecting the
	// context for cancellation and timeout.
	if bs, ok := rt.scorer.(BatchScorer); ok {
		batchScores, err := bs.ScoreBatch(ctx, strategy, n, testCases)
		if err != nil {
			return nil, fmt.Errorf("arena: batch score strategy: %w", err)
		}
		if len(batchScores) != n {
			return nil, fmt.Errorf("arena: batch scorer returned %d scores, want %d", len(batchScores), n)
		}
		return batchScores, nil
	}

	scores := make([]float64, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelRuns)
	errOnce := sync.Once{}
	var runErr error
	completed := 0 // number of slots actually scored (guarded by mu)

	// Derive a cancellable child context so goroutines can check cancellation.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	for i := 0; i < n; i++ {
		if err := runCtx.Err(); err != nil {
			return nil, err
		}

		sem <- struct{}{}
		wg.Add(1)

		i := i
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()

			if err := runCtx.Err(); err != nil {
				return
			}

			var input = strategy
			if len(testCases) > 0 {
				input = TestCaseInput{
					Strategy: strategy,
					TestCase: testCases[i%len(testCases)],
					Index:    i,
				}
			}

			score, err := rt.scorer.Score(runCtx, input)
			if err != nil {
				errOnce.Do(func() {
					runErr = fmt.Errorf("arena: score run %d: %w", i, err)
					runCancel() // cancel remaining goroutines on first error
				})
				return
			}
			mu.Lock()
			scores[i] = score
			completed++
			mu.Unlock()
		}()
	}
	wg.Wait()

	// If context was cancelled during parallel scoring, prefer that error.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runErr != nil {
		return nil, runErr
	}
	// A cancellation may have raced between the scorer returning and the slot
	// write, leaving some slots unfilled. Never return a 0-filled partial
	// result as if it were complete.
	if completed != n {
		return nil, fmt.Errorf("arena: incomplete score set: scored %d/%d runs", completed, n)
	}
	return scores, nil
}

// buildResult constructs the final RegressionResult from collected scores.
func (rt *RegressionTester) buildResult(cfg RegressionConfig, oldScores, newScores []float64) *RegressionResult {
	oldAvg := computeMean(oldScores)
	newAvg := computeMean(newScores)
	winRate := computeWinRate(oldScores, newScores)
	confident, pValue := computeSignificance(oldScores, newScores, cfg.Confidence)

	// Samples reflects the actual number of runs scored, not the configured
	// run counts: adaptive mode may stop early and trim both slices, so the
	// configured BaselineRuns/CompareRuns would over-report the real sample
	// size and make the result self-contradictory.
	samples := len(oldScores)
	if len(newScores) < samples {
		samples = len(newScores)
	}

	return &RegressionResult{
		OldStrategyID: fmt.Sprintf("%v", cfg.OldStrategy),
		NewStrategyID: fmt.Sprintf("%v", cfg.NewStrategy),
		OldAvg:        oldAvg,
		NewAvg:        newAvg,
		OldScores:     oldScores,
		NewScores:     newScores,
		WinRate:       winRate,
		Confident:     confident,
		// MinWinRate is the floor below which the new strategy is not
		// considered an improvement. Surfacing it makes the previously dead
		// config option meaningful to callers (e.g. the evolution gate).
		NewBetter: winRate >= cfg.MinWinRate,
		Samples:   samples,
		PValue:    pValue,
		TestedAt:  time.Now(),
	}
}

// computeMean calculates the arithmetic mean of a slice of floats.
func computeMean(scores []float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	sum := 0.0
	for _, s := range scores {
		sum += s
	}
	return sum / float64(len(scores))
}

// computeVariance calculates the sample variance of a slice of floats using Bessel's correction (n-1 denominator).
// Returns 0 for empty or single-element slices where sample variance is undefined.
func computeVariance(scores []float64) float64 {
	if len(scores) <= 1 {
		return 0
	}
	mean := computeMean(scores)
	sumSqDiff := 0.0
	for _, s := range scores {
		diff := s - mean
		sumSqDiff += diff * diff
	}
	return sumSqDiff / float64(len(scores)-1)
}

// computeWinRate calculates fraction where new score >= old score in pairwise comparison.
func computeWinRate(oldScores, newScores []float64) float64 {
	if len(oldScores) == 0 || len(newScores) == 0 {
		return 0
	}
	minLen := len(oldScores)
	if len(newScores) < minLen {
		minLen = len(newScores)
	}
	wins := 0
	for i := 0; i < minLen; i++ {
		if newScores[i] >= oldScores[i] {
			wins++
		}
	}
	return float64(wins) / float64(minLen)
}

// computeSignificance performs a simplified Welch's t-test.
// Returns true if the difference is statistically significant at the given confidence level.
// Uses a basic t-statistic approximation with normal distribution for large samples.
//
// Args:
//   - oldScores: baseline strategy scores.
//   - newScores: new strategy scores.
//   - confidenceLevel: significance threshold (e.g., 0.05 for 95% confidence).
//
// Returns:
//   - bool: true if statistically significant (p-value < confidenceLevel).
//   - float64: computed approximate p-value.
func computeSignificance(oldScores, newScores []float64, confidenceLevel float64) (bool, float64) {
	if len(oldScores) < 2 || len(newScores) < 2 {
		return false, 1.0
	}

	oldMean := computeMean(oldScores)
	newMean := computeMean(newScores)
	oldVar := computeVariance(oldScores)
	newVar := computeVariance(newScores)
	nOld := float64(len(oldScores))
	nNew := float64(len(newScores))

	// Welch's t-test standard error calculation.
	se := math.Sqrt(oldVar/nOld + newVar/nNew)
	if se == 0 {
		// Identical means with zero variance: no significant difference.
		if oldMean == newMean {
			return false, 1.0
		}
		// Different means but zero variance: highly significant.
		return true, 0.0
	}

	tStat := math.Abs(newMean-oldMean) / se

	// Approximate degrees of freedom using Welch-Satterthwaite equation.
	numerator := (oldVar/nOld + newVar/nNew) * (oldVar/nOld + newVar/nNew)
	denominator := (oldVar*oldVar)/(nOld*nOld*(nOld-1)) +
		(newVar*newVar)/(nNew*nNew*(nNew-1))
	if denominator == 0 {
		denominator = 1e-10
	}
	df := numerator / denominator

	// Approximate p-value using normal distribution for large samples.
	// For small df, this is conservative (overestimates p-value).
	pValue := approximatePValue(tStat, df)

	confident := pValue < confidenceLevel
	return confident, pValue
}

// approximatePValue computes a two-tailed p-value from a t-statistic.
// For large degrees of freedom (>30) the normal approximation is used; for
// smaller df it evaluates the exact Student's t distribution via the
// regularized incomplete beta function (Lentz continued fraction), replacing
// the previous ad-hoc scaling that was inaccurate near p≈0.05.
func approximatePValue(tStat float64, df float64) float64 {
	// For large degrees of freedom (>30), normal approximation is accurate.
	if df > 30 {
		return normalApproximationPValue(tStat)
	}
	// Exact two-tailed p-value: p = I_{df/(df+t²)}(df/2, 1/2).
	// (Abramowitz & Stegun 26.7.1 — Student's t CDF via incomplete beta.)
	if df <= 0 {
		return 1.0
	}
	x := df / (df + tStat*tStat)
	return regularizedBeta(df/2, 0.5, x)
}

// regularizedBeta computes the regularized incomplete beta function I_x(a, b)
// for 0 <= x <= 1 using the Lentz continued-fraction algorithm (Numerical
// Recipes betacf). It is exact enough for p-value work and does not need a
// t-distribution table.
func regularizedBeta(a, b, x float64) float64 {
	if x <= 0 {
		return 0
	}
	if x >= 1 {
		return 1
	}
	// Use the symmetry I_x(a,b) = 1 - I_{1-x}(b,a) when x is large to keep
	// the continued fraction well-conditioned.
	if x > (a+1)/(a+b+2) {
		return 1 - regularizedBeta(b, a, 1-x)
	}

	lnBetaA, _ := math.Lgamma(a)
	lnBetaB, _ := math.Lgamma(b)
	lnBetaAB, _ := math.Lgamma(a + b)
	lnBeta := lnBetaA + lnBetaB - lnBetaAB
	front := math.Exp(a*math.Log(x)+b*math.Log(1-x)-lnBeta) / a

	const maxIter = 200
	const eps = 3e-12

	qab := a + b
	qap := a + 1
	qam := a - 1
	c := 1.0
	d := 1 - qab*x/qap
	if math.Abs(d) < 1e-30 {
		d = 1e-30
	}
	d = 1 / d
	h := d
	for m := 1; m <= maxIter; m++ {
		m2 := 2 * m
		aa := float64(m) * (b - float64(m)) * x / ((qam + float64(m2)) * (a + float64(m2)))
		d = 1 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1 / d
		h *= d * c

		aa = -(a + float64(m)) * (qab + float64(m)) * x / ((a + float64(m2)) * (qap + float64(m2)))
		d = 1 + aa*d
		if math.Abs(d) < 1e-30 {
			d = 1e-30
		}
		c = 1 + aa/c
		if math.Abs(c) < 1e-30 {
			c = 1e-30
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < eps {
			break
		}
	}
	return front * h
}

// normalApproximationPValue approximates two-tailed p-value using error function.
func normalApproximationPValue(z float64) float64 {
	// Use the complementary error function approximation.
	// For large |z|, return very small values.
	// This is a simplified implementation suitable for regression testing purposes.
	absZ := math.Abs(z)

	// Approximation formula based on Abramowitz and Stegun.
	t := 1.0 / (1.0 + 0.2316419*absZ)
	poly := t * (0.319381530 + t*(-0.356563782+t*(1.781477937+t*(-1.821255978+t*1.330274429))))
	cdf := 1.0 - 0.3989422804014327*math.Exp(-z*z/2)*poly

	// Two-tailed p-value.
	pVal := 2.0 * (1.0 - cdf)
	if pVal > 1.0 {
		pVal = 1.0
	}
	if pVal < 0.0 {
		pVal = 0.0
	}
	return pVal
}

// validateRegressionConfig checks that all required fields are valid.
func validateRegressionConfig(cfg RegressionConfig) error {
	if cfg.OldStrategy == nil {
		return ErrNilStrategy
	}
	if cfg.NewStrategy == nil {
		return ErrNilStrategy
	}
	if cfg.BaselineRuns < 0 { // 0 means "use default"
		return ErrInvalidRuns
	}
	if cfg.CompareRuns < 0 {
		return ErrInvalidRuns
	}
	if cfg.Confidence < 0 || cfg.Confidence > 1 {
		return ErrConfidenceRange
	}
	if cfg.MinWinRate < 0 || cfg.MinWinRate > 1 {
		return fmt.Errorf("arena: min_win_rate must be between 0 and 1, got %f", cfg.MinWinRate)
	}
	return nil
}
