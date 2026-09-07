package agentfabric

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/agentipc"
)

// TestCognitiveStateSchemaVersionMigration is the A2 schema migration test
// code rules : DecodeCognitiveState accepts the current version and
// legacy v0 (pre-A2 zero value), and rejects a future version instead of
// silently misinterpreting it.
func TestCognitiveStateSchemaVersionMigration(t *testing.T) {
	cases := []struct {
		name    string
		input   any
		wantErr bool
	}{
		{
			name:  "current_version_accepted",
			input: CognitiveState{SchemaVersion: CognitiveStateSchemaVersion, Context: "c"},
		},
		{
			name:  "legacy_zero_version_accepted",
			input: CognitiveState{Context: "legacy"},
		},
		{
			name:  "legacy_pointer_accepted",
			input: &CognitiveState{Context: "legacy-ptr"},
		},
		{
			name:  "json_roundtrip_map_accepted",
			input: map[string]any{"schema_version": float64(CognitiveStateSchemaVersion), "context": "mapped"},
		},
		{
			name:    "future_version_rejected",
			input:   CognitiveState{SchemaVersion: CognitiveStateSchemaVersion + 1, Context: "future"},
			wantErr: true,
		},
		{
			name:    "future_version_pointer_rejected",
			input:   &CognitiveState{SchemaVersion: CognitiveStateSchemaVersion + 1},
			wantErr: true,
		},
		{
			name:    "future_version_map_rejected",
			input:   map[string]any{"schema_version": float64(CognitiveStateSchemaVersion + 1)},
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeCognitiveState(c.input)
			if c.wantErr && err == nil {
				t.Fatal("expected ErrCognitiveStateSchemaVersion, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestSetCognitiveStateStampsVersion verifies the A2 boundary contract: a
// legacy state (SchemaVersion=0) written via SetCognitiveState is upgraded to
// the current version, so every stored state carries a version (readable via
// DecodeCognitiveState without ambiguity).
func TestSetCognitiveStateStampsVersion(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{Identity: "a"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.SetCognitiveState("a", CognitiveState{Context: "legacy"}); err != nil {
		t.Fatalf("SetCognitiveState: %v", err)
	}
	cs, err := f.CognitiveState("a")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.SchemaVersion != CognitiveStateSchemaVersion {
		t.Fatalf("stored state must carry the current version, got %d", cs.SchemaVersion)
	}
}

// TestRecoverStampsVersion verifies the same boundary upgrade on the Recover
// path: a recovered legacy cognitive state is versioned before it becomes the
// agent's live state.
func TestRecoverStampsVersion(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{Identity: "a"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if err := f.Recover(context.Background(), "a", CognitiveState{Context: "recovered"}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	cs, err := f.CognitiveState("a")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.SchemaVersion != CognitiveStateSchemaVersion {
		t.Fatalf("recovered state must carry the current version, got %d", cs.SchemaVersion)
	}
	if cs.Context != "recovered" {
		t.Fatalf("recovered context must be preserved, got %v", cs.Context)
	}
}

// TestIPCIsolationFromContextLayers verifies the third layer boundary (A2):
// IPC messages travel on the agentipc bus — a storage surface completely
// separate from the agent's Task Shared and Private layers. A message
// delivered over IPC must never appear in the receiving agent's ContextView,
// and the sender's Private state must never leak into the IPC payload.
func TestIPCIsolationFromContextLayers(t *testing.T) {
	f := NewFabric()
	for _, id := range []string{"A", "B"} {
		if _, err := f.Spawn(context.Background(), SpawnSpec{
			Identity:     id,
			Capabilities: []string{"code"},
			TaskContext:  map[string]any{"goal": "shared-goal"},
		}); err != nil {
			t.Fatalf("Spawn %s: %v", id, err)
		}
	}
	// A writes private state that must never cross the IPC boundary.
	if err := f.SetPrivate("A", "hypothesis", "A-secret-hypothesis"); err != nil {
		t.Fatalf("SetPrivate A: %v", err)
	}

	bus := agentipc.NewBus()
	var received *agentipc.Message
	if err := bus.Register("B", func(_ context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		received = msg
		return nil, nil
	}); err != nil {
		t.Fatalf("Register B: %v", err)
	}
	if err := bus.Send(context.Background(), "A", "B", "handoff", map[string]any{"data": "ipc-payload"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// 1. The IPC payload must not appear in B's Task Shared or Private layers.
	viewB, err := f.ContextView("B")
	if err != nil {
		t.Fatalf("ContextView B: %v", err)
	}
	if _, leak := viewB.TaskShared["data"]; leak {
		t.Fatal("IPC payload must not leak into the receiver's Task Shared layer")
	}
	if _, leak := viewB.Private["data"]; leak {
		t.Fatal("IPC payload must not leak into the receiver's Private layer")
	}
	// 2. The receiver's Task Shared state must not be smuggled into the IPC
	//    message (the bus carries only what Send put in).
	if received == nil {
		t.Fatal("IPC message must have been delivered")
	}
	if _, ok := received.Payload.(map[string]any)["goal"]; ok {
		t.Fatal("receiver's Task Shared state must not appear in the IPC payload")
	}
	// 3. The sender's Private state must not appear in the IPC payload.
	payload, ok := received.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload must be map, got %T", received.Payload)
	}
	if _, leak := payload["hypothesis"]; leak {
		t.Fatal("sender's Private state must not leak into the IPC payload")
	}
}
