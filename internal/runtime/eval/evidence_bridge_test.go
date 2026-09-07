package eval

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// ── ToEvidence verdict derivation ───────────

func TestDimensionJudgeBridge_ToEvidence_Pass(t *testing.T) {
	bridge := NewDimensionJudgeBridge(nil)
	resp := &DimensionJudgeResponse{
		Correctness:  3,
		Completeness: 3,
		Efficiency:   2,
		Safety:       2,
		Reason:       "all dimensions look good",
	}

	ev, err := bridge.ToEvidence("task-1", "coder", resp)
	require.NoError(t, err)
	require.NotNil(t, ev)
	assert.Equal(t, VerdictPass, ev.Verdict)
	assert.Equal(t, "dimension_judge", ev.Source)
	assert.Equal(t, "task-1", ev.TaskID)
	assert.Equal(t, "coder", ev.Role)
	assert.False(t, ev.HasFailure())
	assert.Len(t, ev.Dimensions, 4)
	// Confidence: (3+3+2+2)/10 = 1.0
	assert.Equal(t, 1.0, ev.Confidence)
}

func TestDimensionJudgeBridge_ToEvidence_Fail(t *testing.T) {
	bridge := NewDimensionJudgeBridge(nil)
	resp := &DimensionJudgeResponse{
		Correctness:  0,
		Completeness: 3,
		Efficiency:   2,
		Safety:       2,
		Reason:       "incorrect answer",
	}

	ev, err := bridge.ToEvidence("task-1", "coder", resp)
	require.NoError(t, err)
	assert.Equal(t, VerdictFail, ev.Verdict)
	assert.True(t, ev.HasFailure())
	assert.NotEmpty(t, ev.Flag)
	// The failing dimension carries its own flag.
	assert.Contains(t, ev.FailureFlags()[0], "correctness")
}

func TestDimensionJudgeBridge_ToEvidence_UncertainReason(t *testing.T) {
	bridge := NewDimensionJudgeBridge(nil)

	tests := []struct {
		name   string
		reason string
	}{
		{"chinese cannot judge", "无法判断结果是否正确"},
		{"chinese cannot determine", "无法确定"},
		{"english cannot determine", "cannot determine the result"},
		{"english uncertain", "result is uncertain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &DimensionJudgeResponse{
				Correctness:  1,
				Completeness: 1,
				Efficiency:   1,
				Safety:       1,
				Reason:       tt.reason,
			}
			ev, err := bridge.ToEvidence("task-1", "coder", resp)
			require.NoError(t, err)
			assert.Equal(t, VerdictUncertain, ev.Verdict)
		})
	}
}

func TestDimensionJudgeBridge_ToEvidence_NilResponse(t *testing.T) {
	bridge := NewDimensionJudgeBridge(nil)
	ev, err := bridge.ToEvidence("task-1", "coder", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilJudgeResponse)
	assert.Nil(t, ev)
}

func TestDimensionJudgeBridge_ClampScore(t *testing.T) {
	assert.Equal(t, 0, clampScore(-5, 3))
	assert.Equal(t, 3, clampScore(99, 3))
	assert.Equal(t, 2, clampScore(2.4, 3)) // rounds down
	assert.Equal(t, 3, clampScore(2.6, 3)) // rounds up
	assert.Equal(t, 1, clampScore(1, 2))
}

// ── Persistence round-trip ──────────────────

func TestDimensionJudgeBridge_EmitAndQueryBack(t *testing.T) {
	store := evidence.NewMemoryStore()
	bridge := NewDimensionJudgeBridge(store)
	resp := &DimensionJudgeResponse{
		Correctness:  1,
		Completeness: 3,
		Efficiency:   2,
		Safety:       2,
		Reason:       "mostly correct",
	}

	err := bridge.Emit(context.Background(), "task-7", "coder", resp)
	require.NoError(t, err)

	// Query the universal store by the new kind.
	records, err := store.Query(context.Background(), evidence.Filter{Kind: evidence.KindDimensionEval})
	require.NoError(t, err)
	require.Len(t, records, 1, "exactly one dimension_eval record expected")

	record := records[0]
	assert.Equal(t, "dimension_judge", record.Source)
	assert.Equal(t, evidence.KindDimensionEval, record.Kind)
	assert.Equal(t, "task-7", record.Metadata["task_id"])
	assert.Equal(t, "coder", record.Metadata["role"])

	// The payload must round-trip into an Evidence.
	var back Evidence
	err = json.Unmarshal(record.Payload, &back)
	require.NoError(t, err)
	assert.Equal(t, "task-7", back.TaskID)
	assert.Equal(t, VerdictFail, back.Verdict)
	assert.Len(t, back.Dimensions, 4)
}

func TestDimensionJudgeBridge_Emit_NilStore(t *testing.T) {
	bridge := NewDimensionJudgeBridge(nil)
	err := bridge.Emit(context.Background(), "task-1", "coder", &DimensionJudgeResponse{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilEvidenceStore)
}

func TestDimensionJudgeBridge_Emit_NilResponse(t *testing.T) {
	bridge := NewDimensionJudgeBridge(evidence.NewMemoryStore())
	err := bridge.Emit(context.Background(), "task-1", "coder", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilJudgeResponse)
}

// ── LLM judge item helper ───────────────────

func TestLLMJudgeItem(t *testing.T) {
	passed := llmJudgeItem("correctness", 3, 3)
	require.Len(t, passed, 1)
	assert.Equal(t, "passed", passed[0].Status)
	assert.Equal(t, "llm_judge", passed[0].Type)

	failed := llmJudgeItem("safety", 1, 2)
	require.Len(t, failed, 1)
	assert.Equal(t, "failed", failed[0].Status)
}

// ── isUncertainReason ───────────────────────

func TestIsUncertainReason(t *testing.T) {
	assert.True(t, isUncertainReason("无法判断"))
	assert.True(t, isUncertainReason("Cannot Determine"))
	assert.False(t, isUncertainReason("the answer is correct"))
	assert.False(t, isUncertainReason(""))
}
