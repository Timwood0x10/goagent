package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckpointPlugin_FlushInterval(t *testing.T) {
	t.Run("flush interval 0 saves on every step (default)", func(t *testing.T) {
		store := newMemoryCheckpointStore()
		p := NewCheckpointPlugin("cp", store)
		require.NoError(t, p.Start(context.Background(), nil))

		for i := 0; i < 3; i++ {
			stepID := stepIDFromInt(i)
			require.NoError(t, p.BeforeStep(context.Background(), "exec-1", &Step{ID: stepID}))
			require.NoError(t, p.AfterStep(context.Background(), "exec-1", &StepResult{StepID: stepID, Status: StepStatusCompleted}))
		}

		// Should have saved 6 times (3 BeforeStep + 3 AfterStep).
		data, err := store.Load(context.Background(), "checkpoint/exec-1")
		require.NoError(t, err)
		require.NotNil(t, data)

		var ckpt ExperienceCheckpoint
		require.NoError(t, json.Unmarshal(data, &ckpt))
		assert.Len(t, ckpt.StepStates, 3)
	})

	t.Run("flush interval 2 saves every 2 hook calls", func(t *testing.T) {
		store := newMemoryCheckpointStore()
		p := NewCheckpointPlugin("cp", store).WithFlushInterval(2)
		require.NoError(t, p.Start(context.Background(), nil))

		// Save at step 2, 4, 6 of hook calls.
		// 3 steps = 6 hook calls → 3 saves (at hook call 2, 4, 6).
		for i := 0; i < 3; i++ {
			stepID := stepIDFromInt(i)
			require.NoError(t, p.BeforeStep(context.Background(), "exec-2", &Step{ID: stepID}))
			require.NoError(t, p.AfterStep(context.Background(), "exec-2", &StepResult{StepID: stepID, Status: StepStatusCompleted}))
		}

		// Final checkpoint should have all 3 steps.
		data, err := store.Load(context.Background(), "checkpoint/exec-2")
		require.NoError(t, err)
		require.NotNil(t, data)

		var ckpt ExperienceCheckpoint
		require.NoError(t, json.Unmarshal(data, &ckpt))
		assert.Len(t, ckpt.StepStates, 3)
	})

	t.Run("Flush forces save regardless of interval", func(t *testing.T) {
		store := newMemoryCheckpointStore()
		p := NewCheckpointPlugin("cp", store).WithFlushInterval(100) // only save on explicit flush
		require.NoError(t, p.Start(context.Background(), nil))

		require.NoError(t, p.BeforeStep(context.Background(), "exec-3", &Step{ID: "s1"}))
		require.NoError(t, p.AfterStep(context.Background(), "exec-3", &StepResult{StepID: "s1", Status: StepStatusCompleted}))

		// No save happened yet.
		data, err := store.Load(context.Background(), "checkpoint/exec-3")
		require.NoError(t, err)
		assert.Nil(t, data)

		// Flush explicitly.
		require.NoError(t, p.Flush(context.Background(), "exec-3"))

		data, err = store.Load(context.Background(), "checkpoint/exec-3")
		require.NoError(t, err)
		require.NotNil(t, data)

		var ckpt ExperienceCheckpoint
		require.NoError(t, json.Unmarshal(data, &ckpt))
		assert.Len(t, ckpt.StepStates, 1)
	})
}

func stepIDFromInt(i int) string {
	return []string{"s0", "s1", "s2", "s3", "s4", "s5"}[i]
}
