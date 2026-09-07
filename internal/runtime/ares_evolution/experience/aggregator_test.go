package experience

import (
	"context"
	"sync"
	"testing"
	"time"
)

// MockExperienceStore implements ExperienceStore for testing.
type MockExperienceStore struct {
	mu           sync.RWMutex
	experiences  []NormalizedExperience
	appendErr    error
	queryFunc    func(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error)
	taskTypeFunc func(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error)
}

// NewMockExperienceStore creates a MockExperienceStore.
func NewMockExperienceStore() *MockExperienceStore {
	return &MockExperienceStore{
		experiences: make([]NormalizedExperience, 0),
	}
}

func (m *MockExperienceStore) Append(ctx context.Context, exp NormalizedExperience) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.experiences = append(m.experiences, exp)
	return nil
}

func (m *MockExperienceStore) AppendBatch(ctx context.Context, exps []NormalizedExperience) error {
	if m.appendErr != nil {
		return m.appendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.experiences = append(m.experiences, exps...)
	return nil
}

func (m *MockExperienceStore) Query(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error) {
	if m.queryFunc != nil {
		return m.queryFunc(ctx, strategyID, startTime, endTime)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []NormalizedExperience
	for _, exp := range m.experiences {
		if exp.StrategyID == strategyID &&
			!exp.CreatedAt.Before(startTime) &&
			!exp.CreatedAt.After(endTime) {
			result = append(result, exp)
		}
	}
	return result, nil
}

func (m *MockExperienceStore) QueryByTaskType(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error) {
	if m.taskTypeFunc != nil {
		return m.taskTypeFunc(ctx, taskType, limit)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []NormalizedExperience
	for _, exp := range m.experiences {
		if exp.TaskType == taskType {
			result = append(result, exp)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MockExperienceStore) GetStatistics(ctx context.Context, strategyID string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func (m *MockExperienceStore) GetTaskTypeStatistics(ctx context.Context, taskType string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

// SetQueryFunc sets a custom query function.
func (m *MockExperienceStore) SetQueryFunc(f func(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error)) {
	m.queryFunc = f
}

// SetTaskTypeFunc sets a custom task type query function.
func (m *MockExperienceStore) SetTaskTypeFunc(f func(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error)) {
	m.taskTypeFunc = f
}

// AddExperience adds a normalized experience to the mock store.
func (m *MockExperienceStore) AddExperience(exp NormalizedExperience) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.experiences = append(m.experiences, exp)
}

// newTestNormalizedExp creates a test NormalizedExperience.
func newTestNormalizedExp(id, strategyID, taskType string, score float64) NormalizedExperience {
	return NormalizedExperience{
		ID:         id,
		StrategyID: strategyID,
		TaskType:   taskType,
		Score:      score,
		Success:    true,
		Outcome:    "success",
		CreatedAt:  time.Now(),
	}
}

func TestNewDefaultEvidenceAggregator(t *testing.T) {
	t.Run("with default config", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)
		if agg == nil {
			t.Fatal("expected non-nil aggregator")
		}
		if agg.cache == nil {
			t.Error("expected cache to be enabled by default")
		}
	})

	t.Run("with custom config", func(t *testing.T) {
		store := NewMockExperienceStore()
		config := DefaultAggregatorConfig()
		config.EnableCache = false
		agg := NewDefaultEvidenceAggregator(store, config)
		if agg == nil {
			t.Fatal("expected non-nil aggregator")
		}
		if agg.cache != nil {
			t.Error("expected cache to be nil when disabled")
		}
	})

	t.Run("with cache disabled", func(t *testing.T) {
		store := NewMockExperienceStore()
		config := &AggregatorConfig{EnableCache: false}
		agg := NewDefaultEvidenceAggregator(store, config)
		if agg.cache != nil {
			t.Error("expected cache to be nil for disabled config")
		}
	})
}

func TestAggregate(t *testing.T) {
	now := time.Now()

	t.Run("empty store returns zero-sample evidence", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)

		ev, err := agg.Aggregate(context.Background(), "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 0 {
			t.Errorf("expected SampleCount 0, got %d", ev.SampleCount)
		}
		if ev.StrategyID != "strategy-1" {
			t.Errorf("expected StrategyID strategy-1, got %s", ev.StrategyID)
		}
	})

	t.Run("aggregates experiences correctly", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-1", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success", LatencyMs: 100, CreatedAt: now,
		})
		store.AddExperience(NormalizedExperience{
			ID: "exp-2", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.6, Success: false, Outcome: "failure", LatencyMs: 500, CreatedAt: now,
		})
		store.AddExperience(NormalizedExperience{
			ID: "exp-3", StrategyID: "strategy-2", TaskType: "bug_fix",
			Score: 0.9, Success: true, Outcome: "success", LatencyMs: 50, CreatedAt: now,
		})

		agg := NewDefaultEvidenceAggregator(store, nil)

		ev, err := agg.Aggregate(context.Background(), "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 2 {
			t.Errorf("expected SampleCount 2, got %d", ev.SampleCount)
		}
		if ev.SuccessRate != 0.5 {
			t.Errorf("expected SuccessRate 0.5, got %f", ev.SuccessRate)
		}
	})

	t.Run("returns error for empty strategy id", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.Aggregate(context.Background(), "")
		if err == nil {
			t.Error("expected error for empty strategy id")
		}
	})

	t.Run("handles store error", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.SetQueryFunc(func(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error) {
			return nil, context.DeadlineExceeded
		})
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.Aggregate(context.Background(), "strategy-1")
		if err == nil {
			t.Error("expected error from store")
		}
	})

	t.Run("uses cache on second call", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(newTestNormalizedExp("exp-1", "strategy-1", "code_review", 0.8))

		callCount := 0
		store.SetQueryFunc(func(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error) {
			callCount++
			return store.experiences, nil
		})

		agg := NewDefaultEvidenceAggregator(store, nil)

		_, err := agg.Aggregate(context.Background(), "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		_, err = agg.Aggregate(context.Background(), "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if callCount != 1 {
			t.Errorf("expected 1 store call (cached), got %d", callCount)
		}
	})
}

func TestAggregateByTaskType(t *testing.T) {
	now := time.Now()

	t.Run("empty store returns empty evidence", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)

		ev, err := agg.AggregateByTaskType(context.Background(), "code_review")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ev.IsEmpty() {
			t.Errorf("expected empty evidence, got %+v", ev)
		}
	})

	t.Run("aggregates experiences across strategies", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-1", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success", LatencyMs: 100, CreatedAt: now,
		})
		store.AddExperience(NormalizedExperience{
			ID: "exp-2", StrategyID: "strategy-1", TaskType: "bug_fix",
			Score: 0.9, Success: true, Outcome: "success", LatencyMs: 50, CreatedAt: now,
		})
		store.AddExperience(NormalizedExperience{
			ID: "exp-3", StrategyID: "strategy-2", TaskType: "code_review",
			Score: 0.7, Success: false, Outcome: "failure", LatencyMs: 300, CreatedAt: now,
		})

		agg := NewDefaultEvidenceAggregator(store, nil)

		ev, err := agg.AggregateByTaskType(context.Background(), "code_review")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 2 {
			t.Errorf("expected SampleCount 2, got %d", ev.SampleCount)
		}
		if ev.SuccessRate != 0.5 {
			t.Errorf("expected SuccessRate 0.5, got %f", ev.SuccessRate)
		}
	})

	t.Run("returns error for empty task type", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.AggregateByTaskType(context.Background(), "")
		if err == nil {
			t.Error("expected error for empty task type")
		}
	})

	t.Run("handles store error", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.SetTaskTypeFunc(func(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error) {
			return nil, context.DeadlineExceeded
		})
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.AggregateByTaskType(context.Background(), "code_review")
		if err == nil {
			t.Error("expected error from store")
		}
	})
}

func TestAggregateByTimeWindow(t *testing.T) {
	now := time.Now()

	t.Run("hourly window", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-1", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success", CreatedAt: now,
		})

		agg := NewDefaultEvidenceAggregator(store, nil)
		ev, err := agg.AggregateByTimeWindow(context.Background(), "strategy-1", "hourly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 1 {
			t.Errorf("expected SampleCount 1, got %d", ev.SampleCount)
		}
	})

	t.Run("daily window", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-1", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success", CreatedAt: now,
		})

		agg := NewDefaultEvidenceAggregator(store, nil)
		ev, err := agg.AggregateByTimeWindow(context.Background(), "strategy-1", "daily")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 1 {
			t.Errorf("expected SampleCount 1, got %d", ev.SampleCount)
		}
	})

	t.Run("weekly window", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-1", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success", CreatedAt: now,
		})

		agg := NewDefaultEvidenceAggregator(store, nil)
		ev, err := agg.AggregateByTimeWindow(context.Background(), "strategy-1", "weekly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 1 {
			t.Errorf("expected SampleCount 1, got %d", ev.SampleCount)
		}
	})

	t.Run("invalid window returns error", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.AggregateByTimeWindow(context.Background(), "strategy-1", "invalid")
		if err == nil {
			t.Error("expected error for invalid window")
		}
	})

	t.Run("returns error for empty strategy id", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)
		_, err := agg.AggregateByTimeWindow(context.Background(), "", "daily")
		if err == nil {
			t.Error("expected error for empty strategy id")
		}
	})

	t.Run("filters experiences outside window", func(t *testing.T) {
		store := NewMockExperienceStore()
		store.AddExperience(NormalizedExperience{
			ID: "exp-old", StrategyID: "strategy-1", TaskType: "code_review",
			Score: 0.8, Success: true, Outcome: "success",
			CreatedAt: now.Add(-48 * time.Hour),
		})

		agg := NewDefaultEvidenceAggregator(store, nil)
		ev, err := agg.AggregateByTimeWindow(context.Background(), "strategy-1", "daily")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ev.SampleCount != 0 {
			t.Errorf("expected 0 samples (outside window), got %d", ev.SampleCount)
		}
	})
}

func TestRefreshAll(t *testing.T) {
	t.Run("clears cache", func(t *testing.T) {
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, nil)

		// Force an entry into the cache.
		agg.setCachedByStrategy("strategy-1", Evidence{
			StrategyID: "strategy-1", SampleCount: 5,
		})

		err := agg.RefreshAll(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		_, ok := agg.getCachedByStrategy("strategy-1")
		if ok {
			t.Error("expected cache to be cleared")
		}
	})

	t.Run("no-op when cache disabled", func(t *testing.T) {
		config := &AggregatorConfig{EnableCache: false}
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, config)

		err := agg.RefreshAll(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestCalculateConfidence(t *testing.T) {
	t.Run("zero sample count returns zero confidence", func(t *testing.T) {
		config := DefaultAggregatorConfig()
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, config)

		confidence := agg.calculateConfidence(0)
		if confidence != 0.0 {
			t.Errorf("expected 0.0, got %f", confidence)
		}
	})

	t.Run("small sample count scales confidence down", func(t *testing.T) {
		config := DefaultAggregatorConfig()
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, config)

		confidence := agg.calculateConfidence(1)
		if confidence >= 0.5 {
			t.Errorf("expected confidence < 0.5 for single sample, got %f", confidence)
		}
	})

	t.Run("at min threshold gives 0.5 confidence", func(t *testing.T) {
		config := &AggregatorConfig{
			MinSampleCount: 10,
			MaxSampleCount: 100,
		}
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, config)

		confidence := agg.calculateConfidence(10)
		if confidence != 0.5 {
			t.Errorf("expected 0.5, got %f", confidence)
		}
	})

	t.Run("large sample count returns full confidence", func(t *testing.T) {
		config := DefaultAggregatorConfig()
		store := NewMockExperienceStore()
		agg := NewDefaultEvidenceAggregator(store, config)

		confidence := agg.calculateConfidence(10000)
		if confidence != 1.0 {
			t.Errorf("expected 1.0, got %f", confidence)
		}
	})
}
