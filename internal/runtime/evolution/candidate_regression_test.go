package evolution

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/evidence"
	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// strategyAwareScorer returns a fixed score based on which strategy
// (old stable instructions vs new candidate diff) is being scored.
type strategyAwareScorer struct {
	oldScore float64
	newScore float64
}

func (s *strategyAwareScorer) Score(_ context.Context, input any) (float64, error) {
	ti, ok := input.(ares_arena.TestCaseInput)
	if !ok {
		return 0, errors.New("unexpected scorer input type")
	}
	strategy, ok := ti.Strategy.(string)
	if !ok {
		return 0, errors.New("strategy must be a string")
	}
	if strategy == stableInstructionsCoder {
		return s.oldScore, nil
	}
	return s.newScore, nil
}

// failingScorer always errors to exercise the error-propagation path.
type failingScorer struct{}

func (f *failingScorer) Score(_ context.Context, _ any) (float64, error) {
	return 0, errors.New("scorer failure")
}

// preservedCase is a minimal preserved case value; the scorer does not inspect
// it, only the strategy.
type preservedCase struct {
	Name string
}

const stableInstructionsCoder = "old coder instructions"

// newRegressionProfileStore seeds a candidate + stable profile for a role.
func newRegressionProfileStore(t *testing.T, stableInstructions string) *ProfileStore {
	t.Helper()
	store := NewProfileStore()
	profile := &agents.AgentProfile{Role: "coder", Instructions: stableInstructions}
	require.NoError(t, store.Update(profile))
	require.NoError(t, store.SetStable("coder", profile))
	return store
}

func TestNewCandidateRegressionChecker_Validation(t *testing.T) {
	t.Run("nil profile store rejected", func(t *testing.T) {
		_, err := NewCandidateRegressionChecker(nil, &strategyAwareScorer{}, nil)
		require.ErrorIs(t, err, ErrNilRegressionProfileStore)
	})
	t.Run("nil scorer rejected", func(t *testing.T) {
		_, err := NewCandidateRegressionChecker(NewProfileStore(), nil, nil)
		require.ErrorIs(t, err, ErrNilRegressionScorer)
	})
}

func TestCandidateRegressionChecker_RejectsRegression(t *testing.T) {
	// New profile scores clearly worse on the preserved suite.
	scorer := &strategyAwareScorer{oldScore: 0.9, newScore: 0.2}
	checker, err := NewCandidateRegressionChecker(
		newRegressionProfileStore(t, stableInstructionsCoder),
		scorer,
		[]any{preservedCase{Name: "case-1"}, preservedCase{Name: "case-2"}, preservedCase{Name: "case-3"}},
		WithRegressionRuns(5),
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateInstruction, "coder", "new coder instructions", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.Error(t, err, "a significant drop on preserved cases must be rejected")
	assert.Contains(t, err.Error(), "regression")
}

func TestCandidateRegressionChecker_PassesNoRegression(t *testing.T) {
	// New profile scores equal or better than old on the preserved suite.
	scorer := &strategyAwareScorer{oldScore: 0.8, newScore: 0.9}
	checker, err := NewCandidateRegressionChecker(
		newRegressionProfileStore(t, stableInstructionsCoder),
		scorer,
		[]any{preservedCase{Name: "case-1"}, preservedCase{Name: "case-2"}},
		WithRegressionRuns(5),
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateInstruction, "coder", "new coder instructions", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.NoError(t, err)
}

func TestCandidateRegressionChecker_SkipsWhenNoBaseline(t *testing.T) {
	// No stable profile for the role -> no baseline to regress against.
	scorer := &strategyAwareScorer{oldScore: 0.9, newScore: 0.2}
	checker, err := NewCandidateRegressionChecker(
		NewProfileStore(), // empty store, no stable profile
		scorer,
		[]any{preservedCase{Name: "case-1"}},
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateInstruction, "coder", "new", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.NoError(t, err, "missing stable baseline must skip the gate, not fail")
}

func TestCandidateRegressionChecker_SkipsNonInstruction(t *testing.T) {
	scorer := &strategyAwareScorer{oldScore: 0.9, newScore: 0.2}
	checker, err := NewCandidateRegressionChecker(
		newRegressionProfileStore(t, stableInstructionsCoder),
		scorer,
		[]any{preservedCase{Name: "case-1"}},
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateSkill, "coder", "new skill", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.NoError(t, err, "non-instruction candidates are not regression-checked in v1")
}

func TestCandidateRegressionChecker_SkipsEmptySuite(t *testing.T) {
	scorer := &strategyAwareScorer{oldScore: 0.9, newScore: 0.2}
	checker, err := NewCandidateRegressionChecker(
		newRegressionProfileStore(t, stableInstructionsCoder),
		scorer,
		nil, // no preserved cases
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateInstruction, "coder", "new", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.NoError(t, err, "an empty preserved suite must skip the gate")
}

func TestCandidateRegressionChecker_PropagatesScorerError(t *testing.T) {
	checker, err := NewCandidateRegressionChecker(
		newRegressionProfileStore(t, stableInstructionsCoder),
		&failingScorer{},
		[]any{preservedCase{Name: "case-1"}},
		WithRegressionRuns(3),
	)
	require.NoError(t, err)

	c := NewCandidate(CandidateInstruction, "coder", "new", "fix", []string{"ev-1"})
	err = checker.Check(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scorer failure")
}

// TestCandidateRegressionChecker_WiredIntoVerifier verifies the regression
// checker integrates with the CandidateVerifier gate 3 via WithRegressionCheck.
func TestCandidateRegressionChecker_WiredIntoVerifier(t *testing.T) {
	scorer := &strategyAwareScorer{oldScore: 0.9, newScore: 0.2}
	profileStore := newRegressionProfileStore(t, stableInstructionsCoder)
	checker, err := NewCandidateRegressionChecker(profileStore, scorer, []any{preservedCase{Name: "case-1"}})
	require.NoError(t, err)

	// A candidate that is both valid and regresses preserved cases.
	c := NewCandidate(CandidateInstruction, "coder", "new coder instructions", "fix", []string{"ev-1"})

	// Seed matching failure evidence so gate 2 passes and only gate 3 decides.
	store := evidence.NewMemoryStore()
	appendFailureEvidence(t, store, "ev-1", "coder")
	verifier := NewCandidateVerifierWithOptions(
		WithEvidenceStore(store),
		WithRegressionCheck(checker.Check),
	)

	result := verifier.Verify(c)
	assert.False(t, result.Success, "a regressing candidate must be rejected at gate 3")
	assert.Contains(t, result.Reason, "regression")
	assert.Equal(t, StatusRejected, c.Status)
}
