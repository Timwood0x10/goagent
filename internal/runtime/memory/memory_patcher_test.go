package memory

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// TestMemoryPatchExecutor_ApplyAndRollback verifies a memory patch mutates the
// live config and that the returned rollback patch restores the previous values.
// This guards the P1 bug where rollback carried a MemoryConfig struct (not a
// map), so re-applying it silently no-op'd and never restored the config.
func TestMemoryPatchExecutor_ApplyAndRollback(t *testing.T) {
	store := NewMinimalMemoryManager()
	ex := NewMemoryPatchExecutor(store)
	ctx := context.Background()

	prev := *store.GetConfig()

	p := patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Target: "memory",
		Value:  map[string]any{"max_history": 42, "max_sessions": 7},
	}
	rb, err := ex.Apply(ctx, p)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if got := store.GetConfig().MaxHistory; got != 42 {
		t.Errorf("MaxHistory = %d, want 42", got)
	}
	if got := store.GetConfig().MaxSessions; got != 7 {
		t.Errorf("MaxSessions = %d, want 7", got)
	}
	if rb == nil {
		t.Fatal("expected rollback patch, got nil")
	}

	if _, err := ex.Apply(ctx, *rb); err != nil {
		t.Fatalf("rollback Apply failed: %v", err)
	}
	after := store.GetConfig()
	if after.MaxHistory != prev.MaxHistory {
		t.Errorf("after rollback MaxHistory = %d, want %d", after.MaxHistory, prev.MaxHistory)
	}
	if after.MaxSessions != prev.MaxSessions {
		t.Errorf("after rollback MaxSessions = %d, want %d", after.MaxSessions, prev.MaxSessions)
	}
}

// TestMemoryPatchExecutor_BadSessionTTL_NoPartialMutate verifies validate-before-mutate:
// an invalid session_ttl must not partially apply the rest of the patch
// (max_distilled_tasks), which was the prior data-corruption path.
func TestMemoryPatchExecutor_BadSessionTTL_NoPartialMutate(t *testing.T) {
	store := NewMinimalMemoryManager()
	ex := NewMemoryPatchExecutor(store)
	ctx := context.Background()
	prev := *store.GetConfig()

	p := patch.RuntimePatch{
		Type:   patch.PatchChangeBudget,
		Target: "memory",
		Value:  map[string]any{"max_distilled_tasks": 999, "session_ttl": "not-a-duration"},
	}
	if _, err := ex.Apply(ctx, p); err == nil {
		t.Fatal("expected error for invalid session_ttl, got nil")
	}
	if got := store.GetConfig().MaxDistilledTasks; got != prev.MaxDistilledTasks {
		t.Errorf("MaxDistilledTasks = %d, want %d (unchanged on validation failure)", got, prev.MaxDistilledTasks)
	}
}

// TestMemoryPatchExecutor_BadValueType_ReturnsError verifies an ill-typed Value
// is rejected rather than silently no-op'd (prior behavior masked malformed patches).
func TestMemoryPatchExecutor_BadValueType_ReturnsError(t *testing.T) {
	store := NewMinimalMemoryManager()
	ex := NewMemoryPatchExecutor(store)
	ctx := context.Background()

	p := patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Target: "memory",
		Value:  "not-a-map",
	}
	if _, err := ex.Apply(ctx, p); err == nil {
		t.Fatal("expected error for non-map Value, got nil")
	}
}
