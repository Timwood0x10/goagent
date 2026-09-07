package experience

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockCollectorNormalizer implements Normalizer for testing.
type mockCollectorNormalizer struct {
	normalizeFn      func(ctx context.Context, raw RawExperience) (NormalizedExperience, error)
	normalizeBatchFn func(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error)
}

func (m *mockCollectorNormalizer) Normalize(ctx context.Context, raw RawExperience) (NormalizedExperience, error) {
	if m.normalizeFn != nil {
		return m.normalizeFn(ctx, raw)
	}
	score, _ := raw.Score.(float64)
	return NormalizedExperience{
		StrategyID: raw.StrategyID,
		TaskType:   raw.TaskType,
		Score:      score,
		Outcome:    "collected",
	}, nil
}

func (m *mockCollectorNormalizer) NormalizeBatch(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error) {
	if m.normalizeBatchFn != nil {
		return m.normalizeBatchFn(ctx, raws)
	}
	out := make([]NormalizedExperience, 0, len(raws))
	for _, r := range raws {
		n, err := m.Normalize(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func TestNewToolCallExperienceCollector_NilNormalizer(t *testing.T) {
	_, err := NewToolCallExperienceCollector(nil, NewMockExperienceStore())
	if err == nil {
		t.Fatal("expected error for nil normalizer")
	}
}

func TestNewToolCallExperienceCollector_NilStore(t *testing.T) {
	_, err := NewToolCallExperienceCollector(&mockCollectorNormalizer{}, nil)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNewToolCallExperienceCollector_Success(t *testing.T) {
	c, err := NewToolCallExperienceCollector(&mockCollectorNormalizer{}, NewMockExperienceStore())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
}

func TestToolCallCollect_AppendCalledOnSuccess(t *testing.T) {
	store := NewMockExperienceStore()
	c, err := NewToolCallExperienceCollector(&mockCollectorNormalizer{}, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = c.Collect(context.Background(), ToolCallRecord{
		StrategyID: "s1",
		TaskType:   "code_gen",
		ToolName:   "web_search",
		Success:    true,
		Timestamp:  time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.experiences) != 1 {
		t.Fatalf("expected 1 experience in store, got %d", len(store.experiences))
	}
	if store.experiences[0].StrategyID != "s1" {
		t.Errorf("expected StrategyID s1, got %s", store.experiences[0].StrategyID)
	}
}

func TestToolCallCollect_FilteredExperience(t *testing.T) {
	store := NewMockExperienceStore()
	norm := &mockCollectorNormalizer{
		normalizeFn: func(ctx context.Context, raw RawExperience) (NormalizedExperience, error) {
			return NormalizedExperience{
				StrategyID:   raw.StrategyID,
				IsFiltered:   true,
				FilterReason: "low_confidence",
			}, nil
		},
	}
	c, err := NewToolCallExperienceCollector(norm, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = c.Collect(context.Background(), ToolCallRecord{StrategyID: "s1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.experiences) != 0 {
		t.Error("expected no experiences for filtered record")
	}
}

func TestToolCallCollect_NormalizerError(t *testing.T) {
	norm := &mockCollectorNormalizer{
		normalizeFn: func(ctx context.Context, raw RawExperience) (NormalizedExperience, error) {
			return NormalizedExperience{}, errors.New("normalize error")
		},
	}
	c, _ := NewToolCallExperienceCollector(norm, NewMockExperienceStore())
	err := c.Collect(context.Background(), ToolCallRecord{StrategyID: "s1"})
	if err == nil {
		t.Fatal("expected error from normalizer")
	}
}

func TestToolCallCollect_StoreError(t *testing.T) {
	store := NewMockExperienceStore()
	store.appendErr = errors.New("store error")

	c, err := NewToolCallExperienceCollector(&mockCollectorNormalizer{}, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = c.Collect(context.Background(), ToolCallRecord{StrategyID: "s1"})
	if err == nil {
		t.Fatal("expected error from store")
	}
}

func TestToolCallCollectBatch_MixedFiltered(t *testing.T) {
	store := NewMockExperienceStore()
	norm := &mockCollectorNormalizer{
		normalizeBatchFn: func(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error) {
			out := make([]NormalizedExperience, len(raws))
			for i, r := range raws {
				out[i] = NormalizedExperience{
					StrategyID: r.StrategyID,
					Score:      r.Score.(float64),
				}
				if i == 1 {
					out[i].IsFiltered = true
					out[i].FilterReason = "noise"
				}
			}
			return out, nil
		},
	}
	c, err := NewToolCallExperienceCollector(norm, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = c.CollectBatch(context.Background(), []ToolCallRecord{
		{StrategyID: "s1", ToolName: "t1"},
		{StrategyID: "s2", ToolName: "t2"}, // filtered
		{StrategyID: "s3", ToolName: "t3"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.experiences) != 2 {
		t.Fatalf("expected 2 experiences (one filtered out), got %d", len(store.experiences))
	}
	if store.experiences[0].StrategyID != "s1" {
		t.Errorf("expected first experience StrategyID s1, got %s", store.experiences[0].StrategyID)
	}
	if store.experiences[1].StrategyID != "s3" {
		t.Errorf("expected second experience StrategyID s3, got %s", store.experiences[1].StrategyID)
	}
}

func TestToolCallCollectBatch_AllFiltered(t *testing.T) {
	store := NewMockExperienceStore()
	norm := &mockCollectorNormalizer{
		normalizeBatchFn: func(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error) {
			out := make([]NormalizedExperience, len(raws))
			for i := range raws {
				out[i] = NormalizedExperience{
					IsFiltered:   true,
					FilterReason: "noise",
				}
			}
			return out, nil
		},
	}
	c, err := NewToolCallExperienceCollector(norm, store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = c.CollectBatch(context.Background(), []ToolCallRecord{
		{StrategyID: "s1"},
		{StrategyID: "s2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.experiences) != 0 {
		t.Error("expected no experiences when all are filtered")
	}
}

func TestToolCallCollectBatch_NormalizerError(t *testing.T) {
	norm := &mockCollectorNormalizer{
		normalizeBatchFn: func(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error) {
			return nil, errors.New("batch normalize error")
		},
	}
	c, _ := NewToolCallExperienceCollector(norm, NewMockExperienceStore())
	err := c.CollectBatch(context.Background(), []ToolCallRecord{{StrategyID: "s1"}})
	if err == nil {
		t.Fatal("expected error from normalizer")
	}
}

func TestToolCallCollect_NilReceiver(t *testing.T) {
	var c *ToolCallExperienceCollector
	err := c.Collect(context.Background(), ToolCallRecord{})
	if err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

func TestToolCallCollectBatch_NilReceiver(t *testing.T) {
	var c *ToolCallExperienceCollector
	err := c.CollectBatch(context.Background(), []ToolCallRecord{{}})
	if err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

func TestRecordToRaw_Success(t *testing.T) {
	now := time.Now()
	rec := ToolCallRecord{
		StrategyID:      "s1",
		TaskType:        "code_gen",
		ToolName:        "web_search",
		InputSummary:    "in",
		OutputSummary:   "out",
		LatencyMs:       150,
		Success:         true,
		Timestamp:       now,
		RetryCount:      2,
		ResultSizeBytes: 1024,
	}

	raw := recordToRaw(rec)

	if raw.StrategyID != "s1" {
		t.Errorf("expected StrategyID s1, got %s", raw.StrategyID)
	}
	if raw.TaskType != "code_gen" {
		t.Errorf("expected TaskType code_gen, got %s", raw.TaskType)
	}
	if got, ok := raw.Latency.(int64); !ok || got != 150 {
		t.Errorf("expected Latency 150, got %v (%T)", raw.Latency, raw.Latency)
	}
	if got, ok := raw.Score.(float64); !ok || got != 1.0 {
		t.Errorf("expected Score 1.0, got %v (%T)", raw.Score, raw.Score)
	}
	if got, ok := raw.Success.(bool); !ok || got != true {
		t.Errorf("expected Success true, got %v (%T)", raw.Success, raw.Success)
	}
	if raw.MutationType != "tool_call" {
		t.Errorf("expected MutationType tool_call, got %s", raw.MutationType)
	}
	if got, ok := raw.ErrorRate.(float64); !ok || got != 0.0 {
		t.Errorf("expected ErrorRate 0.0, got %v (%T)", raw.ErrorRate, raw.ErrorRate)
	}
	if got, ok := raw.Cost.(float64); !ok || got != 1024.0 {
		t.Errorf("expected Cost 1024.0, got %v (%T)", raw.Cost, raw.Cost)
	}
}

func TestRecordToRaw_Failure(t *testing.T) {
	rec := ToolCallRecord{
		StrategyID:      "s1",
		Success:         false,
		ErrorCode:       "TIMEOUT",
		RetryCount:      3,
		ResultSizeBytes: 512,
	}

	raw := recordToRaw(rec)

	if got, ok := raw.Score.(float64); !ok || got != 0.0 {
		t.Errorf("expected Score 0.0, got %v (%T)", raw.Score, raw.Score)
	}
	if got, ok := raw.Success.(bool); !ok || got != false {
		t.Errorf("expected Success false, got %v (%T)", raw.Success, raw.Success)
	}
	if got, ok := raw.ErrorRate.(float64); !ok || got != 1.0 {
		t.Errorf("expected ErrorRate 1.0, got %v (%T)", raw.ErrorRate, raw.ErrorRate)
	}
	expectedCost := float64(512 + 3*1024)
	if got, ok := raw.Cost.(float64); !ok || got != expectedCost {
		t.Errorf("expected Cost %f, got %v (%T)", expectedCost, raw.Cost, raw.Cost)
	}
}

func TestBoolToScore(t *testing.T) {
	if boolToScore(true) != 1.0 {
		t.Error("expected 1.0 for true")
	}
	if boolToScore(false) != 0.0 {
		t.Error("expected 0.0 for false")
	}
}
