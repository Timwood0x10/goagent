package eval

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Verdict ─────────────────────────────────

func TestVerdict_String(t *testing.T) {
	assert.Equal(t, "pass", VerdictPass.String())
	assert.Equal(t, "fail", VerdictFail.String())
	assert.Equal(t, "uncertain", VerdictUncertain.String())

	// Unknown enum value falls back to uncertain.
	assert.Equal(t, "uncertain", Verdict(99).String())
}

// ── EvidenceItem ────────────────────────────

func TestEvidenceItem_String(t *testing.T) {
	item := EvidenceItem{Type: "test", Name: "unit_test_auth", Status: "failed", Detail: "assertion error"}
	assert.Equal(t, "[test] unit_test_auth: failed — assertion error", item.String())
}

// ── NewEvidence ─────────────────────────────

func TestNewEvidence_InitialState(t *testing.T) {
	e := NewEvidence("task-1", "coder", "result_verifier")
	require.NotNil(t, e)
	assert.Equal(t, "task-1", e.TaskID)
	assert.Equal(t, "coder", e.Role)
	assert.Equal(t, "result_verifier", e.Source)
	assert.NotNil(t, e.Meta)
	assert.Empty(t, e.Meta)
	assert.Empty(t, e.Dimensions)
	assert.False(t, e.CreatedAt.IsZero())
	assert.WithinDuration(t, time.Now(), e.CreatedAt, time.Second)
}

// ── AddDimension ────────────────────────────

func TestAddDimension_Threshold(t *testing.T) {
	tests := []struct {
		name  string
		score int
		max   int
		want  bool
	}{
		{"exactly 2/3 passes", 2, 3, true},
		{"above 2/3 passes", 3, 3, true},
		{"full score passes", 10, 10, true},
		{"below 2/3 fails", 1, 3, false},
		{"zero score fails", 0, 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEvidence("t", "coder", "result_verifier")
			e.AddDimension("task_result", tt.score, tt.max, nil, "")
			require.Len(t, e.Dimensions, 1)
			assert.Equal(t, tt.want, e.Dimensions[0].Pass)
		})
	}
}

func TestAddDimension_InvalidMax(t *testing.T) {
	t.Run("zero max never passes and records flag", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("task_result", 5, 0, nil, "")
		require.Len(t, e.Dimensions, 1)
		assert.False(t, e.Dimensions[0].Pass)
		assert.Contains(t, e.Dimensions[0].Flag, "invalid max score")
	})

	t.Run("negative max never passes", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("task_result", 5, -1, nil, "")
		require.Len(t, e.Dimensions, 1)
		assert.False(t, e.Dimensions[0].Pass)
	})
}

func TestAddDimension_ExplicitFlagWins(t *testing.T) {
	e := NewEvidence("t", "coder", "result_verifier")
	e.AddDimension("privacy_boundary", 1, 3, nil, "leaked user data")
	require.Len(t, e.Dimensions, 1)
	assert.Equal(t, "leaked user data", e.Dimensions[0].Flag)
	// The overall flag records the first failing dimension.
	assert.Equal(t, "leaked user data", e.Flag)
}

func TestAddDimension_SetsOverallFlag(t *testing.T) {
	e := NewEvidence("t", "coder", "result_verifier")
	e.AddDimension("task_result", 1, 3, nil, "")
	require.Len(t, e.Dimensions, 1)
	assert.False(t, e.Dimensions[0].Pass)
	assert.Contains(t, e.Flag, "task_result below threshold")
}

func TestAddDimension_PreservesEvidenceItems(t *testing.T) {
	e := NewEvidence("t", "coder", "result_verifier")
	items := []EvidenceItem{{Type: "test", Name: "t1", Status: "passed"}}
	e.AddDimension("task_result", 3, 3, items, "")
	require.Len(t, e.Dimensions, 1)
	assert.Equal(t, items, e.Dimensions[0].Evidence)
}

// ── HasFailure ──────────────────────────────

func TestEvidence_HasFailure(t *testing.T) {
	t.Run("no dimensions has no failure", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		assert.False(t, e.HasFailure())
	})

	t.Run("all passing has no failure", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("a", 3, 3, nil, "")
		e.AddDimension("b", 2, 3, nil, "")
		assert.False(t, e.HasFailure())
	})

	t.Run("any failing dimension triggers failure", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("a", 3, 3, nil, "")
		e.AddDimension("b", 1, 3, nil, "")
		assert.True(t, e.HasFailure())
	})
}

// ── FailureFlags ────────────────────────────

func TestEvidence_FailureFlags(t *testing.T) {
	t.Run("empty when nothing failed", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("a", 3, 3, nil, "")
		assert.Empty(t, e.FailureFlags())
	})

	t.Run("collects non-empty flags only", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("a", 1, 3, nil, "")                 // auto flag
		e.AddDimension("b", 1, 3, nil, "explicit failure") // explicit flag
		e.AddDimension("c", 1, 3, nil, "")                 // auto flag
		e.AddDimension("d", 3, 3, nil, "")                 // pass, no flag
		flags := e.FailureFlags()
		assert.Len(t, flags, 3)
		assert.Contains(t, flags[0], "a below threshold")
		assert.Contains(t, flags, "explicit failure")
	})

	t.Run("failed dimension without flag contributes empty entry", func(t *testing.T) {
		// FailureFlags filters out empty flags; a failed dim without a flag
		// is still counted via HasFailure but not listed here.
		e := NewEvidence("t", "coder", "result_verifier")
		e.AddDimension("x", 0, 3, nil, "")
		assert.True(t, e.HasFailure())
		assert.NotEmpty(t, e.FailureFlags()) // auto flag set by AddDimension
	})
}

// ── String ──────────────────────────────────

func TestEvidence_String(t *testing.T) {
	t.Run("passing evidence", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.Verdict = VerdictPass
		e.Confidence = 0.9
		e.AddDimension("a", 3, 3, nil, "")
		s := e.String()
		assert.Contains(t, s, "coder")
		assert.Contains(t, s, "pass")
		assert.Contains(t, s, "conf=0.90")
		assert.NotContains(t, s, "failures")
	})

	t.Run("failing evidence shows failure count", func(t *testing.T) {
		e := NewEvidence("t", "coder", "result_verifier")
		e.Verdict = VerdictFail
		e.AddDimension("a", 1, 3, nil, "")
		e.AddDimension("b", 1, 3, nil, "")
		s := e.String()
		assert.Contains(t, s, "fail(2 failures)")
	})
}
