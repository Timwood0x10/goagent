package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// ErrNilJudgeResponse is returned when a DimensionJudgeResponse is nil.
var ErrNilJudgeResponse = errors.New("dimension judge response is nil")

// ErrNilEvidenceStore is returned when the bridge has no evidence store.
var ErrNilEvidenceStore = errors.New("evidence store is nil")

// evidenceTypeLLMJudge is the EvidenceItem type for rubric-dimension items.
const evidenceTypeLLMJudge = "llm_judge"

// DimensionJudgeBridge converts a scalar DimensionJudgeResponse (four rubric
// dimensions) into a structured Evidence diagnosis (Ch.8: verification
// results must not be compressed into a single scalar score) and persists it
// into the universal evidence store under KindDimensionEval.
type DimensionJudgeBridge struct {
	store evidence.Store
}

// NewDimensionJudgeBridge creates a bridge that persists structured evidence
// through the given store.
// Args:
//
//	store - the universal evidence store; must be non-nil for Emit.
//
// Returns:
//
//	bridge - a ready-to-use bridge; Emit returns ErrNilEvidenceStore when
//	  store is nil.
func NewDimensionJudgeBridge(store evidence.Store) *DimensionJudgeBridge {
	return &DimensionJudgeBridge{store: store}
}

// ToEvidence converts a scalar judge response into structured evidence.
// The verdict is derived as follows:
//   - reason containing an "uncertain" keyword  -> VerdictUncertain;
//   - every dimension passing the 2/3 threshold -> VerdictPass;
//   - any dimension failing                      -> VerdictFail.
//
// Args:
//
//	taskID - the task being evaluated.
//	role - the agent role whose execution is assessed.
//	resp - the scalar judge response, must be non-nil.
//
// Returns:
//
//	evidence - the structured diagnosis; nil when resp is nil.
//	err - ErrNilJudgeResponse when resp is nil.
func (b *DimensionJudgeBridge) ToEvidence(taskID, role string, resp *DimensionJudgeResponse) (*Evidence, error) {
	if resp == nil {
		return nil, ErrNilJudgeResponse
	}

	ev := NewEvidence(taskID, role, "dimension_judge")
	// Feed the SAME clamped value to both the dimension verdict and its
	// evidence item: previously the item status was computed from the
	// RAW float while Pass used the rounded int, so a score like 1.2/2 could
	// yield Pass=true with a "failed" item — contradictory evidence.
	correctness := clampScore(resp.Correctness, 3)
	completeness := clampScore(resp.Completeness, 3)
	efficiency := clampScore(resp.Efficiency, 2)
	safety := clampScore(resp.Safety, 2)
	ev.AddDimension("correctness", correctness, 3, llmJudgeItem("correctness", float64(correctness), 3), "")
	ev.AddDimension("completeness", completeness, 3, llmJudgeItem("completeness", float64(completeness), 3), "")
	ev.AddDimension("efficiency", efficiency, 2, llmJudgeItem("efficiency", float64(efficiency), 2), "")
	ev.AddDimension("safety", safety, 2, llmJudgeItem("safety", float64(safety), 2), "")
	ev.Confidence = averageConfidence(resp)

	switch {
	case isUncertainReason(resp.Reason):
		ev.Verdict = VerdictUncertain
	case ev.HasFailure():
		ev.Verdict = VerdictFail
	default:
		ev.Verdict = VerdictPass
	}
	return ev, nil
}

// Emit converts the judge response into structured evidence and persists it
// into the universal evidence store under KindDimensionEval.
// Args:
//
//	ctx - timeout and cancellation context.
//	taskID - the task being evaluated, recorded as metadata.
//	role - the agent role whose execution is assessed.
//	resp - the scalar judge response, must be non-nil.
//
// Returns:
//
//	err - ErrNilEvidenceStore when the store is nil, ErrNilJudgeResponse when
//	  resp is nil, or the underlying persistence error.
func (b *DimensionJudgeBridge) Emit(ctx context.Context, taskID, role string, resp *DimensionJudgeResponse) error {
	if b.store == nil {
		return ErrNilEvidenceStore
	}
	if resp == nil {
		return ErrNilJudgeResponse
	}
	ev, err := b.ToEvidence(taskID, role, resp)
	if err != nil {
		return err
	}
	collector := evidence.NewCollector(b.store, "dimension_judge")
	return collector.EmitWithMeta(ctx, evidence.KindDimensionEval, ev,
		"task_id", taskID, "role", role)
}

// clampScore rounds a float score to the nearest integer and clamps it into
// the valid [0, max] range to defend against out-of-range judge output.
func clampScore(score float64, max int) int {
	rounded := int(math.Round(score))
	if rounded < 0 {
		return 0
	}
	if rounded > max {
		return max
	}
	return rounded
}

// llmJudgeItem builds the EvidenceItem attached to a rubric dimension.
func llmJudgeItem(name string, score float64, max int) []EvidenceItem {
	status := "passed"
	if score < float64(max)*2/3 {
		status = "failed"
	}
	return []EvidenceItem{{
		Type:   evidenceTypeLLMJudge,
		Name:   name,
		Status: status,
		Detail: fmt.Sprintf("score %.1f/%d", score, max),
	}}
}

// isUncertainReason reports whether the judge's reason signals that the result
// cannot be determined (Chinese or English keywords).
func isUncertainReason(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "无法判断") ||
		strings.Contains(lower, "无法确定") ||
		strings.Contains(lower, "cannot determine") ||
		strings.Contains(lower, "uncertain")
}

// averageConfidence computes the overall confidence from the four dimensions.
func averageConfidence(resp *DimensionJudgeResponse) float64 {
	total := resp.Correctness + resp.Completeness + resp.Efficiency + resp.Safety
	const maxTotal = 10.0 // 3 + 3 + 2 + 2
	if total < 0 {
		return 0
	}
	if total > maxTotal {
		return 1
	}
	return total / maxTotal
}
