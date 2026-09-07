// Package memory provides unified memory management for the StyleAgent framework.
package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

// failingExpRepo is an ExperienceRepository mock whose Create always returns
// storeErr. It is used to verify that StoreDistilledTask propagates storage
// failures through the errgroup instead of silently dropping them.
type failingExpRepo struct {
	storeErr error
}

func (r *failingExpRepo) SearchByVector(_ context.Context, _ []float64, _ string, _ int) ([]distillation.Experience, error) {
	return nil, nil
}

func (r *failingExpRepo) GetByMemoryType(_ context.Context, _ string, _ distillation.MemoryType) ([]distillation.Experience, error) {
	return nil, nil
}

func (r *failingExpRepo) CountByMemoryType(_ context.Context, _ string, _ distillation.MemoryType) (int, error) {
	return 0, nil
}

func (r *failingExpRepo) Update(_ context.Context, _ *distillation.Experience) error { return nil }

func (r *failingExpRepo) Delete(_ context.Context, _ string) error { return nil }

func (r *failingExpRepo) DeleteBatch(_ context.Context, _ []string) error { return nil }

func (r *failingExpRepo) Create(_ context.Context, _ *distillation.Experience) error {
	return r.storeErr
}

var _ distillation.ExperienceRepository = (*failingExpRepo)(nil)

// TestMemoryManager_GetLatestSessionForAgent_Unsupported verifies that the
// in-memory memoryManager returns ErrAgentCheckpointNotSupported rather than
// a silent ("", nil).
//
// The in-memory backend has no agent->session mapping (sessions are keyed by
// session ID and carry a UserID, not an agent ID), so it cannot answer
// the question. Returning a distinct error lets the caller
// (ares_runtime/manager_lifecycle.go buildCognitiveState) distinguish
// "no session for this agent" from "backend cannot answer" and log
// accordingly, instead of silently treating it as "no session" and losing
// agent recovery.
func TestMemoryManager_GetLatestSessionForAgent_Unsupported(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	// Seed a session so we can prove the empty result is NOT just because no
	// sessions exist: even with a live session, the in-memory backend cannot
	// map an agent ID to a session.
	_, err = mgr.CreateSession(ctx, "some-user")
	require.NoError(t, err)

	sessionID, err := mgr.GetLatestSessionForAgent(ctx, "any-agent-id")

	// The error must be ErrAgentCheckpointNotSupported so callers can branch on it.
	require.ErrorIs(t, err, ErrAgentCheckpointNotSupported,
		"in-memory backend must return ErrAgentCheckpointNotSupported, not a silent empty result")
	require.Empty(t, sessionID, "no session ID should be returned when the backend is unsupported")
}

// TestMemoryManager_GetLatestSessionForAgent_EmptyAgentID verifies the
// unsupported error is returned regardless of the agent ID value, since the
// backend cannot answer the lookup at all (not because of input validation).
func TestMemoryManager_GetLatestSessionForAgent_EmptyAgentID(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	_, err = mgr.GetLatestSessionForAgent(ctx, "")
	require.ErrorIs(t, err, ErrAgentCheckpointNotSupported)
}

// TestMemoryManager_StoreDistilledTask_RepositoryError verifies that when the
// experience repository fails to store a memory, StoreDistilledTask returns an
// error rather than silently succeeding.
//
// This test documents why the previously-removed dead branch
// `len(memories) > 0 && storedCount == 0` was unreachable: the errgroup
// propagates the first storage error from g.Wait(), so any failure surfaces as
// a non-nil error before storedCount is ever inspected. With the dead branch
// gone, this test guards the real error-propagation path.
//
// The input/output pair is chosen so the distiller extracts at least one
// memory (the extractor classifies "I have an error in my code" as a question
// and pairs it with the assistant response). Without a memory the errgroup
// would have no goroutines to fail and the error path could not be exercised.
func TestMemoryManager_StoreDistilledTask_RepositoryError(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	storeErr := errors.New("simulated storage failure")
	mgr, err := NewMemoryManagerWithDistiller(config, &testEmbedder{}, &failingExpRepo{storeErr: storeErr})
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	require.NoError(t, err)

	const (
		// Pair known to produce at least one extracted memory (see
		// distiller_test.go TestDistiller_DistillConversation).
		inputStr  = "I have an error in my code"
		outputStr = "Fix the syntax error on line 10"
	)
	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", inputStr)
	require.NoError(t, err)

	distilled := &models.Task{
		TaskID: taskID,
		Payload: map[string]any{
			"input":   inputStr,
			"output":  outputStr,
			"context": map[string]interface{}{},
		},
	}

	err = mgr.StoreDistilledTask(ctx, taskID, distilled)
	require.Error(t, err, "StoreDistilledTask must surface storage failures, not swallow them")
}

// TestMemoryManager_StoreDistilledTask_AllStoredSucceeds verifies the happy path:
// when the distiller produces memories and the repository stores them all,
// StoreDistilledTask returns nil. This complements the error test and guards the
// path that the removed dead branch (`len(memories) > 0 && storedCount == 0`)
// used to (incorrectly) cover — here storedCount == len(memories) > 0.
func TestMemoryManager_StoreDistilledTask_AllStoredSucceeds(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	repo := &testExpRepo{}
	mgr, err := NewMemoryManagerWithDistiller(config, &testEmbedder{}, repo)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	require.NoError(t, err)

	const (
		inputStr  = "I have an error in my code"
		outputStr = "Fix the syntax error on line 10"
	)
	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", inputStr)
	require.NoError(t, err)

	distilled := &models.Task{
		TaskID: taskID,
		Payload: map[string]any{
			"input":   inputStr,
			"output":  outputStr,
			"context": map[string]interface{}{},
		},
	}

	require.NoError(t, mgr.StoreDistilledTask(ctx, taskID, distilled),
		"StoreDistilledTask should succeed when all experiences are stored")
	require.NotEmpty(t, repo.experiences,
		"at least one experience should have been stored, otherwise the storage path was not exercised")
}
