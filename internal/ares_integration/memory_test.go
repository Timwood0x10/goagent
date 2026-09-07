// package integration provides end-to-end integration tests with real PostgreSQL.
package ares_integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/api/embedding"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	pgembed "github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
)

// testEmbedder is a minimal EmbeddingService mock for in-memory tests.
type testEmbedder struct{}

func (t *testEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedWithPrefix(_ context.Context, _, _ string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedBatch(_ context.Context, _ []string) ([][]float64, error) {
	return [][]float64{{0.1, 0.2, 0.3}}, nil
}

func (t *testEmbedder) HealthCheck(_ context.Context) error { return nil }
func (t *testEmbedder) GetModel() string                    { return "test-model" }
func (t *testEmbedder) GetTimeout() time.Duration           { return time.Second }

var _ embedding.EmbeddingService = (*testEmbedder)(nil)

// testExpRepo is a minimal ExperienceRepository mock for in-memory tests.
type testExpRepo struct {
	experiences []distillation.Experience
}

func (r *testExpRepo) SearchByVector(_ context.Context, _ []float64, _ string, _ int) ([]distillation.Experience, error) {
	return r.experiences, nil
}

func (r *testExpRepo) GetByMemoryType(_ context.Context, _ string, _ distillation.MemoryType) ([]distillation.Experience, error) {
	return r.experiences, nil
}

func (r *testExpRepo) CountByMemoryType(_ context.Context, _ string, _ distillation.MemoryType) (int, error) {
	return len(r.experiences), nil
}

func (r *testExpRepo) Update(_ context.Context, _ *distillation.Experience) error { return nil }
func (r *testExpRepo) Delete(_ context.Context, _ string) error                   { return nil }
func (r *testExpRepo) DeleteBatch(_ context.Context, _ []string) error            { return nil }

func (r *testExpRepo) Create(_ context.Context, experience *distillation.Experience) error {
	r.experiences = append(r.experiences, *experience)
	return nil
}

var _ distillation.ExperienceRepository = (*testExpRepo)(nil)

// createTestMemoryManager creates a ProductionMemoryManager for integration tests.
// Returns nil and skips if embedding client cannot be created.
func createTestMemoryManager(t *testing.T, pool *postgres.Pool) *memory.ProductionMemoryManager {
	t.Helper()

	// Create an embedding client pointing to a non-existent service.
	// Embedding operations will fail, but session/task operations that
	// don't require embeddings will still work.
	embeddingClient := pgembed.NewEmbeddingClient(
		"http://localhost:9999",
		"intfloat/e5-large",
		nil,
		5*time.Second,
	)

	config := &memory.MemoryConfig{
		Enabled:           true,
		Storage:           "postgres",
		MaxHistory:        10,
		MaxSessions:       100,
		MaxTasks:          1000,
		MaxDistilledTasks: 5000,
		SessionTTL:        24 * time.Hour,
		TaskTTL:           7 * 24 * time.Hour,
		VectorDim:         128,
		EnablePostgres:    true,
	}

	mgr, err := memory.NewProductionMemoryManager(pool, embeddingClient, config)
	require.NoError(t, err, "failed to create ProductionMemoryManager")
	return mgr
}

// TestProductionMemorySessionPipeline verifies the full session pipeline:
// CreateSession -> AddMessage -> GetMessages -> BuildContext.
func TestProductionMemorySessionPipeline(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	t.Cleanup(func() {
		cleanupTables(t, pool, "conversations")
	})

	ctx := context.Background()
	mgr := createTestMemoryManager(t, pool)
	require.NotNil(t, mgr)

	// Start the manager.
	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	// Set tenant ID for multi-tenant operations.
	require.NoError(t, mgr.SetTenantID("test-tenant"))

	// Create a session.
	sessionID, err := mgr.CreateSession(ctx, "test-user")
	require.NoError(t, err)
	require.NotEmpty(t, sessionID, "expected non-empty session ID")

	// Add messages to the session.
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "Hello, I need help with styling"))
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "assistant", "Sure, I can help you with that"))
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "I prefer casual style"))

	// Get messages from the session.
	messages, err := mgr.GetMessages(ctx, sessionID)
	require.NoError(t, err)
	require.Len(t, messages, 3, "expected 3 messages")
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "Hello, I need help with styling", messages[0].Content)
	assert.Equal(t, "assistant", messages[1].Role)
	assert.Equal(t, "Sure, I can help you with that", messages[1].Content)

	// Build context from session history.
	contextStr, err := mgr.BuildContext(ctx, "What about shoes?", sessionID)
	require.NoError(t, err)
	assert.Contains(t, contextStr, "Previous conversation history", "expected context to include history")
	assert.Contains(t, contextStr, "What about shoes?", "expected context to include current input")
}

// TestProductionMemorySessionValidation verifies input validation for session operations.
func TestProductionMemorySessionValidation(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)

	ctx := context.Background()
	mgr := createTestMemoryManager(t, pool)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	require.NoError(t, mgr.SetTenantID("test-tenant"))

	// AddMessage with empty session ID should fail.
	err := mgr.AddMessage(ctx, "", "user", "hello")
	require.Error(t, err)

	// AddMessage with empty role should fail.
	sessionID, err := mgr.CreateSession(ctx, "test-user")
	require.NoError(t, err)
	err = mgr.AddMessage(ctx, sessionID, "", "hello")
	require.Error(t, err)

	// AddMessage with empty content should fail.
	err = mgr.AddMessage(ctx, sessionID, "user", "")
	require.Error(t, err)

	// GetMessages with empty session ID should fail.
	_, err = mgr.GetMessages(ctx, "")
	require.Error(t, err)
}

// TestProductionMemoryTaskPipeline verifies the task lifecycle:
// CreateTask -> UpdateTaskOutput -> DistillTask.
func TestProductionMemoryTaskPipeline(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	t.Cleanup(func() {
		cleanupTables(t, pool, "task_results_1024")
	})

	ctx := context.Background()
	mgr := createTestMemoryManager(t, pool)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	require.NoError(t, mgr.SetTenantID("test-tenant"))

	// Create a session first.
	sessionID, err := mgr.CreateSession(ctx, "test-user")
	require.NoError(t, err)

	// Create a task.
	taskID, err := mgr.CreateTask(ctx, sessionID, "test-user", "Find casual shoes")
	require.NoError(t, err)
	require.NotEmpty(t, taskID, "expected non-empty task ID")

	// Update task output.
	require.NoError(t, mgr.UpdateTaskOutput(ctx, taskID, "Found 3 casual shoe options"))

	// Distill the task.
	distilled, err := mgr.DistillTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, distilled, "expected non-nil distilled task")
	assert.Equal(t, taskID, distilled.TaskID)
	assert.NotNil(t, distilled.Payload, "expected non-nil payload")
}

// TestProductionMemoryTaskValidation verifies input validation for task operations.
func TestProductionMemoryTaskValidation(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)

	ctx := context.Background()
	mgr := createTestMemoryManager(t, pool)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	require.NoError(t, mgr.SetTenantID("test-tenant"))

	// UpdateTaskOutput with empty task ID should fail.
	err := mgr.UpdateTaskOutput(ctx, "", "output")
	require.Error(t, err)

	// DistillTask with non-existent task ID should fail.
	_, err = mgr.DistillTask(ctx, "non-existent-task-id")
	require.Error(t, err)
}

// TestGetLatestSessionForAgent verifies that the checkpoint-based session
// lookup works correctly with the ProductionMemoryManager.
func TestGetLatestSessionForAgent(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	t.Cleanup(func() {
		cleanupTables(t, pool, "agent_checkpoints")
	})

	ctx := context.Background()
	mgr := createTestMemoryManager(t, pool)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	require.NoError(t, mgr.SetTenantID("test-tenant"))

	agentID := fmt.Sprintf("test-agent-%d", time.Now().UnixNano())

	// Before any checkpoint exists, should return empty string.
	sessionID, err := mgr.GetLatestSessionForAgent(ctx, agentID)
	require.NoError(t, err)
	assert.Empty(t, sessionID, "expected empty session ID when no checkpoint exists")

	// Insert a checkpoint directly.
	_, err = pool.Exec(ctx,
		"INSERT INTO agent_checkpoints (agent_id, session_id, status, updated_at) VALUES ($1, $2, $3, NOW())",
		agentID, "session-abc", "active",
	)
	require.NoError(t, err)

	// Now should return the session ID.
	sessionID, err = mgr.GetLatestSessionForAgent(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "session-abc", sessionID)

	// Insert a newer checkpoint for the same agent.
	_, err = pool.Exec(ctx,
		"INSERT INTO agent_checkpoints (agent_id, session_id, status, updated_at) VALUES ($1, $2, $3, NOW()) ON CONFLICT (agent_id) DO UPDATE SET session_id = EXCLUDED.session_id, updated_at = NOW()",
		agentID, "session-xyz", "active",
	)
	require.NoError(t, err)

	// Should return the newest session ID.
	sessionID, err = mgr.GetLatestSessionForAgent(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "session-xyz", sessionID)

	// Empty agent ID should return empty string.
	sessionID, err = mgr.GetLatestSessionForAgent(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, sessionID)
}

// TestInMemoryMemoryManagerSessionPipeline verifies the in-memory MemoryManager
// session pipeline: CreateSession -> AddMessage -> GetMessages -> BuildContext.
func TestInMemoryMemoryManagerSessionPipeline(t *testing.T) {
	ctx := context.Background()

	config := memory.DefaultMemoryConfig()
	config.MaxHistory = 5

	mgr, err := memory.NewMemoryManager(config)
	require.NoError(t, err)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	// Create a session.
	sessionID, err := mgr.CreateSession(ctx, "user-1")
	require.NoError(t, err)
	require.NotEmpty(t, sessionID)

	// Add messages.
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "Hello"))
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "assistant", "Hi there!"))
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "Help me find a dress"))

	// Get messages.
	messages, err := mgr.GetMessages(ctx, sessionID)
	require.NoError(t, err)
	// Session memory stores messages including a system message from CreateSession.
	assert.GreaterOrEqual(t, len(messages), 3, "expected at least 3 messages")

	// Build context.
	contextStr, err := mgr.BuildContext(ctx, "What about red dresses?", sessionID)
	require.NoError(t, err)
	assert.Contains(t, contextStr, "What about red dresses?", "expected context to include current input")
}

// TestInMemoryMemoryManagerTaskPipeline verifies the in-memory MemoryManager
// task pipeline: CreateTask -> UpdateTaskOutput -> DistillTask -> StoreDistilledTask.
func TestInMemoryMemoryManagerTaskPipeline(t *testing.T) {
	ctx := context.Background()

	config := memory.DefaultMemoryConfig()
	config.VectorDim = 128

	mgr, err := memory.NewMemoryManagerWithDistiller(config, &testEmbedder{}, &testExpRepo{})
	require.NoError(t, err)
	require.NotNil(t, mgr)

	require.NoError(t, mgr.Start(ctx))
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, mgr.Stop(stopCtx))
	}()

	// Create a session.
	sessionID, err := mgr.CreateSession(ctx, "user-1")
	require.NoError(t, err)

	// Create a task.
	taskID, err := mgr.CreateTask(ctx, sessionID, "user-1", "Find blue sneakers")
	require.NoError(t, err)
	require.NotEmpty(t, taskID)

	// Update task output.
	require.NoError(t, mgr.UpdateTaskOutput(ctx, taskID, "Found 5 blue sneaker options"))

	// Distill the task.
	distilled, err := mgr.DistillTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, distilled)

	// Store the distilled task.
	require.NoError(t, mgr.StoreDistilledTask(ctx, taskID, distilled))

	// Search for similar tasks.
	similar, err := mgr.SearchSimilarTasks(ctx, "blue sneakers", 5)
	require.NoError(t, err)
	assert.NotNil(t, similar, "expected non-nil similar tasks result")
}
