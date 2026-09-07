package evolution

import (
	"context"
	"errors"
	"fmt"
	"time"

	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// defaultRegressionTimeout bounds a single gate-3 regression run so a slow or
// hanging scorer can never block the verification pipeline indefinitely.
const defaultRegressionTimeout = 30 * time.Second

// defaultRegressionRuns is the preserved-case run count per strategy side.
const defaultRegressionRuns = 5

// defaultRegressionMinWinRate is the floor for the preserved-suite win rate;
// below it the new profile is treated as a regression.
const defaultRegressionMinWinRate = 0.55

// ErrNilRegressionScorer is returned when no ares_arena scorer is provided.
var ErrNilRegressionScorer = errors.New("evolution: regression scorer is nil")

// ErrNilRegressionProfileStore is returned when no profile store is provided.
var ErrNilRegressionProfileStore = errors.New("evolution: regression profile store is nil")

// CandidateRegressionOption configures a CandidateRegressionChecker.
type CandidateRegressionOption func(*CandidateRegressionChecker)

// WithRegressionRuns sets the preserved-case run count per strategy side.
func WithRegressionRuns(n int) CandidateRegressionOption {
	return func(rc *CandidateRegressionChecker) {
		if n > 0 {
			rc.baselineRuns = n
			rc.compareRuns = n
		}
	}
}

// WithRegressionMinWinRate sets the win-rate floor below which the new profile
// is considered a regression.
func WithRegressionMinWinRate(f float64) CandidateRegressionOption {
	return func(rc *CandidateRegressionChecker) {
		if f > 0 && f <= 1 {
			rc.minWinRate = f
		}
	}
}

// WithRegressionTimeout bounds a single regression run.
func WithRegressionTimeout(d time.Duration) CandidateRegressionOption {
	return func(rc *CandidateRegressionChecker) {
		if d > 0 {
			rc.timeout = d
		}
	}
}

// CandidateRegressionChecker wires the ares_arena preserved-case regression
// into candidate verification gate 3. It compares the target role's stable
// instructions against the candidate's diff over a preserved case suite using
// an injected ares_arena.Scorer, and rejects the candidate when the new profile
// significantly regresses the preserved cases (Ch.8 release gate).
//
// The scorer receives arena.TestCaseInput{Strategy, TestCase, Index}: Strategy
// is the old or new instruction string and TestCase is a preserved case. The
// caller provides a scorer that executes the instructions against the case and
// returns a [0,1] quality score.
type CandidateRegressionChecker struct {
	profileStore *ProfileStore
	scorer       ares_arena.Scorer
	testCases    []any
	baselineRuns int
	compareRuns  int
	minWinRate   float64
	timeout      time.Duration
}

// NewCandidateRegressionChecker creates a gate-3 regression checker.
// Args:
//
//	profileStore - reads the stable instructions of the target role; must be
//	  non-nil.
//	scorer - scores a single (instructions, case) execution; must be non-nil.
//	testCases - the preserved case suite (old-behavior cases that must not
//	  regress); may be empty, in which case the gate is skipped.
//	opts - optional configuration.
//
// Returns:
//
//	checker - the ready-to-use regression checker.
//	err - ErrNilRegressionProfileStore or ErrNilRegressionScorer.
func NewCandidateRegressionChecker(
	profileStore *ProfileStore,
	scorer ares_arena.Scorer,
	testCases []any,
	opts ...CandidateRegressionOption,
) (*CandidateRegressionChecker, error) {
	if profileStore == nil {
		return nil, ErrNilRegressionProfileStore
	}
	if scorer == nil {
		return nil, ErrNilRegressionScorer
	}
	rc := &CandidateRegressionChecker{
		profileStore: profileStore,
		scorer:       scorer,
		testCases:    testCases,
		baselineRuns: defaultRegressionRuns,
		compareRuns:  defaultRegressionRuns,
		minWinRate:   defaultRegressionMinWinRate,
		timeout:      defaultRegressionTimeout,
	}
	for _, opt := range opts {
		opt(rc)
	}
	return rc, nil
}

// Check implements the gate-3 regression check contract
// (func(c *Candidate) error). It uses a bounded background context so the
// injected scorer can observe cancellation and timeouts.
// Args:
//
//	c - the candidate under verification.
//
// Returns:
//
//	err - nil when no regression is detected (or the gate is not applicable);
//	  otherwise an error describing the regressed preserved cases.
func (rc *CandidateRegressionChecker) Check(c *Candidate) error {
	ctx, cancel := context.WithTimeout(context.Background(), rc.timeout)
	defer cancel()
	return rc.CheckContext(ctx, c)
}

// CheckContext runs the preserved-case regression with a caller-supplied
// context for cancellation and timeout control.
// Args:
//
//	ctx - cancellation and timeout context.
//	c - the candidate under verification; must be non-nil.
//
// Returns:
//
//	err - nil when no regression is detected, or an error describing the
//	  regressed preserved cases.
func (rc *CandidateRegressionChecker) CheckContext(ctx context.Context, c *Candidate) error {
	if c == nil {
		return errors.New("evolution: regression candidate is nil")
	}
	// Only instruction changes have a stable baseline to compare against;
	// skill/tool candidates are not regression-checked in v1.
	if c.Kind != CandidateInstruction {
		return nil
	}
	// No preserved suite: nothing to regress, gate is skipped by design.
	if len(rc.testCases) == 0 {
		return nil
	}
	stable := rc.profileStore.GetStable(c.TargetRole)
	if stable == nil {
		// No stable baseline for this role: cannot measure a regression.
		return nil
	}

	tester, err := ares_arena.NewRegressionTesterWithScorer(rc.scorer)
	if err != nil {
		return fmt.Errorf("evolution: build regression tester: %w", err)
	}
	result, err := tester.Run(ctx, ares_arena.RegressionConfig{
		OldStrategy:  stable.Instructions,
		NewStrategy:  c.Diff,
		BaselineRuns: rc.baselineRuns,
		CompareRuns:  rc.compareRuns,
		TestSuite:    "profile:" + c.TargetRole,
		Confidence:   0.05,
		MinWinRate:   rc.minWinRate,
		TestCases:    rc.testCases,
	})
	if err != nil {
		return fmt.Errorf("evolution: run preserved-case regression: %w", err)
	}

	// A regression is a statistically significant drop in the preserved suite.
	if result.Confident && result.NewAvg < result.OldAvg {
		return fmt.Errorf(
			"regression: preserved-suite avg dropped %.3f -> %.3f (win rate %.2f, p=%.4f, samples=%d)",
			result.OldAvg, result.NewAvg, result.WinRate, result.PValue, result.Samples,
		)
	}
	return nil
}
