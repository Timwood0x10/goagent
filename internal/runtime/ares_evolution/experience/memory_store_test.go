package experience

import (
	"context"
	"sync"
	"testing"
	"time"
)

// newTestExperience creates a test experience with default values.
func newTestExperience(id, strategyID, taskType string) NormalizedExperience {
	return NormalizedExperience{
		ID:         id,
		StrategyID: strategyID,
		TaskType:   taskType,
		Problem:    "test problem",
		Solution:   "test solution",
		Outcome:    "success",
		Score:      0.8,
		CreatedAt:  time.Now(),
		TenantID:   "tenant-1",
	}
}

// TestMemoryExperienceStore_NewMemoryExperienceStore tests store initialization.
func TestMemoryExperienceStore_NewMemoryExperienceStore(t *testing.T) {
	t.Run("without indexing", func(t *testing.T) {
		cfg := ExperienceStoreConfig{
			MaxSize: 100,
		}
		store := NewMemoryExperienceStore(cfg)
		if store == nil {
			t.Fatal("expected non-nil store")
		}
		if store.config.MaxSize != 100 {
			t.Errorf("expected MaxSize 100, got %d", store.config.MaxSize)
		}
		if store.indices != nil {
			t.Error("expected nil indices when indexing disabled")
		}
	})

	t.Run("with indexing", func(t *testing.T) {
		cfg := ExperienceStoreConfig{
			MaxSize:        100,
			EnableIndexing: true,
		}
		store := NewMemoryExperienceStore(cfg)
		if store == nil {
			t.Fatal("expected non-nil store")
		}
		if store.indices == nil {
			t.Fatal("expected non-nil indices when indexing enabled")
		}
		if store.indices.strategyIndex == nil {
			t.Error("expected non-nil strategyIndex")
		}
		if store.indices.taskTypeIndex == nil {
			t.Error("expected non-nil taskTypeIndex")
		}
	})
}

// TestMemoryExperienceStore_Append tests single experience append.
func TestMemoryExperienceStore_Append(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{}
	store := NewMemoryExperienceStore(cfg)

	t.Run("valid experience", func(t *testing.T) {
		exp := newTestExperience("exp-1", "strategy-1", "code_review")
		err := store.Append(ctx, exp)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid experience missing ID", func(t *testing.T) {
		exp := newTestExperience("", "strategy-1", "code_review")
		err := store.Append(ctx, exp)
		if err != ErrInvalidExperience {
			t.Errorf("expected ErrInvalidExperience, got %v", err)
		}
	})

	t.Run("invalid experience missing strategy ID", func(t *testing.T) {
		exp := newTestExperience("exp-2", "", "code_review")
		err := store.Append(ctx, exp)
		if err != ErrInvalidExperience {
			t.Errorf("expected ErrInvalidExperience, got %v", err)
		}
	})

	t.Run("invalid experience missing task type", func(t *testing.T) {
		exp := newTestExperience("exp-3", "strategy-1", "")
		err := store.Append(ctx, exp)
		if err != ErrInvalidExperience {
			t.Errorf("expected ErrInvalidExperience, got %v", err)
		}
	})

	t.Run("store full", func(t *testing.T) {
		cfg := ExperienceStoreConfig{MaxSize: 1}
		smallStore := NewMemoryExperienceStore(cfg)

		exp1 := newTestExperience("exp-1", "strategy-1", "code_review")
		err := smallStore.Append(ctx, exp1)
		if err != nil {
			t.Fatalf("unexpected error on first append: %v", err)
		}

		exp2 := newTestExperience("exp-2", "strategy-1", "code_review")
		err = smallStore.Append(ctx, exp2)
		if err != ErrStoreFull {
			t.Errorf("expected ErrStoreFull, got %v", err)
		}
	})
}

// TestMemoryExperienceStore_AppendBatch tests batch append.
func TestMemoryExperienceStore_AppendBatch(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{}
	store := NewMemoryExperienceStore(cfg)

	t.Run("empty batch", func(t *testing.T) {
		err := store.AppendBatch(ctx, []NormalizedExperience{})
		if err != nil {
			t.Errorf("unexpected error on empty batch: %v", err)
		}
	})

	t.Run("valid batch", func(t *testing.T) {
		exps := []NormalizedExperience{
			newTestExperience("exp-1", "strategy-1", "code_review"),
			newTestExperience("exp-2", "strategy-1", "bug_fix"),
			newTestExperience("exp-3", "strategy-2", "code_review"),
		}
		err := store.AppendBatch(ctx, exps)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("batch with invalid experience", func(t *testing.T) {
		exps := []NormalizedExperience{
			newTestExperience("exp-4", "strategy-1", "code_review"),
			newTestExperience("", "strategy-1", "bug_fix"),
		}
		err := store.AppendBatch(ctx, exps)
		if err != ErrInvalidExperience {
			t.Errorf("expected ErrInvalidExperience, got %v", err)
		}
	})

	t.Run("batch exceeds capacity", func(t *testing.T) {
		cfg := ExperienceStoreConfig{MaxSize: 2}
		smallStore := NewMemoryExperienceStore(cfg)

		exps := []NormalizedExperience{
			newTestExperience("exp-1", "strategy-1", "code_review"),
			newTestExperience("exp-2", "strategy-1", "bug_fix"),
			newTestExperience("exp-3", "strategy-2", "code_review"),
		}
		err := smallStore.AppendBatch(ctx, exps)
		if err != ErrStoreFull {
			t.Errorf("expected ErrStoreFull, got %v", err)
		}
	})
}

// TestMemoryExperienceStore_Query tests query by strategy and time range.
func TestMemoryExperienceStore_Query(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{EnableIndexing: true}
	store := NewMemoryExperienceStore(cfg)

	now := time.Now()
	baseTime := now.Add(-24 * time.Hour)

	// Add test experiences.
	exps := []NormalizedExperience{
		{
			ID:         "exp-1",
			StrategyID: "strategy-1",
			TaskType:   "code_review",
			Score:      0.8,
			CreatedAt:  baseTime,
		},
		{
			ID:         "exp-2",
			StrategyID: "strategy-1",
			TaskType:   "bug_fix",
			Score:      0.9,
			CreatedAt:  baseTime.Add(1 * time.Hour),
		},
		{
			ID:         "exp-3",
			StrategyID: "strategy-2",
			TaskType:   "code_review",
			Score:      0.7,
			CreatedAt:  baseTime.Add(2 * time.Hour),
		},
		{
			ID:         "exp-4",
			StrategyID: "strategy-1",
			TaskType:   "test",
			Score:      0.6,
			CreatedAt:  baseTime.Add(25 * time.Hour),
		},
	}

	for _, exp := range exps {
		err := store.Append(ctx, exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	t.Run("query by strategy", func(t *testing.T) {
		startTime := baseTime.Add(-1 * time.Hour)
		endTime := baseTime.Add(26 * time.Hour)

		results, err := store.Query(ctx, "strategy-1", startTime, endTime)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 results, got %d", len(results))
		}
	})

	t.Run("query by time range", func(t *testing.T) {
		startTime := baseTime
		endTime := baseTime.Add(2 * time.Hour)

		results, err := store.Query(ctx, "strategy-1", startTime, endTime)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("query no results", func(t *testing.T) {
		startTime := baseTime.Add(-1 * time.Hour)
		endTime := baseTime.Add(26 * time.Hour)

		results, err := store.Query(ctx, "strategy-999", startTime, endTime)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})

	t.Run("sorted by CreatedAt descending", func(t *testing.T) {
		startTime := baseTime.Add(-1 * time.Hour)
		endTime := baseTime.Add(26 * time.Hour)

		results, err := store.Query(ctx, "strategy-1", startTime, endTime)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 1; i < len(results); i++ {
			if results[i-1].CreatedAt.Before(results[i].CreatedAt) {
				t.Error("results not sorted by CreatedAt descending")
			}
		}
	})
}

// TestMemoryExperienceStore_QueryByTaskType tests query by task type.
func TestMemoryExperienceStore_QueryByTaskType(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{EnableIndexing: true}
	store := NewMemoryExperienceStore(cfg)

	// Add test experiences.
	exps := []NormalizedExperience{
		{
			ID:         "exp-1",
			StrategyID: "strategy-1",
			TaskType:   "code_review",
			Score:      0.8,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-2",
			StrategyID: "strategy-1",
			TaskType:   "bug_fix",
			Score:      0.9,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-3",
			StrategyID: "strategy-2",
			TaskType:   "code_review",
			Score:      0.7,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-4",
			StrategyID: "strategy-2",
			TaskType:   "code_review",
			Score:      0.95,
			CreatedAt:  time.Now(),
		},
	}

	for _, exp := range exps {
		err := store.Append(ctx, exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	t.Run("query by task type with limit", func(t *testing.T) {
		results, err := store.QueryByTaskType(ctx, "code_review", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("query by task type without limit", func(t *testing.T) {
		results, err := store.QueryByTaskType(ctx, "code_review", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 results, got %d", len(results))
		}
	})

	t.Run("sorted by Score descending", func(t *testing.T) {
		results, err := store.QueryByTaskType(ctx, "code_review", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for i := 1; i < len(results); i++ {
			if results[i-1].Score < results[i].Score {
				t.Error("results not sorted by Score descending")
			}
		}
	})

	t.Run("query non-existent task type", func(t *testing.T) {
		results, err := store.QueryByTaskType(ctx, "nonexistent", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	})
}

// TestMemoryExperienceStore_GetStatistics tests statistics calculation.
func TestMemoryExperienceStore_GetStatistics(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{EnableIndexing: true}
	store := NewMemoryExperienceStore(cfg)

	// Add test experiences.
	exps := []NormalizedExperience{
		{
			ID:         "exp-1",
			StrategyID: "strategy-1",
			TaskType:   "code_review",
			Score:      0.8,
			Outcome:    "success",
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-2",
			StrategyID: "strategy-1",
			TaskType:   "bug_fix",
			Score:      0.9,
			Outcome:    "success",
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-3",
			StrategyID: "strategy-1",
			TaskType:   "code_review",
			Score:      0.7,
			Outcome:    "partial",
			CreatedAt:  time.Now(),
		},
		{
			ID:         "exp-4",
			StrategyID: "strategy-2",
			TaskType:   "code_review",
			Score:      0.6,
			Outcome:    "failure",
			CreatedAt:  time.Now(),
		},
	}

	for _, exp := range exps {
		err := store.Append(ctx, exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	t.Run("statistics for existing strategy", func(t *testing.T) {
		stats, err := store.GetStatistics(ctx, "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if stats["total_experiences"] != 3 {
			t.Errorf("expected total_experiences 3, got %v", stats["total_experiences"])
		}

		expectedAvg := (0.8 + 0.9 + 0.7) / 3
		// Allow small floating-point precision difference
		if stats["avg_score"] < expectedAvg-0.0001 || stats["avg_score"] > expectedAvg+0.0001 {
			t.Errorf("expected avg_score %v, got %v", expectedAvg, stats["avg_score"])
		}

		if stats["success_rate"] != 2.0/3.0 {
			t.Errorf("expected success_rate %v, got %v", 2.0/3.0, stats["success_rate"])
		}
	})

	t.Run("statistics for non-existent strategy", func(t *testing.T) {
		stats, err := store.GetStatistics(ctx, "strategy-999")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(stats) != 0 {
			t.Errorf("expected empty stats, got %v", stats)
		}
	})
}

// TestMemoryExperienceStore_Concurrency tests thread-safety.
func TestMemoryExperienceStore_Concurrency(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{EnableIndexing: true}
	store := NewMemoryExperienceStore(cfg)

	const numGoroutines = 10
	const numOpsPerGoroutine = 50

	var wg sync.WaitGroup

	// Concurrent writes.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				exp := newTestExperience(
					"exp-"+string(rune(id*numOpsPerGoroutine+j)),
					"strategy-1",
					"code_review",
				)
				_ = store.Append(ctx, exp)
			}
		}(i)
	}

	// Concurrent reads.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				_, _ = store.Query(ctx, "strategy-1", time.Now().Add(-24*time.Hour), time.Now())
				_, _ = store.QueryByTaskType(ctx, "code_review", 10)
				_, _ = store.GetStatistics(ctx, "strategy-1")
			}
		}()
	}

	wg.Wait()

	// Verify all writes succeeded.
	stats, err := store.GetStatistics(ctx, "strategy-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedTotal := float64(numGoroutines * numOpsPerGoroutine)
	if stats["total_experiences"] != expectedTotal {
		t.Errorf("expected total_experiences %v, got %v", expectedTotal, stats["total_experiences"])
	}
}

// TestMemoryExperienceStore_ContextCancellation tests context handling.
func TestMemoryExperienceStore_ContextCancellation(t *testing.T) {
	cfg := ExperienceStoreConfig{}
	store := NewMemoryExperienceStore(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exp := newTestExperience("exp-1", "strategy-1", "code_review")

	t.Run("Append with cancelled context", func(t *testing.T) {
		err := store.Append(ctx, exp)
		if err == nil {
			t.Error("expected error with cancelled context")
		}
	})

	t.Run("AppendBatch with cancelled context", func(t *testing.T) {
		err := store.AppendBatch(ctx, []NormalizedExperience{exp})
		if err == nil {
			t.Error("expected error with cancelled context")
		}
	})

	t.Run("Query with cancelled context", func(t *testing.T) {
		_, err := store.Query(ctx, "strategy-1", time.Now(), time.Now())
		if err == nil {
			t.Error("expected error with cancelled context")
		}
	})

	t.Run("QueryByTaskType with cancelled context", func(t *testing.T) {
		_, err := store.QueryByTaskType(ctx, "code_review", 10)
		if err == nil {
			t.Error("expected error with cancelled context")
		}
	})

	t.Run("GetStatistics with cancelled context", func(t *testing.T) {
		_, err := store.GetStatistics(ctx, "strategy-1")
		if err == nil {
			t.Error("expected error with cancelled context")
		}
	})
}

// TestMemoryExperienceStore_WithoutIndexing tests operations without indexing.
func TestMemoryExperienceStore_WithoutIndexing(t *testing.T) {
	ctx := context.Background()
	cfg := ExperienceStoreConfig{EnableIndexing: false}
	store := NewMemoryExperienceStore(cfg)

	// Add test experiences.
	exps := []NormalizedExperience{
		newTestExperience("exp-1", "strategy-1", "code_review"),
		newTestExperience("exp-2", "strategy-1", "bug_fix"),
		newTestExperience("exp-3", "strategy-2", "code_review"),
	}

	for _, exp := range exps {
		err := store.Append(ctx, exp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	t.Run("Query works without indexing", func(t *testing.T) {
		results, err := store.Query(ctx, "strategy-1", time.Now().Add(-1*time.Hour), time.Now().Add(1*time.Hour))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("QueryByTaskType works without indexing", func(t *testing.T) {
		results, err := store.QueryByTaskType(ctx, "code_review", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results, got %d", len(results))
		}
	})

	t.Run("GetStatistics works without indexing", func(t *testing.T) {
		stats, err := store.GetStatistics(ctx, "strategy-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stats["total_experiences"] != 2 {
			t.Errorf("expected total_experiences 2, got %v", stats["total_experiences"])
		}
	})
}
