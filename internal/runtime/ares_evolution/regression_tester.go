package evolution

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	mathrand "math/rand"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// RegressionTester implements TesterInterface by scoring candidate and baseline
// strategies using a provided scorer function.
type RegressionTester struct {
	scorer func(*mutation.Strategy) float64

	// rngMu serializes access to rng: math/rand.Rand is not goroutine-safe,
	// and Run may be called concurrently by findWinner's parallel evaluation
	// (errgroup). Without the lock the shared rng would race.
	rngMu sync.Mutex
	rng   *mathrand.Rand
}

// NewRegressionTester creates a regression tester for arena strategy comparison.
//
// Args:
//
//	scorer - scoring function for strategy evaluation (must not be nil).
//
// Returns:
//
//	*RegressionTester - the configured tester.
//	error - non-nil if scorer is nil.
func NewRegressionTester(scorer func(*mutation.Strategy) float64) (*RegressionTester, error) {
	if scorer == nil {
		return nil, errors.New("scorer must not be nil")
	}
	return &RegressionTester{
		scorer: scorer,
		rng:    newCryptoRand(),
	}, nil
}

// newCryptoRand creates a math/rand source seeded from crypto/rand.
func newCryptoRand() *mathrand.Rand {
	var seed int64
	if err := binary.Read(rand.Reader, binary.LittleEndian, &seed); err != nil {
		seed = time.Now().UnixNano()
	}
	return mathrand.New(mathrand.NewSource(seed)) //nolint:gosec // seeded rand for test perturbation
}

// Run executes a regression test comparing a candidate strategy against a baseline.
// With TaskSampleSize > 1, it runs multiple trials with slight score perturbation
// to produce a meaningful win rate.
//
// Args:
//
//	ctx - operation context for cancellation.
//	cfg - regression test configuration.
//
// Returns:
//
//	*RegressionResult - evaluation result with win rate and scores.
//	error - non-nil if scoring fails.
func (t *RegressionTester) Run(ctx context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	candidateMS := evolutionToMutationStrategy(cfg.Candidate)
	baselineMS := evolutionToMutationStrategy(cfg.Baseline)

	sampleSize := cfg.TaskSampleSize
	if sampleSize <= 0 {
		sampleSize = 1
	}

	var (
		candidateTotal float64
		baselineTotal  float64
		wins           int
	)

	for i := 0; i < sampleSize; i++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("regression test cancelled: %w", err)
		}

		candScore := t.scorer(&candidateMS)
		baseScore := t.scorer(&baselineMS)

		// Add slight noise for stochastic comparison when sampleSize > 1.
		if sampleSize > 1 {
			t.rngMu.Lock()
			candScore += (t.rng.Float64() - 0.5) * 0.02
			baseScore += (t.rng.Float64() - 0.5) * 0.02
			t.rngMu.Unlock()
		}

		candidateTotal += candScore
		baselineTotal += baseScore
		if candScore > baseScore {
			wins++
		}
	}

	n := float64(sampleSize)
	winRate := float64(wins) / n

	if winRate > 1.0 {
		winRate = 1.0
	}
	if winRate < 0 {
		winRate = 0
	}

	log.Debug("[RegressionTester] Evaluation complete",
		"candidate_id", cfg.Candidate.ID,
		"candidate_score", candidateTotal/n,
		"baseline_score", baselineTotal/n,
		"win_rate", winRate,
		"samples", sampleSize,
	)

	return &RegressionResult{
		CandidateScore: candidateTotal / n,
		BaselineScore:  baselineTotal / n,
		WinRate:        winRate,
		TotalTasks:     sampleSize,
	}, nil
}
