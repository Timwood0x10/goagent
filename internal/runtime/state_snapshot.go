package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrStateSnapshotNotFound is returned by LoadStateSnapshot when no snapshot
// exists under the requested key. Callers distinguish it from a real load
// failure with errors.Is.
var ErrStateSnapshotNotFound = errors.New("state snapshot: not found")

// currentStateSnapshotVersion is the schema version of StateSnapshot payloads.
// Bump it when StateSnapshot's serialized shape changes.
const currentStateSnapshotVersion = 1

// StateSnapshot is a versioned, serializable snapshot of runtime state,
// persisted via CheckpointStore for crash recovery (primitive 5: runtime
// state snapshot).
// State is intentionally an opaque map so callers can snapshot arbitrary
// runtime variables without coupling this package to their types.
type StateSnapshot struct {
	SchemaVersion int            `json:"schema_version"`
	SavedAt       time.Time      `json:"saved_at"`
	State         map[string]any `json:"state,omitempty"`
}

// SaveStateSnapshot serializes and durably writes a state snapshot under key.
// The snapshot carries SchemaVersion so a future Load can reject payloads
// written by an incompatible schema version.
func SaveStateSnapshot(ctx context.Context, store CheckpointStore, key string, state map[string]any) error {
	if store == nil {
		return errors.New("state snapshot: checkpoint store must not be nil")
	}
	if key == "" {
		return errors.New("state snapshot: key must not be empty")
	}
	data, err := json.Marshal(StateSnapshot{
		SchemaVersion: currentStateSnapshotVersion,
		SavedAt:       time.Now(),
		State:         state,
	})
	if err != nil {
		return fmt.Errorf("state snapshot: marshal %s: %w", key, err)
	}
	if err := store.Save(ctx, key, data); err != nil {
		return fmt.Errorf("state snapshot: save %s: %w", key, err)
	}
	return nil
}

// LoadStateSnapshot reads a snapshot from the store and validates its schema
// version. A payload with an unknown/newer schema version is rejected rather
// than blindly restored (code_rules: identity/version check before
// recovery). Returns ErrStateSnapshotNotFound when no snapshot exists under
// key.
func LoadStateSnapshot(ctx context.Context, store CheckpointStore, key string) (*StateSnapshot, error) {
	if store == nil {
		return nil, errors.New("state snapshot: checkpoint store must not be nil")
	}
	if key == "" {
		return nil, errors.New("state snapshot: key must not be empty")
	}
	data, err := store.Load(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("state snapshot: load %s: %w", key, err)
	}
	if data == nil {
		return nil, fmt.Errorf("%w: %s", ErrStateSnapshotNotFound, key)
	}
	var snap StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("state snapshot: unmarshal %s: %w", key, err)
	}
	if snap.SchemaVersion != currentStateSnapshotVersion {
		return nil, fmt.Errorf("state snapshot: %s schema version %d unsupported (want %d)",
			key, snap.SchemaVersion, currentStateSnapshotVersion)
	}
	return &snap, nil
}
