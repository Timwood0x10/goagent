package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

// mockNormalizer implements experience.Normalizer for testing.
type mockNormalizer struct {
	normalizeFn func(ctx context.Context, raw experience.RawExperience) (experience.NormalizedExperience, error)
}

func (m *mockNormalizer) Normalize(ctx context.Context, raw experience.RawExperience) (experience.NormalizedExperience, error) {
	if m.normalizeFn != nil {
		return m.normalizeFn(ctx, raw)
	}
	return experience.NormalizedExperience{StrategyID: raw.StrategyID, TaskType: "test"}, nil
}

func (m *mockNormalizer) NormalizeBatch(ctx context.Context, raws []experience.RawExperience) ([]experience.NormalizedExperience, error) {
	out := make([]experience.NormalizedExperience, 0, len(raws))
	for _, r := range raws {
		n, err := m.Normalize(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// mockOutcomeStore implements experience.ExperienceStore for testing.
type mockOutcomeStore struct {
	appendFn      func(ctx context.Context, exp experience.NormalizedExperience) error
	appendBatchFn func(ctx context.Context, exps []experience.NormalizedExperience) error
}

func (m *mockOutcomeStore) Append(ctx context.Context, exp experience.NormalizedExperience) error {
	if m.appendFn != nil {
		return m.appendFn(ctx, exp)
	}
	return nil
}

func (m *mockOutcomeStore) AppendBatch(ctx context.Context, exps []experience.NormalizedExperience) error {
	if m.appendBatchFn != nil {
		return m.appendBatchFn(ctx, exps)
	}
	return nil
}

func (m *mockOutcomeStore) Query(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]experience.NormalizedExperience, error) {
	return nil, nil
}

func (m *mockOutcomeStore) QueryByTaskType(ctx context.Context, taskType string, limit int) ([]experience.NormalizedExperience, error) {
	return nil, nil
}

func (m *mockOutcomeStore) GetStatistics(ctx context.Context, strategyID string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func (m *mockOutcomeStore) GetTaskTypeStatistics(ctx context.Context, taskType string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func TestNewOutcomeExperienceRecorder_NilNormalizer(t *testing.T) {
	_, err := NewOutcomeExperienceRecorder(nil, &mockOutcomeStore{})
	if err == nil {
		t.Fatal("expected error for nil normalizer")
	}
}

func TestNewOutcomeExperienceRecorder_NilStore(t *testing.T) {
	_, err := NewOutcomeExperienceRecorder(&mockNormalizer{}, nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNewOutcomeExperienceRecorder_Success(t *testing.T) {
	r, err := NewOutcomeExperienceRecorder(&mockNormalizer{}, &mockOutcomeStore{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil recorder")
	}
}

func TestRecordOutcome_NilRecorder(t *testing.T) {
	var r *OutcomeExperienceRecorder
	err := r.RecordOutcome(context.Background(), ExecutionOutcome{})
	if err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

func TestRecordOutcome_AppendCalledOnSuccess(t *testing.T) {
	appendCalled := false
	store := &mockOutcomeStore{
		appendFn: func(ctx context.Context, exp experience.NormalizedExperience) error {
			appendCalled = true
			if exp.StrategyID != "exec-1" {
				t.Errorf("expected StrategyID exec-1, got %s", exp.StrategyID)
			}
			return nil
		},
	}
	r, err := NewOutcomeExperienceRecorder(&mockNormalizer{}, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = r.RecordOutcome(context.Background(), ExecutionOutcome{
		ExecutionID: "exec-1",
		Status:      "success",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !appendCalled {
		t.Error("expected Append to be called")
	}
}

func TestRecordOutcome_NormalizerError(t *testing.T) {
	store := &mockOutcomeStore{}
	norm := &mockNormalizer{
		normalizeFn: func(ctx context.Context, raw experience.RawExperience) (experience.NormalizedExperience, error) {
			return experience.NormalizedExperience{}, errors.New("normalize failed")
		},
	}
	r, err := NewOutcomeExperienceRecorder(norm, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = r.RecordOutcome(context.Background(), ExecutionOutcome{ExecutionID: "x"})
	if err == nil {
		t.Fatal("expected error from normalizer")
	}
}

func TestRecordOutcome_FilteredExperience(t *testing.T) {
	appendCalled := false
	store := &mockOutcomeStore{
		appendFn: func(ctx context.Context, exp experience.NormalizedExperience) error {
			appendCalled = true
			return nil
		},
	}
	norm := &mockNormalizer{
		normalizeFn: func(ctx context.Context, raw experience.RawExperience) (experience.NormalizedExperience, error) {
			return experience.NormalizedExperience{
				StrategyID:   raw.StrategyID,
				IsFiltered:   true,
				FilterReason: "noise",
			}, nil
		},
	}
	r, err := NewOutcomeExperienceRecorder(norm, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = r.RecordOutcome(context.Background(), ExecutionOutcome{ExecutionID: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if appendCalled {
		t.Error("expected Append NOT to be called for filtered experience")
	}
}

func TestRecordOutcome_StoreError(t *testing.T) {
	store := &mockOutcomeStore{
		appendFn: func(ctx context.Context, exp experience.NormalizedExperience) error {
			return errors.New("store full")
		},
	}
	r, err := NewOutcomeExperienceRecorder(&mockNormalizer{}, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = r.RecordOutcome(context.Background(), ExecutionOutcome{ExecutionID: "x"})
	if err == nil {
		t.Fatal("expected error from store")
	}
}

func TestComputeOutcomeScore_SuccessNoSteps(t *testing.T) {
	score := computeOutcomeScore(ExecutionOutcome{Status: "success"})
	if score != 1.0 {
		t.Errorf("expected 1.0, got %f", score)
	}
}

func TestComputeOutcomeScore_SuccessWithSteps(t *testing.T) {
	score := computeOutcomeScore(ExecutionOutcome{
		Status:      "success",
		TotalSteps:  10,
		FailedSteps: 2,
		ErrorCount:  1,
	})
	expected := 1.0 - float64(2+1)/float64(10) // 0.7
	if score != expected {
		t.Errorf("expected %f, got %f", expected, score)
	}
}

func TestComputeOutcomeScore_Failure(t *testing.T) {
	score := computeOutcomeScore(ExecutionOutcome{
		Status:         "failure",
		TotalSteps:     10,
		FailedSteps:    4,
		ErrorCount:     2,
		InterruptCount: 1,
	})
	expected := 1.0 - float64(4+2+1)/float64(10+1) // ~0.3636
	if score != expected {
		t.Errorf("expected %f, got %f", expected, score)
	}
}

func TestComputeOutcomeScore_EmptyStatus(t *testing.T) {
	score := computeOutcomeScore(ExecutionOutcome{})
	if score != 0.0 {
		t.Errorf("expected 0.0, got %f", score)
	}
}

func TestOutcomeToRaw_Fields(t *testing.T) {
	r, _ := NewOutcomeExperienceRecorder(&mockNormalizer{}, &mockOutcomeStore{})

	raw := r.outcomeToRaw(ExecutionOutcome{
		ExecutionID:    "e1",
		WorkflowID:     "wf-1",
		Status:         "completed",
		Duration:       1500,
		TotalSteps:     5,
		FailedSteps:    1,
		ToolCount:      3,
		MemoryHitCount: 2,
	})

	if raw.StrategyID != "e1" {
		t.Errorf("expected StrategyID e1, got %s", raw.StrategyID)
	}
	if raw.TaskType != "wf-1" {
		t.Errorf("expected TaskType wf-1, got %s", raw.TaskType)
	}
	if raw.Latency != int64(1500) {
		t.Errorf("expected Latency 1500, got %v", raw.Latency)
	}
	if raw.ErrorRate != 0.2 {
		t.Errorf("expected ErrorRate 0.2, got %v", raw.ErrorRate)
	}
	if raw.Success != true {
		t.Errorf("expected Success true, got %v", raw.Success)
	}
}
