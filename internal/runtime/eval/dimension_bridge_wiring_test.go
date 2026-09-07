package eval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// errFailingStore is the sentinel error returned by failingWiringStore.
var errFailingStore = errors.New("wiring store failure")

// TestEvaluate_DimensionBridge_WiresEvidence verifies that when a dimension
// judge is configured with WithDimensionAveraging + WithEvidenceStore + WithRole,
// the structured dimension diagnosis is persisted as KindDimensionEval evidence
// and can be queried back — the production bridge that feeds the Diagnoser.
func TestEvaluate_DimensionBridge_WiresEvidence(t *testing.T) {
	ctx := context.Background()
	store := evidence.NewMemoryStore()
	client := &mockLLMClient{
		response: `{"correctness":3,"completeness":3,"efficiency":2,"safety":2,"reason":"all good"}`,
	}
	evaluator, err := NewLLMJudgeEvaluator(client,
		WithDimensionAveraging(),
		WithEvidenceStore(store),
		WithRole("coder"),
	)
	require.NoError(t, err)

	tc := TestCase{ID: "tc-bridge", Input: "solve task", ExpectedOutput: "expected"}
	result := TestResult{TestCaseID: "tc-bridge", ActualOutput: "actual"}

	scores, err := evaluator.Evaluate(ctx, tc, result)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, "llm_judge_dimension_avg", scores[0].Metric)

	// The dimension judgment must be queryable as KindDimensionEval evidence.
	records, err := store.Query(ctx, evidence.Filter{Kind: evidence.KindDimensionEval})
	require.NoError(t, err)
	require.Len(t, records, 1, "exactly one dimension_eval record must be emitted")
	assert.Equal(t, "dimension_judge", records[0].Source)
	assert.Equal(t, "coder", records[0].Metadata["role"])
	assert.Equal(t, "tc-bridge", records[0].Metadata["task_id"])
}

// TestEvaluate_DimensionBridge_NoStore_IsCompatibilityPath verifies that without
// a wired store the scalar dimension path stays intact and persists nothing.
func TestEvaluate_DimensionBridge_NoStore_IsCompatibilityPath(t *testing.T) {
	ctx := context.Background()
	client := &mockLLMClient{
		response: `{"correctness":1,"completeness":1,"efficiency":1,"safety":1,"reason":"mixed"}`,
	}
	evaluator, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	require.NoError(t, err)

	tc := TestCase{ID: "tc-nostore", Input: "in", ExpectedOutput: "exp"}
	result := TestResult{TestCaseID: "tc-nostore", ActualOutput: "out"}

	scores, err := evaluator.Evaluate(ctx, tc, result)
	require.NoError(t, err)
	require.Len(t, scores, 1)
	assert.Equal(t, "llm_judge_dimension_avg", scores[0].Metric)
}

// TestEvaluate_DimensionBridge_EmitFailureIsBestEffort verifies that a failing
// evidence store does not fail the scoring path — persistence is best-effort.
func TestEvaluate_DimensionBridge_EmitFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	store := &failingWiringStore{}
	client := &mockLLMClient{
		response: `{"correctness":3,"completeness":3,"efficiency":2,"safety":2,"reason":"ok"}`,
	}
	evaluator, err := NewLLMJudgeEvaluator(client,
		WithDimensionAveraging(),
		WithEvidenceStore(store),
		WithRole("reviewer"),
	)
	require.NoError(t, err)

	tc := TestCase{ID: "tc-err", Input: "in", ExpectedOutput: "exp"}
	result := TestResult{TestCaseID: "tc-err", ActualOutput: "out"}

	scores, err := evaluator.Evaluate(ctx, tc, result)
	require.NoError(t, err, "emit failure must not fail the evaluation")
	require.Len(t, scores, 1)
}

// failingWiringStore fails every persistence/query call to exercise the
// best-effort bridge path.
type failingWiringStore struct{}

func (f *failingWiringStore) Append(_ context.Context, _ evidence.Evidence) error {
	return errFailingStore
}

func (f *failingWiringStore) Query(_ context.Context, _ evidence.Filter) ([]evidence.Evidence, error) {
	return nil, errFailingStore
}

func (f *failingWiringStore) Aggregate(_ context.Context, _ evidence.Filter, _ evidence.AggregateFn) (float64, error) {
	return 0, errFailingStore
}

// TestEvaluate_DimensionBridge_PayloadDecodable verifies the emitted payload can
// be decoded back into the Evidence domain type (round-trip integrity).
func TestEvaluate_DimensionBridge_PayloadDecodable(t *testing.T) {
	ctx := context.Background()
	store := evidence.NewMemoryStore()
	// efficiency must be ≥ ceil(2*2/3)=2 to pass: the fixture previously
	// used 1/2, which only passed under the truncated integer threshold while
	// its evidence item said "failed" — the exact inconsistency the unified
	// float threshold removes.
	client := &mockLLMClient{
		response: `{"correctness":2,"completeness":3,"efficiency":2,"safety":2,"reason":"passes"}`,
	}
	evaluator, err := NewLLMJudgeEvaluator(client,
		WithDimensionAveraging(),
		WithEvidenceStore(store),
		WithRole("coder"),
	)
	require.NoError(t, err)

	tc := TestCase{ID: "tc-roundtrip", Input: "in", ExpectedOutput: "exp"}
	result := TestResult{TestCaseID: "tc-roundtrip", ActualOutput: "out"}
	_, err = evaluator.Evaluate(ctx, tc, result)
	require.NoError(t, err)

	records, err := store.Query(ctx, evidence.Filter{Kind: evidence.KindDimensionEval})
	require.NoError(t, err)
	require.Len(t, records, 1)

	// Payload must decode back into the Evidence domain type with its
	// structured verdict and dimensions preserved (round-trip integrity).
	var decoded Evidence
	require.NoError(t, json.Unmarshal(records[0].Payload, &decoded))
	assert.Equal(t, VerdictPass, decoded.Verdict, "payload must carry the structured verdict")
	require.NotEmpty(t, decoded.Dimensions, "payload must carry per-dimension scores")
}

// TestDimensionVerdictItemConsistency locks the verdict/evidence consistency
// contract: a
// dimension's Pass flag and its evidence-item status are derived from the SAME
// clamped score and the SAME float threshold, so they can never contradict.
func TestDimensionVerdictItemConsistency(t *testing.T) {
	ctx := context.Background()
	store := evidence.NewMemoryStore()
	// efficiency = 1 of max 2 (50% < 66.7%): must FAIL consistently in both
	// the dimension verdict and the attached item status.
	client := &mockLLMClient{
		response: `{"correctness":3,"completeness":3,"efficiency":1,"safety":2,"reason":"weak efficiency"}`,
	}
	evaluator, err := NewLLMJudgeEvaluator(client,
		WithDimensionAveraging(),
		WithEvidenceStore(store),
		WithRole("coder"),
	)
	require.NoError(t, err)

	tc := TestCase{ID: "tc-consistency", Input: "in", ExpectedOutput: "exp"}
	result := TestResult{TestCaseID: "tc-consistency", ActualOutput: "out"}
	_, err = evaluator.Evaluate(ctx, tc, result)
	require.NoError(t, err)

	records, err := store.Query(ctx, evidence.Filter{Kind: evidence.KindDimensionEval})
	require.NoError(t, err)
	require.Len(t, records, 1)

	var decoded Evidence
	require.NoError(t, json.Unmarshal(records[0].Payload, &decoded))
	for _, ds := range decoded.Dimensions {
		if ds.Name != "efficiency" {
			continue
		}
		assert.False(t, ds.Pass, "efficiency 1/2 must not pass")
		require.Len(t, ds.Evidence, 1)
		assert.Equal(t, "failed", ds.Evidence[0].Status,
			"item status must agree with the dimension Pass flag")
	}
}
