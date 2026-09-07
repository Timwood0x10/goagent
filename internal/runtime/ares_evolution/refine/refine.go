// Package refine implements Harness-style small-step evolution of supplement
// state (memory / skill / context entries): plan → apply → rollback with
// baseline conflict detection (primitive 1).
//
// The Refiner owns the apply/rollback protocol; the underlying store is
// injected (e.g. an ares_memory backing store), so this package stays a
// platform-level primitive with no LLM or storage dependency.
package refine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// RefineKind classifies the supplement state a Proposal mutates.
type RefineKind string

const (
	// KindMemory refines a memory entry (e.g. a distilled experience).
	KindMemory RefineKind = "memory"
	// KindSkill refines a skill/capability description.
	KindSkill RefineKind = "skill"
	// KindContext refines a conversation/context supplement entry.
	KindContext RefineKind = "context"
)

// Proposal is one small-step change to supplement state. Before is the value
// the store currently holds (baseline); After is the proposed new value.
// Rollback restores Before, so every applied edit is reversible.
type Proposal struct {
	// ID uniquely identifies this proposal (used for idempotency + rollback).
	ID string
	// Kind is the supplement-state category being edited.
	Kind RefineKind
	// Target is the store key of the entry being edited.
	Target string
	// Before is the baseline value (must match the store's current value on
	// Apply, otherwise the edit is a stale-write conflict and is rejected).
	Before any
	// After is the new value written on Apply.
	After any
	// Reason documents why this edit was proposed (kept for audit).
	Reason string
}

// Store is the read/write interface for supplement state. Implementations
// persist entries by Target key.
type Store interface {
	// Get returns the current value for target. ok is false when absent.
	Get(ctx context.Context, target string) (any, bool)
	// Set stores value for target.
	Set(ctx context.Context, target string, value any) error
}

// Refiner applies and rolls back Proposals against a Store with baseline
// conflict detection. Applied proposals are tracked so re-delivery is
// idempotent and every applied edit can be reversed.
type Refiner struct {
	store Store

	mu      sync.Mutex
	applied map[string]Proposal // proposal ID → applied proposal (for rollback)
}

// NewRefiner creates a Refiner over store.
func NewRefiner(store Store) *Refiner {
	return &Refiner{
		store:   store,
		applied: make(map[string]Proposal),
	}
}

// Apply validates the baseline, writes After, and records the proposal for
// rollback. It rejects a proposal whose ID was already applied (idempotent
// re-delivery) or whose Before does not match the store's current value
// (stale write / concurrent mutation).
//
// The idempotency check, the baseline read, and the store write are all done
// under the same mutex so two concurrent Apply calls for the same proposal ID
// cannot both pass the idempotency check and double-write, and a concurrent
// mutation cannot slip between the baseline read and the write.
func (r *Refiner) Apply(ctx context.Context, p Proposal) error {
	if p.ID == "" || p.Target == "" {
		return errors.New("refine: proposal ID and target must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.applied[p.ID]; dup {
		return nil // idempotent: already applied
	}

	if current, ok := r.store.Get(ctx, p.Target); ok {
		// Baseline conflict: the store no longer holds the value the proposal
		// was based on — reject rather than silently overwrite newer state.
		if !equal(current, p.Before) {
			return fmt.Errorf("refine: baseline conflict on %q: current %v != before %v",
				p.Target, current, p.Before)
		}
	}
	if err := r.store.Set(ctx, p.Target, p.After); err != nil {
		return fmt.Errorf("refine: apply %s: %w", p.ID, err)
	}

	r.applied[p.ID] = p
	return nil
}

// Rollback reverses an applied proposal by restoring Before. Unknown IDs are
// an error (nothing to reverse).
func (r *Refiner) Rollback(ctx context.Context, id string) error {
	r.mu.Lock()
	p, ok := r.applied[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("refine: proposal %q not applied, nothing to roll back", id)
	}
	if err := r.store.Set(ctx, p.Target, p.Before); err != nil {
		return fmt.Errorf("refine: rollback %s: %w", id, err)
	}
	r.mu.Lock()
	delete(r.applied, id)
	r.mu.Unlock()
	return nil
}

// IsApplied reports whether a proposal was already applied (idempotency check).
func (r *Refiner) IsApplied(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.applied[id]
	return ok
}

// AppliedCount returns the number of applied (not yet rolled back) proposals.
func (r *Refiner) AppliedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.applied)
}

// equal compares two supplement-state values. reflect.DeepEqual is used instead
// of fmt.Sprintf("%v") string comparison because %v renders map keys in
// nondeterministic order, so two equal maps could be reported unequal (a false
// baseline conflict). DeepEqual compares maps order-independently and slices
// order-sensitively, which matches the desired semantics.
func equal(a, b any) bool {
	return reflect.DeepEqual(a, b)
}
