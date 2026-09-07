package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
)

// TestNewCompactableStoreWithArchive_EnableDefault verifies the default-on
// branch: a zero-Enabled config (nil) yields an archive-enabled store, so
// appending a task-terminal event eventually produces a round_N.json file in
// cfg.Dir. It also asserts both returned values are non-nil.
func TestNewCompactableStoreWithArchive_EnableDefault(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rounds")

	// Enabled == nil → IsEnabled() true (default-on).
	ces, raw, err := NewCompactableStoreWithArchive(ares_config.ArchiveConfig{
		Dir:       dir,
		MaxRounds: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, ces, "compactable store must be non-nil")
	require.NotNil(t, raw, "raw memory store must be non-nil")

	ctx := context.Background()
	streamID := "stream-enable"
	require.NoError(t, ces.Append(ctx, streamID, []*ares_events.Event{
		{Type: ares_events.EventTaskCompleted, Payload: map[string]any{
			ares_events.EventKeyTask: "wire archive store",
		}},
	}, 0))

	// Archive runs asynchronously in a background goroutine (errgroup), so poll
	// for the round file rather than asserting synchronously. Rounds are stored
	// under a per-stream subdirectory (REVIEW #50) so distinct streams never
	// overwrite each other's round_N.json.
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(filepath.Join(dir, streamID, "round_1.json"))
		return statErr == nil
	}, 2*time.Second, 10*time.Millisecond, "round_1.json must be written under the stream subdir for the default-on config")
}

// TestNewCompactableStoreWithArchive_Disabled verifies the explicit-opt-out
// branch: Enabled == false yields a plain compactable store with NO archive
// sink, so appending a terminal event produces no round file. Compaction still
// runs, but nothing is persisted to disk.
func TestNewCompactableStoreWithArchive_Disabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rounds")
	enabled := false

	ces, raw, err := NewCompactableStoreWithArchive(ares_config.ArchiveConfig{
		Enabled:   &enabled,
		Dir:       dir,
		MaxRounds: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, ces)
	require.NotNil(t, raw)

	ctx := context.Background()
	streamID := "stream-disable"
	require.NoError(t, ces.Append(ctx, streamID, []*ares_events.Event{
		{Type: ares_events.EventTaskCompleted, Payload: map[string]any{
			ares_events.EventKeyTask: "no archive",
		}},
	}, 0))

	// Give the async compaction/archive path a moment, then assert no round file
	// was ever created. The dir itself is not created either because the writer
	// is never constructed when disabled.
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr),
		"archive dir must not be created when archiving is disabled")
}
