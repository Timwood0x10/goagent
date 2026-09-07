package diff

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	aresmemory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

func TestMemoryDiffer_Diff(t *testing.T) {
	d := NewMemoryDiffer()
	oldCfg := genome.MemoryGenomeConfig{MaxHistory: 10, MaxSessions: 100, MaxDistilledTasks: 5000, UseStructuredCleaning: false}
	newCfg := genome.MemoryGenomeConfig{MaxHistory: 20, MaxSessions: 100, MaxDistilledTasks: 5000, UseStructuredCleaning: false}

	patches, err := d.Diff(context.Background(), oldCfg, newCfg)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if len(patches) != 1 {
		t.Fatalf("expected 1 patch, got %d", len(patches))
	}
	if patches[0].Target != "memory" {
		t.Errorf("Target = %q, want %q", patches[0].Target, "memory")
	}
	if patches[0].Type != patch.PatchChangePlanner {
		t.Errorf("Type = %v, want PatchChangePlanner", patches[0].Type)
	}
}

// TestMemoryDiffer_RoutedToExecutor verifies the end-to-end path that the
// review found broken: a MemoryDiffer patch with Target "memory" must route
// through the patch Registry to MemoryPatchExecutor and mutate the live store.
// This is the regression test for the historical Target mismatch ("memory.config"
// never matched a registered key, so patches fell through to the DAG fallback).
func TestMemoryDiffer_RoutedToExecutor(t *testing.T) {
	d := NewMemoryDiffer()
	store := aresmemory.NewMinimalMemoryManager()
	reg := patch.NewRegistry()
	if err := reg.RegisterComponent(aresmemory.NewMemoryPatchExecutor(store)); err != nil {
		t.Fatalf("register executor: %v", err)
	}

	oldCfg := genome.MemoryGenomeConfig{MaxHistory: 10, MaxSessions: 100, MaxDistilledTasks: 5000, UseStructuredCleaning: false}
	newCfg := genome.MemoryGenomeConfig{MaxHistory: 30, MaxSessions: 100, MaxDistilledTasks: 5000, UseStructuredCleaning: false}

	patches, err := d.Diff(context.Background(), oldCfg, newCfg)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	for _, p := range patches {
		if err := reg.Apply(context.Background(), p); err != nil {
			t.Fatalf("Apply patch %s failed: %v", p.Type, err)
		}
	}
	if got := store.GetConfig().MaxHistory; got != 30 {
		t.Errorf("MaxHistory = %d, want 30 (memory patch must route to executor)", got)
	}
}
