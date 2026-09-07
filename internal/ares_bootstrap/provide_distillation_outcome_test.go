// Package ares_bootstrap — strategy outcome write-back tests.
//
// Verifies that GA strategy outcomes are persisted into the experience store
// instead of being silently dropped (the previous nil-RecordFunc no-op), so the
// Strategy → Experience → Guidance loop closes with real data.
package ares_bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// mockExpRepo records created experiences and can inject a failure.
type mockExpRepo struct {
	created  []*models.Experience
	createFn func(ctx context.Context, exp *models.Experience) error
}

func (m *mockExpRepo) Create(ctx context.Context, exp *models.Experience) error {
	if m.createFn != nil {
		return m.createFn(ctx, exp)
	}
	m.created = append(m.created, exp)
	return nil
}

func (m *mockExpRepo) GetByID(ctx context.Context, tenantID, id string) (*models.Experience, error) {
	return nil, errors.New("not implemented")
}

func (m *mockExpRepo) Update(ctx context.Context, exp *models.Experience) error {
	return errors.New("not implemented")
}

func (m *mockExpRepo) UpdateEmbedding(context.Context, string, string, []float64, string, int) error {
	return errors.New("not implemented")
}

func (m *mockExpRepo) Delete(ctx context.Context, id, tenantID string) error {
	return errors.New("not implemented")
}

func (m *mockExpRepo) SearchByVector(ctx context.Context, embedding []float64, tenantID string, limit int) ([]*models.Experience, error) {
	return nil, nil
}

func (m *mockExpRepo) SearchByKeyword(ctx context.Context, query, tenantID string, limit int) ([]*models.Experience, error) {
	return nil, nil
}

func (m *mockExpRepo) IncrementUsageCount(ctx context.Context, tenantID, id string) error {
	return errors.New("not implemented")
}

func (m *mockExpRepo) DecrementRank(ctx context.Context, tenantID, id string) error {
	return errors.New("not implemented")
}

func (m *mockExpRepo) ListByType(ctx context.Context, expType, tenantID string, limit int) ([]*models.Experience, error) {
	return nil, nil
}

func (m *mockExpRepo) ListByAgent(ctx context.Context, agentID, tenantID string, limit int) ([]*models.Experience, error) {
	return nil, nil
}

func (m *mockExpRepo) DeleteExpired(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockExpRepo) CountByType(ctx context.Context, expType string) (int64, error) {
	return 0, nil
}

func (m *mockExpRepo) Close() error { return nil }

// TestRecordStrategyOutcome_PersistsSuccess verifies a successful outcome is
// written as a success experience carrying strategy id, task type and score.
func TestRecordStrategyOutcome_PersistsSuccess(t *testing.T) {
	repo := &mockExpRepo{}
	outcome := ares_evolution.StrategyOutcome{
		StrategyID: "gen-7",
		TaskType:   "code_review",
		Success:    true,
		Score:      0.88,
		Cost:       1.5,
	}

	err := recordStrategyOutcome(context.Background(), repo, outcome)
	require.NoError(t, err, "successful outcome must be recorded")
	require.Len(t, repo.created, 1, "exactly one experience must be created")

	exp := repo.created[0]
	assert.Equal(t, defaultDistillTenant, exp.TenantID, "tenant must align with hint reads")
	assert.Equal(t, "success", exp.Type)
	assert.Equal(t, "code_review", exp.Problem, "problem must carry task type for HintsForTask")
	assert.Equal(t, "code_review", exp.Input)
	assert.Contains(t, exp.Solution, "gen-7", "solution must carry strategy id")
	assert.Equal(t, 0.88, exp.Score)
	assert.True(t, exp.Success)
	assert.Equal(t, "gen-7", exp.Metadata["strategy_id"])
}

// TestRecordStrategyOutcome_PersistsFailure verifies a failed outcome maps to
// a failure experience.
func TestRecordStrategyOutcome_PersistsFailure(t *testing.T) {
	repo := &mockExpRepo{}
	outcome := ares_evolution.StrategyOutcome{
		StrategyID: "gen-3",
		TaskType:   "scheduler",
		Success:    false,
		Score:      0.2,
	}

	err := recordStrategyOutcome(context.Background(), repo, outcome)
	require.NoError(t, err)
	require.Len(t, repo.created, 1)
	assert.Equal(t, "failure", repo.created[0].Type)
	assert.False(t, repo.created[0].Success)
}

// TestRecordStrategyOutcome_RepoErrorPropagates verifies repository failures
// are returned, never swallowed as a silent no-op.
func TestRecordStrategyOutcome_RepoErrorPropagates(t *testing.T) {
	repo := &mockExpRepo{
		createFn: func(context.Context, *models.Experience) error {
			return errors.New("db down")
		},
	}
	err := recordStrategyOutcome(context.Background(), repo, ares_evolution.StrategyOutcome{
		StrategyID: "gen-1",
		Success:    true,
	})
	require.Error(t, err, "repo failure must propagate")
	assert.Contains(t, err.Error(), "db down")
}

// TestRecordStrategyOutcome_NilRepo verifies a nil repository is rejected
// instead of panicking.
func TestRecordStrategyOutcome_NilRepo(t *testing.T) {
	err := recordStrategyOutcome(context.Background(), nil, ares_evolution.StrategyOutcome{
		StrategyID: "gen-1",
		Success:    true,
	})
	require.Error(t, err, "nil repository must be rejected")
	assert.Contains(t, err.Error(), "repository is nil")
}
