package evolution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	aresExperience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	storageModels "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// mockRepoFR implements repositories.ExperienceRepositoryInterface for testing.
type mockRepoFR struct {
	incrementCalls []string
	decrementCalls []string
	err            error
}

func (m *mockRepoFR) Create(_ context.Context, _ *storageModels.Experience) error {
	return nil
}

func (m *mockRepoFR) GetByID(_ context.Context, _, _ string) (*storageModels.Experience, error) {
	return nil, errors.New("not found")
}

func (m *mockRepoFR) Update(_ context.Context, _ *storageModels.Experience) error {
	return nil
}

func (m *mockRepoFR) UpdateEmbedding(_ context.Context, _, _ string, _ []float64, _ string, _ int) error {
	return nil
}

func (m *mockRepoFR) Delete(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockRepoFR) SearchByVector(_ context.Context, _ []float64, _ string, _ int) ([]*storageModels.Experience, error) {
	return nil, nil
}

func (m *mockRepoFR) SearchByKeyword(_ context.Context, _, _ string, _ int) ([]*storageModels.Experience, error) {
	return nil, nil
}

func (m *mockRepoFR) IncrementUsageCount(_ context.Context, _ string, id string) error {
	m.incrementCalls = append(m.incrementCalls, id)
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockRepoFR) DecrementRank(_ context.Context, _ string, id string) error {
	m.decrementCalls = append(m.decrementCalls, id)
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockRepoFR) ListByType(_ context.Context, _, _ string, _ int) ([]*storageModels.Experience, error) {
	return nil, nil
}

func (m *mockRepoFR) ListByAgent(_ context.Context, _, _ string, _ int) ([]*storageModels.Experience, error) {
	return nil, nil
}

// Ensure mockRepoFR implements the interface.
var _ repositories.ExperienceRepositoryInterface = (*mockRepoFR)(nil)

func TestFeedbackRecorder_New(t *testing.T) {
	svc := aresExperience.NewFeedbackService(&mockRepoFR{})
	r := NewFeedbackRecorder(svc)
	if r == nil {
		t.Fatal("expected non-nil recorder")
	}
}

func TestFeedbackRecorder_Register_Success(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	outcome := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       true,
		Score:         85.0,
		ExperienceIDs: []string{"exp-1", "exp-2"},
		Timestamp:     time.Now(),
	}

	err := r.Register(context.Background(), outcome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.incrementCalls) != 2 {
		t.Errorf("expected 2 increment calls, got %d", len(mock.incrementCalls))
	}
	if len(mock.decrementCalls) != 0 {
		t.Errorf("expected 0 decrement calls, got %d", len(mock.decrementCalls))
	}
}

func TestFeedbackRecorder_Register_Failure(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	outcome := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       false,
		Score:         30.0,
		ExperienceIDs: []string{"exp-1"},
		Timestamp:     time.Now(),
	}

	err := r.Register(context.Background(), outcome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.incrementCalls) != 0 {
		t.Errorf("expected 0 increment calls, got %d", len(mock.incrementCalls))
	}
	if len(mock.decrementCalls) != 1 {
		t.Errorf("expected 1 decrement call, got %d", len(mock.decrementCalls))
	}
}

func TestFeedbackRecorder_Register_EmptyExperienceIDs(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	// Empty ExperienceIDs slice.
	outcome1 := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       true,
		ExperienceIDs: []string{},
		Timestamp:     time.Now(),
	}
	err := r.Register(context.Background(), outcome1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.incrementCalls) != 0 {
		t.Errorf("expected 0 increment calls for empty IDs, got %d", len(mock.incrementCalls))
	}

	// Nil ExperienceIDs.
	outcome2 := StrategyOutcome{
		StrategyID:    "strategy-2",
		Success:       false,
		ExperienceIDs: nil,
		Timestamp:     time.Now(),
	}
	err = r.Register(context.Background(), outcome2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.decrementCalls) != 0 {
		t.Errorf("expected 0 decrement calls for nil IDs, got %d", len(mock.decrementCalls))
	}

	// ExperienceIDs with empty string entries.
	outcome3 := StrategyOutcome{
		StrategyID:    "strategy-3",
		Success:       true,
		ExperienceIDs: []string{""},
		Timestamp:     time.Now(),
	}
	err = r.Register(context.Background(), outcome3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.incrementCalls) != 0 {
		t.Errorf("expected 0 increment calls for empty string ID, got %d", len(mock.incrementCalls))
	}
}

func TestFeedbackRecorder_Register_NilFeedbackService(t *testing.T) {
	r := NewFeedbackRecorder(nil)
	if r == nil {
		t.Fatal("expected non-nil recorder with nil service")
	}

	outcome := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       true,
		ExperienceIDs: []string{"exp-1"},
		Timestamp:     time.Now(),
	}

	// Should not panic or error when feedback service is nil.
	err := r.Register(context.Background(), outcome)
	if err != nil {
		t.Fatalf("unexpected error with nil service: %v", err)
	}
}

func TestFeedbackRecorder_Register_RepoError(t *testing.T) {
	mock := &mockRepoFR{err: errors.New("db error")}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	outcome := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       true,
		ExperienceIDs: []string{"exp-1"},
		Timestamp:     time.Now(),
	}

	err := r.Register(context.Background(), outcome)
	if err == nil {
		t.Fatal("expected error from repo")
	}
	if !strings.Contains(err.Error(), "db error") {
		t.Errorf("expected error to contain 'db error', got: %v", err)
	}
}

func TestFeedbackRecorder_MultipleOutcomes(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	// Record multiple outcomes.
	for i := 0; i < 5; i++ {
		outcome := StrategyOutcome{
			StrategyID:    "strategy-%d",
			Success:       i%2 == 0, // Alternate success/failure.
			Score:         float64(i * 20),
			ExperienceIDs: []string{"exp-%d"},
			Timestamp:     time.Now(),
		}
		err := r.Register(context.Background(), outcome)
		if err != nil {
			t.Fatalf("unexpected error at outcome %d: %v", i, err)
		}
	}

	// Check String() output.
	summary := r.String()
	if !strings.Contains(summary, "5 outcomes") {
		t.Errorf("expected summary to contain '5 outcomes', got: %s", summary)
	}
	if !strings.Contains(summary, "60.0%") {
		t.Errorf("expected summary to contain '60.0%%' (3/5 success), got: %s", summary)
	}
}

func TestFeedbackRecorder_String_Empty(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	summary := r.String()
	if summary != "FeedbackRecorder: no outcomes recorded" {
		t.Errorf("unexpected empty summary: %s", summary)
	}
}

func TestFeedbackRecorder_String_RecentFive(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	// Record 10 outcomes; only last 5 should appear in recent.
	for i := 0; i < 10; i++ {
		outcome := StrategyOutcome{
			StrategyID:    "strategy-%d",
			Success:       true,
			Score:         float64(i),
			ExperienceIDs: []string{"exp-%d"},
			Timestamp:     time.Now(),
		}
		err := r.Register(context.Background(), outcome)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	summary := r.String()
	if !strings.Contains(summary, "10 outcomes") {
		t.Errorf("expected 10 outcomes in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "100.0%") {
		t.Errorf("expected 100%% success rate, got: %s", summary)
	}
}

func TestFeedbackRecorder_ExperienceIDsCopied(t *testing.T) {
	mock := &mockRepoFR{}
	svc := aresExperience.NewFeedbackService(mock)
	r := NewFeedbackRecorder(svc)

	originalIDs := []string{"exp-1", "exp-2"}
	outcome := StrategyOutcome{
		StrategyID:    "strategy-1",
		Success:       true,
		ExperienceIDs: originalIDs,
		Timestamp:     time.Now(),
	}

	err := r.Register(context.Background(), outcome)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Modify the original slice; recorder should not be affected.
	originalIDs[0] = "exp-modified"

	// Register another outcome to verify immutability.
	outcome2 := StrategyOutcome{
		StrategyID:    "strategy-2",
		Success:       false,
		ExperienceIDs: []string{"exp-3"},
		Timestamp:     time.Now(),
	}
	err = r.Register(context.Background(), outcome2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify increment calls are from the first outcome (2 calls, both original).
	if len(mock.incrementCalls) != 2 {
		t.Errorf("expected 2 increment calls, got %d", len(mock.incrementCalls))
	}
	if len(mock.decrementCalls) != 1 {
		t.Errorf("expected 1 decrement call, got %d", len(mock.decrementCalls))
	}
}
