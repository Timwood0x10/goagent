package ares_events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The events-table retention primitive: CleanupExpiredBefore deletes exactly
// the rows older than the cutoff (PG integration; skips without
// TEST_POSTGRES_DSN).
func TestPostgresEventStoreCleanupExpiredBefore(t *testing.T) {
	pool := getTestPool(t)
	store, err := NewPostgresEventStore(pool)
	require.NoError(t, err)
	ctx := context.Background()

	now := time.Now()
	// Three events: one old (before cutoff), two fresh.
	require.NoError(t, store.Append(ctx, "retention-stream", []*Event{
		{ID: "ret-old", Type: "task.completed", Payload: map[string]any{"k": "old"}, Timestamp: now.Add(-72 * time.Hour)},
		{ID: "ret-new-1", Type: "task.created", Payload: map[string]any{"k": "new"}, Timestamp: now},
		{ID: "ret-new-2", Type: "task.completed", Payload: map[string]any{"k": "new"}, Timestamp: now},
	}, 0))

	deleted, err := store.CleanupExpiredBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "exactly the pre-cutoff row must be deleted")

	// The fresh events survive.
	evs, err := store.Read(ctx, "retention-stream", ReadOptions{})
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, ev := range evs {
		ids[ev.ID] = true
	}
	assert.False(t, ids["ret-old"], "the old event must be gone")
	assert.True(t, ids["ret-new-1"])
	assert.True(t, ids["ret-new-2"])

	// Idempotent: a second run deletes nothing.
	deleted, err = store.CleanupExpiredBefore(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
}
