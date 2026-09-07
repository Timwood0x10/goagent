package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memCheckpointStore is an in-memory CheckpointStore for tests.
type memCheckpointStore struct {
	kv map[string][]byte
}

func newMemCheckpointStore() *memCheckpointStore {
	return &memCheckpointStore{kv: make(map[string][]byte)}
}

func (s *memCheckpointStore) Save(_ context.Context, key string, data []byte) error {
	s.kv[key] = data
	return nil
}

func (s *memCheckpointStore) Load(_ context.Context, key string) ([]byte, error) {
	return s.kv[key], nil
}

func TestStateSnapshot_SaveAndLoadRoundTrip(t *testing.T) {
	store := newMemCheckpointStore()
	ctx := context.Background()

	err := SaveStateSnapshot(ctx, store, "run:abc", map[string]any{
		"step":     "compile",
		"progress": 3,
	})
	require.NoError(t, err)

	snap, err := LoadStateSnapshot(ctx, store, "run:abc")
	require.NoError(t, err)
	require.NotNil(t, snap)
	assert.Equal(t, currentStateSnapshotVersion, snap.SchemaVersion)
	assert.Equal(t, "compile", snap.State["step"])
	assert.Equal(t, float64(3), snap.State["progress"], "JSON round-trip turns ints into float64")
}

func TestStateSnapshot_LoadMissingReturnsNil(t *testing.T) {
	store := newMemCheckpointStore()
	snap, err := LoadStateSnapshot(context.Background(), store, "missing")
	require.ErrorIs(t, err, ErrStateSnapshotNotFound, "missing snapshot must surface the sentinel")
	assert.Nil(t, snap)
}

func TestStateSnapshot_RejectsUnsupportedSchemaVersion(t *testing.T) {
	store := newMemCheckpointStore()
	// Write a payload with a newer, unsupported schema version by hand.
	data, err := json.Marshal(StateSnapshot{
		SchemaVersion: currentStateSnapshotVersion + 1,
		State:         map[string]any{"x": 1},
	})
	require.NoError(t, err)
	require.NoError(t, store.Save(context.Background(), "future", data))

	snap, err := LoadStateSnapshot(context.Background(), store, "future")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version")
	assert.Nil(t, snap, "unsupported schema must not be restored")
}

func TestStateSnapshot_RejectsCorruptPayload(t *testing.T) {
	store := newMemCheckpointStore()
	require.NoError(t, store.Save(context.Background(), "corrupt", []byte("{not-json")))

	snap, err := LoadStateSnapshot(context.Background(), store, "corrupt")
	require.Error(t, err)
	assert.Nil(t, snap)
}

func TestStateSnapshot_NilStoreAndEmptyKey(t *testing.T) {
	ctx := context.Background()
	err := SaveStateSnapshot(ctx, nil, "k", map[string]any{})
	require.Error(t, err, "nil store must be rejected")

	err = SaveStateSnapshot(ctx, newMemCheckpointStore(), "", map[string]any{})
	require.Error(t, err, "empty key must be rejected")

	_, err = LoadStateSnapshot(ctx, nil, "k")
	require.Error(t, err, "nil store must be rejected")

	_, err = LoadStateSnapshot(ctx, newMemCheckpointStore(), "")
	require.Error(t, err, "empty key must be rejected")
}
