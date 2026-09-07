// Package diff provides the Runtime Diff Engine.
//
// It compares two Genome snapshots and produces RuntimePatches.
// This is the ONLY component that knows how to translate
// genome differences into runtime mutations.
//
// Architecture:
//
//	Genome (old) ──┐
//	                ├──→ Diff Engine ──→ []RuntimePatch
//	Genome (new) ──┘
package diff

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// Diff source constants.
const (
	srcWorkflow  = "diff.workflow"
	srcKnowledge = "diff.knowledge"
	srcScheduler = "diff.scheduler"
	srcRecovery  = "diff.recovery"
	srcMemory    = "diff.memory"
)

// Differ computes the difference between two genome snapshots
// and produces RuntimePatches to transition from old to new.
type Differ interface {
	// Name returns the differ identifier, matching a genome name.
	Name() string

	// Diff compares old and new snapshots, returning patches
	// that would transform old into new.
	Diff(ctx context.Context, old, new any) ([]patch.RuntimePatch, error)
}

// SnapshotPair pairs the old and new snapshot for a genome.
type SnapshotPair struct {
	Old any
	New any
}

// Registry manages pluggable Differ implementations.
type Registry struct {
	mu    sync.RWMutex
	diffs map[string]Differ
}

// NewRegistry creates a new Diff Registry.
func NewRegistry() *Registry {
	return &Registry{
		diffs: make(map[string]Differ),
	}
}

// Register adds a Differ for a genome name.
func (r *Registry) Register(d Differ) error {
	if d == nil {
		return errors.New("diff: cannot register nil differ")
	}
	name := d.Name()
	if name == "" {
		return errors.New("diff: differ name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.diffs[name]; exists {
		return fmt.Errorf("diff: differ %q already registered", name)
	}
	r.diffs[name] = d
	return nil
}

// Get returns the Differ registered for the given genome name.
func (r *Registry) Get(name string) (Differ, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.diffs[name]
	if !ok {
		return nil, fmt.Errorf("diff: no differ for %q", name)
	}
	return d, nil
}

// List returns all registered differ names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.diffs))
	for name := range r.diffs {
		names = append(names, name)
	}
	return names
}

// DiffAll computes patches for all snapshot pairs.
// Each pair key must match a registered Differ name.
func (r *Registry) DiffAll(ctx context.Context, snapshots map[string]SnapshotPair) ([]patch.RuntimePatch, error) {
	var allPatches []patch.RuntimePatch
	for name, pair := range snapshots {
		differ, err := r.Get(name)
		if err != nil {
			// A snapshot key without a registered Differ is configuration
			// drift, not a transient error: surface it instead of silently
			// skipping so GA pipelines cannot hide misconfigured differ names.
			return nil, fmt.Errorf("diff %s: no differ registered: %w", name, err)
		}
		patches, err := differ.Diff(ctx, pair.Old, pair.New)
		if err != nil {
			return nil, fmt.Errorf("diff %s: %w", name, err)
		}
		allPatches = append(allPatches, patches...)
	}
	return allPatches, nil
}
