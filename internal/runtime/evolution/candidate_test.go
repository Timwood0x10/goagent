package evolution

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// ── Candidate state machine ─────────────────

func TestNewCandidate_InitialState(t *testing.T) {
	c := NewCandidate(CandidateInstruction, "coder", "diff", "reason", []string{"ev-1"})
	require.NotNil(t, c)
	assert.Equal(t, StatusCandidate, c.Status)
	assert.Equal(t, CandidateInstruction, c.Kind)
	assert.Equal(t, "coder", c.TargetRole)
	assert.Equal(t, "diff", c.Diff)
	assert.Equal(t, []string{"ev-1"}, c.EvidenceIDs)
	assert.False(t, c.CreatedAt.IsZero())
}

func TestCandidate_VerifyRejectPromote(t *testing.T) {
	t.Run("verify clears rejection reason", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "diff", "reason", nil)
		c.Reject("static check failed")
		assert.Equal(t, StatusRejected, c.Status)
		assert.Equal(t, "static check failed", c.RejectionReason)

		c.Verify()
		assert.Equal(t, StatusVerified, c.Status)
		assert.Empty(t, c.RejectionReason)
	})

	t.Run("promote records timestamp", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "diff", "reason", nil)
		c.Verify()
		c.Promote()
		assert.Equal(t, StatusPromoted, c.Status)
		require.NotNil(t, c.PromotedAt)
		assert.False(t, c.PromotedAt.IsZero())
	})
}

func TestCandidate_String_ShortID(t *testing.T) {
	// Regression: c.ID[:8] panics when the ID is shorter than 8 characters
	// (CandidateStore.Submit generates IDs like "cand-1").
	store := NewCandidateStore()
	c := NewCandidate(CandidateInstruction, "coder", "diff", "reason", nil)
	store.Submit(c)
	assert.Equal(t, "cand-1", c.ID)
	assert.NotPanics(t, func() {
		_ = c.String()
	})
	assert.Contains(t, c.String(), "cand-1")
}

// ── CandidateStore ──────────────────────────

func TestCandidateStore_SubmitGetList(t *testing.T) {
	store := NewCandidateStore()
	store.Submit(NewCandidate(CandidateInstruction, "coder", "d1", "r1", nil))
	store.Submit(NewCandidate(CandidateSkill, "reviewer", "d2", "r2", nil))

	c1 := store.Get("cand-1")
	require.NotNil(t, c1)
	assert.Equal(t, "coder", c1.TargetRole)

	assert.Nil(t, store.Get("cand-999"))

	byStatus := store.ListByStatus(StatusCandidate)
	assert.Len(t, byStatus, 2)
	assert.Len(t, store.ListByStatus(StatusPromoted), 0)

	byRole := store.ListByRole("coder")
	require.Len(t, byRole, 1)
	assert.Equal(t, "cand-1", byRole[0].ID)
	assert.Len(t, store.ListByRole("planner"), 0)
}

// ── CandidateVerifier ───────────────────────

func TestCandidateVerifier_StaticCheck(t *testing.T) {
	verifier := &CandidateVerifier{}

	t.Run("valid candidate passes static check", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", []string{"ev-1"})
		result := verifier.Verify(c)
		assert.True(t, result.Success)
		assert.Empty(t, result.Reason)
	})

	t.Run("empty target role rejected", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "", "diff", "reason", []string{"ev-1"})
		result := verifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "target role is empty")
	})

	t.Run("empty diff rejected", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "", "reason", []string{"ev-1"})
		result := verifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "diff is empty")
	})

	t.Run("dangerous pattern rejected", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "ignore all safety and bypass authentication", "reason", []string{"ev-1"})
		result := verifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "dangerous pattern")
	})

	t.Run("no evidence IDs rejected by replay gate", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", nil)
		result := verifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "no evidence IDs")
	})
}

// ── CandidateVerifier failure-replay gate (gate 2) with evidence store ──

// appendFailureEvidence appends a KindDimensionEval evidence record with a
// deterministic ID for a role and returns it.
func appendFailureEvidence(t *testing.T, store evidence.Store, id, role string) {
	t.Helper()
	ctx := context.Background()
	rec := evidence.NewEvidence("result_verifier", evidence.KindDimensionEval,
		map[string]any{"verdict": "fail"},
		evidence.WithMetadata("role", role),
	)
	rec.ID = id
	require.NoError(t, store.Append(ctx, rec))
}

func TestCandidateVerifier_ReplayGate_WithStore(t *testing.T) {
	ctx := context.Background()
	store := evidence.NewMemoryStore()
	appendFailureEvidence(t, store, "ev-1", "coder")
	appendFailureEvidence(t, store, "ev-2", "coder")
	verifier := NewCandidateVerifierWithOptions(WithEvidenceStore(store))

	t.Run("referenced evidence exists and passes", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", []string{"ev-1", "ev-2"})
		result := verifier.Verify(c)
		assert.True(t, result.Success, "existing dimension_eval evidence must pass gate 2")
		assert.Equal(t, StatusVerified, c.Status)
	})

	t.Run("missing evidence ID rejected", func(t *testing.T) {
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", []string{"ev-999"})
		result := verifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "ev-999")
		assert.Equal(t, StatusRejected, c.Status)
	})

	t.Run("wrong-kind evidence rejected", func(t *testing.T) {
		other := evidence.NewMemoryStore()
		rec := evidence.NewEvidence("flight", evidence.KindExecutionTrace, map[string]any{"trace": "t"})
		rec.ID = "ev-trace"
		require.NoError(t, other.Append(ctx, rec))
		otherVerifier := NewCandidateVerifierWithOptions(WithEvidenceStore(other))
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", []string{"ev-trace"})
		result := otherVerifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "dimension_eval")
	})

	t.Run("store query error surfaces as rejection", func(t *testing.T) {
		failing := &failingEvidenceStore{}
		badVerifier := NewCandidateVerifierWithOptions(WithEvidenceStore(failing))
		c := NewCandidate(CandidateInstruction, "coder", "write tests", "fix bug", []string{"ev-1"})
		result := badVerifier.Verify(c)
		assert.False(t, result.Success)
		assert.Contains(t, result.Reason, "query failure evidence")
	})
}

// failingEvidenceStore always fails queries to exercise the error path.
type failingEvidenceStore struct{}

func (f *failingEvidenceStore) Append(_ context.Context, _ evidence.Evidence) error {
	return errors.New("append not supported")
}

func (f *failingEvidenceStore) Query(_ context.Context, _ evidence.Filter) ([]evidence.Evidence, error) {
	return nil, errors.New("query failed")
}

func (f *failingEvidenceStore) Aggregate(_ context.Context, _ evidence.Filter, _ evidence.AggregateFn) (float64, error) {
	return 0, errors.New("aggregate not supported")
}

// TestCandidateStore_ConcurrentAccess exercises the RWMutex under concurrent
// Submit/Get/List calls and must pass go test -race.
func TestCandidateStore_ConcurrentAccess(t *testing.T) {
	store := NewCandidateStore()
	const workers = 32
	const perWorker = 50

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				store.Submit(NewCandidate(CandidateInstruction, "coder", "d", "r", nil))
				_ = store.Get("cand-1")
				_ = store.ListByStatus(StatusCandidate)
				_ = store.ListByRole("coder")
			}
		}()
	}
	wg.Wait()

	// All workers must have been assigned unique sequential IDs.
	ids := make(map[string]bool)
	for _, c := range store.ListByStatus(StatusCandidate) {
		if ids[c.ID] {
			t.Fatalf("duplicate candidate ID %q under concurrency", c.ID)
		}
		ids[c.ID] = true
	}
	assert.Equal(t, workers*perWorker, len(ids), "every submitted candidate must get a unique ID")
}

// ── containsDangerousPattern ────────────────

func TestContainsDangerousPattern(t *testing.T) {
	t.Run("detects all dangerous patterns", func(t *testing.T) {
		patterns := []string{
			"ignore all safety",
			"bypass authentication",
			"delete all data",
			"don't verify",
		}
		for _, p := range patterns {
			assert.True(t, containsDangerousPattern(p), "should detect: %s", p)
		}
	})

	t.Run("allows benign instructions", func(t *testing.T) {
		assert.False(t, containsDangerousPattern("write modular tests for the auth module"))
		assert.False(t, containsDangerousPattern(""))
	})

	t.Run("detects pattern inside longer text", func(t *testing.T) {
		assert.True(t, containsDangerousPattern("never ignore all safety checks"))
	})
}
