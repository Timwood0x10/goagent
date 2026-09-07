package ares_integration

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// TestTenantIsolationIDScopedMutators is the tenant-isolation acceptance
// suite: for EVERY id-scoped mutator, tenant B must not be able to read or
// mutate a row owned by tenant A, and the row must survive unchanged. Each
// block also runs the same call under the owning tenant as a positive control,
// so a regression that makes everything fail (e.g. RLS misconfiguration)
// cannot masquerade as isolation.
func TestTenantIsolationIDScopedMutators(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	ctx := context.Background()

	db := pool.GetDB()
	knowRepo := repositories.NewKnowledgeRepository(db, db)
	expRepo := repositories.NewExperienceRepository(db)
	toolRepo := repositories.NewToolRepository(db)
	taskRepo := repositories.NewTaskResultRepository(db)

	const owner = "iso-tenant-a"
	const other = "iso-tenant-b"
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM knowledge_chunks_1024 WHERE tenant_id LIKE 'iso-tenant-%'`,
			`DELETE FROM experiences_1024 WHERE tenant_id LIKE 'iso-tenant-%'`,
			`DELETE FROM tools WHERE tenant_id LIKE 'iso-tenant-%'`,
			`DELETE FROM task_results WHERE tenant_id LIKE 'iso-tenant-%'`,
		} {
			_, _ = db.ExecContext(ctx, q)
		}
	})

	// ── Knowledge ────────────────────────────────────────────────────────────
	kc := &models.KnowledgeChunk{
		ID:              "kc-" + suffix,
		TenantID:        owner,
		Content:         "secret-knowledge",
		EmbeddingModel:  "m",
		EmbeddingStatus: "pending",
		SourceType:      "test",
		Metadata:        map[string]interface{}{},
	}
	require.NoError(t, knowRepo.Create(ctx, kc))

	t.Run("knowledge_get_by_id", func(t *testing.T) {
		_, err := knowRepo.GetByID(ctx, other, kc.ID)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound), "cross-tenant read must 404, got %v", err)
		got, err := knowRepo.GetByID(ctx, owner, kc.ID)
		require.NoError(t, err)
		assert.Equal(t, "secret-knowledge", got.Content)
	})

	t.Run("knowledge_update", func(t *testing.T) {
		bad := *kc
		bad.TenantID = other
		bad.Content = "hacked"
		err := knowRepo.Update(ctx, &bad)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound), "cross-tenant update must fail, got %v", err)
		got, gerr := knowRepo.GetByID(ctx, owner, kc.ID)
		require.NoError(t, gerr)
		assert.Equal(t, "secret-knowledge", got.Content, "row must be unchanged")
	})

	t.Run("knowledge_update_embedding", func(t *testing.T) {
		err := knowRepo.UpdateEmbedding(ctx, other, kc.ID, []float64{0.1}, "evil", 9)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound))
		require.NoError(t, knowRepo.UpdateEmbedding(ctx, owner, kc.ID, []float64{0.1}, "good", 1))
	})

	t.Run("knowledge_update_embedding_status", func(t *testing.T) {
		err := knowRepo.UpdateEmbeddingStatus(ctx, other, kc.ID, "failed", "x")
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound))
		require.NoError(t, knowRepo.UpdateEmbeddingStatus(ctx, owner, kc.ID, "completed", ""))
	})

	// ── Experience ───────────────────────────────────────────────────────────
	exp := &models.Experience{
		ID:       "exp-" + suffix,
		TenantID: owner,
		Type:     "success",
		Problem:  "p-" + suffix,
		Score:    0.5,
		Success:  true,
		Metadata: map[string]interface{}{},
	}
	require.NoError(t, expRepo.Create(ctx, exp))

	t.Run("experience_get_by_id", func(t *testing.T) {
		_, err := expRepo.GetByID(ctx, other, exp.ID)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound), "got %v", err)
		got, err := expRepo.GetByID(ctx, owner, exp.ID)
		require.NoError(t, err)
		assert.Equal(t, 0.5, got.Score)
	})

	t.Run("experience_update", func(t *testing.T) {
		bad := *exp
		bad.TenantID = other
		bad.Score = 0.99
		assert.True(t, stderrors.Is(expRepo.Update(ctx, &bad), errors.ErrRecordNotFound))
		got, _ := expRepo.GetByID(ctx, owner, exp.ID)
		assert.Equal(t, 0.5, got.Score)
	})

	t.Run("experience_update_score", func(t *testing.T) {
		assert.True(t, stderrors.Is(expRepo.UpdateScore(ctx, other, exp.ID, 0.01), errors.ErrRecordNotFound))
		require.NoError(t, expRepo.UpdateScore(ctx, owner, exp.ID, 0.6))
	})

	t.Run("experience_update_embedding", func(t *testing.T) {
		assert.True(t, stderrors.Is(
			expRepo.UpdateEmbedding(ctx, other, exp.ID, []float64{0.2}, "evil", 9), errors.ErrRecordNotFound))
		require.NoError(t, expRepo.UpdateEmbedding(ctx, owner, exp.ID, []float64{0.2}, "good", 1))
	})

	t.Run("experience_increment_usage_count", func(t *testing.T) {
		before, _ := expRepo.GetByID(ctx, owner, exp.ID)
		assert.True(t, stderrors.Is(expRepo.IncrementUsageCount(ctx, other, exp.ID), errors.ErrRecordNotFound))
		require.NoError(t, expRepo.IncrementUsageCount(ctx, owner, exp.ID))
		after, _ := expRepo.GetByID(ctx, owner, exp.ID)
		assert.Equal(t, before.UsageCount+1, after.UsageCount)
	})

	t.Run("experience_decrement_rank", func(t *testing.T) {
		before, _ := expRepo.GetByID(ctx, owner, exp.ID)
		assert.True(t, stderrors.Is(expRepo.DecrementRank(ctx, other, exp.ID), errors.ErrRecordNotFound))
		require.NoError(t, expRepo.DecrementRank(ctx, owner, exp.ID))
		after, _ := expRepo.GetByID(ctx, owner, exp.ID)
		assert.Less(t, after.Score, before.Score, "owner-path decrement must lower score")
	})

	// ── Tool ────────────────────────────────────────────────────────────────
	tool := &models.Tool{
		ID:          "tool-" + suffix,
		TenantID:    owner,
		Name:        "tool-" + suffix,
		Description: "d",
		AgentType:   "tester",
		Metadata:    map[string]interface{}{},
	}
	require.NoError(t, toolRepo.Create(ctx, tool))

	t.Run("tool_get_by_id", func(t *testing.T) {
		_, err := toolRepo.GetByID(ctx, other, tool.ID)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound), "got %v", err)
		_, err = toolRepo.GetByID(ctx, owner, tool.ID)
		require.NoError(t, err)
	})

	t.Run("tool_update", func(t *testing.T) {
		bad := *tool
		bad.TenantID = other
		bad.Description = "hacked"
		assert.True(t, stderrors.Is(toolRepo.Update(ctx, &bad), errors.ErrRecordNotFound))
		got, _ := toolRepo.GetByID(ctx, owner, tool.ID)
		assert.Equal(t, "d", got.Description)
	})

	t.Run("tool_update_usage", func(t *testing.T) {
		before, _ := toolRepo.GetByID(ctx, owner, tool.ID)
		assert.True(t, stderrors.Is(toolRepo.UpdateUsage(ctx, other, tool.ID, true), errors.ErrRecordNotFound))
		require.NoError(t, toolRepo.UpdateUsage(ctx, owner, tool.ID, true))
		after, _ := toolRepo.GetByID(ctx, owner, tool.ID)
		assert.Equal(t, before.UsageCount+1, after.UsageCount)
	})

	t.Run("tool_update_embedding", func(t *testing.T) {
		assert.True(t, stderrors.Is(
			toolRepo.UpdateEmbedding(ctx, other, tool.ID, []float64{0.3}, "evil", 9), errors.ErrRecordNotFound))
		require.NoError(t, toolRepo.UpdateEmbedding(ctx, owner, tool.ID, []float64{0.3}, "good", 1))
	})

	// ── TaskResult ──────────────────────────────────────────────────────────
	tr := &models.TaskResult{
		ID:        "tr-" + suffix,
		TenantID:  owner,
		SessionID: "sess-" + suffix,
		TaskType:  "code",
		Status:    "done",
		Input:     map[string]interface{}{},
		Output:    map[string]interface{}{},
		Metadata:  map[string]interface{}{},
	}
	require.NoError(t, taskRepo.Create(ctx, tr))

	t.Run("task_result_get_by_id", func(t *testing.T) {
		_, err := taskRepo.GetByID(ctx, other, tr.ID)
		assert.True(t, stderrors.Is(err, errors.ErrRecordNotFound), "got %v", err)
		got, err := taskRepo.GetByID(ctx, owner, tr.ID)
		require.NoError(t, err)
		assert.Equal(t, "done", got.Status)
	})

	t.Run("task_result_update", func(t *testing.T) {
		bad := *tr
		bad.TenantID = other
		bad.Status = "hacked"
		assert.True(t, stderrors.Is(taskRepo.Update(ctx, &bad), errors.ErrRecordNotFound))
		got, _ := taskRepo.GetByID(ctx, owner, tr.ID)
		assert.Equal(t, "done", got.Status)
	})

	t.Run("task_result_update_embedding", func(t *testing.T) {
		assert.True(t, stderrors.Is(
			taskRepo.UpdateEmbedding(ctx, other, tr.ID, []float64{0.4}, "evil", 9), errors.ErrRecordNotFound))
		require.NoError(t, taskRepo.UpdateEmbedding(ctx, owner, tr.ID, []float64{0.4}, "good", 1))
	})

	t.Run("task_result_update_status", func(t *testing.T) {
		assert.True(t, stderrors.Is(taskRepo.UpdateStatus(ctx, other, tr.ID, "failed", "", 0), errors.ErrRecordNotFound))
		require.NoError(t, taskRepo.UpdateStatus(ctx, owner, tr.ID, "archived", "", 0))
	})
}
