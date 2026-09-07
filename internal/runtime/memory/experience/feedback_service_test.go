package experience

import (
	"context"
	"errors"
	"testing"

	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// mockExperienceRepo is a mock implementation of ExperienceRepositoryInterface for testing.
type mockExperienceRepo struct {
	incrementCalls []string
	decrementCalls []string
	err            error
}

func (m *mockExperienceRepo) Create(ctx context.Context, exp *storage_models.Experience) error {
	return nil
}

func (m *mockExperienceRepo) GetByID(ctx context.Context, tenantID, id string) (*storage_models.Experience, error) {
	return nil, errors.New("not implemented in mock")
}

func (m *mockExperienceRepo) Update(ctx context.Context, exp *storage_models.Experience) error {
	return nil
}

func (m *mockExperienceRepo) UpdateEmbedding(
	ctx context.Context,
	tenantID, id string,
	embedding []float64,
	model string,
	version int,
) error {
	return nil
}

func (m *mockExperienceRepo) Delete(ctx context.Context, id, tenantID string) error {
	return nil
}

func (m *mockExperienceRepo) SearchByVector(ctx context.Context, embedding []float64, tenantID string, limit int) ([]*storage_models.Experience, error) {
	return nil, nil
}

func (m *mockExperienceRepo) SearchByKeyword(ctx context.Context, query, tenantID string, limit int) ([]*storage_models.Experience, error) {
	return nil, nil
}

func (m *mockExperienceRepo) IncrementUsageCount(ctx context.Context, tenantID, id string) error {
	// Always record the call, even if returning an error.
	m.incrementCalls = append(m.incrementCalls, id)
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockExperienceRepo) DecrementRank(ctx context.Context, tenantID, id string) error {
	// Always record the call, even if returning an error.
	m.decrementCalls = append(m.decrementCalls, id)
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockExperienceRepo) ListByType(ctx context.Context, expType, tenantID string, limit int) ([]*storage_models.Experience, error) {
	return nil, nil
}

func (m *mockExperienceRepo) ListByAgent(ctx context.Context, agentID, tenantID string, limit int) ([]*storage_models.Experience, error) {
	return nil, nil
}

// Ensure mockExperienceRepo implements the interface.
var _ repositories.ExperienceRepositoryInterface = (*mockExperienceRepo)(nil)

func TestFeedbackService_RecordSuccess(t *testing.T) {
	tests := []struct {
		name         string
		experienceID string
		wantErr      bool
		expectCall   bool
		mockErr      error
	}{
		{
			name:         "valid experience ID increments count",
			experienceID: "exp-123",
			wantErr:      false,
			expectCall:   true,
		},
		{
			name:         "empty experience ID is no-op",
			experienceID: "",
			wantErr:      false,
			expectCall:   false,
		},
		{
			name:         "repository error returns error",
			experienceID: "exp-456",
			wantErr:      true,
			expectCall:   true,
			mockErr:      errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockExperienceRepo{err: tt.mockErr}
			svc := NewFeedbackService(repo)

			err := svc.RecordSuccess(context.Background(), "t1", tt.experienceID)

			if (err != nil) != tt.wantErr {
				t.Errorf("RecordSuccess() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.expectCall && len(repo.incrementCalls) != 1 {
				t.Errorf("expected 1 increment call, got %d", len(repo.incrementCalls))
			}
			if !tt.expectCall && len(repo.incrementCalls) != 0 {
				t.Errorf("expected 0 increment calls, got %d", len(repo.incrementCalls))
			}
		})
	}
}

func TestFeedbackService_RecordFailure(t *testing.T) {
	tests := []struct {
		name         string
		experienceID string
		wantErr      bool
		expectCall   bool
		mockErr      error
	}{
		{
			name:         "valid experience ID decrements rank",
			experienceID: "exp-123",
			wantErr:      false,
			expectCall:   true,
		},
		{
			name:         "empty experience ID is no-op",
			experienceID: "",
			wantErr:      false,
			expectCall:   false,
		},
		{
			name:         "repository error returns error",
			experienceID: "exp-456",
			wantErr:      true,
			expectCall:   true,
			mockErr:      errors.New("db error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockExperienceRepo{err: tt.mockErr}
			svc := NewFeedbackService(repo)

			err := svc.RecordFailure(context.Background(), "t1", tt.experienceID)

			if (err != nil) != tt.wantErr {
				t.Errorf("RecordFailure() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.expectCall && len(repo.decrementCalls) != 1 {
				t.Errorf("expected 1 decrement call, got %d", len(repo.decrementCalls))
			}
			if !tt.expectCall && len(repo.decrementCalls) != 0 {
				t.Errorf("expected 0 decrement calls, got %d", len(repo.decrementCalls))
			}
		})
	}
}

func TestFeedbackService_RecordFeedback(t *testing.T) {
	tests := []struct {
		name         string
		experienceID string
		success      bool
		wantErr      bool
		expectInc    bool
		expectDec    bool
	}{
		{
			name:         "success calls RecordSuccess",
			experienceID: "exp-123",
			success:      true,
			wantErr:      false,
			expectInc:    true,
			expectDec:    false,
		},
		{
			name:         "failure calls RecordFailure",
			experienceID: "exp-456",
			success:      false,
			wantErr:      false,
			expectInc:    false,
			expectDec:    true,
		},
		{
			name:         "empty ID is no-op regardless of success",
			experienceID: "",
			success:      true,
			wantErr:      false,
			expectInc:    false,
			expectDec:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockExperienceRepo{}
			svc := NewFeedbackService(repo)

			err := svc.RecordFeedback(context.Background(), "t1", tt.experienceID, tt.success)

			if (err != nil) != tt.wantErr {
				t.Errorf("RecordFeedback() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.expectInc && len(repo.incrementCalls) != 1 {
				t.Errorf("expected 1 increment call, got %d", len(repo.incrementCalls))
			}
			if !tt.expectInc && len(repo.incrementCalls) != 0 {
				t.Errorf("expected 0 increment calls, got %d", len(repo.incrementCalls))
			}
			if tt.expectDec && len(repo.decrementCalls) != 1 {
				t.Errorf("expected 1 decrement call, got %d", len(repo.decrementCalls))
			}
			if !tt.expectDec && len(repo.decrementCalls) != 0 {
				t.Errorf("expected 0 decrement calls, got %d", len(repo.decrementCalls))
			}
		})
	}
}
