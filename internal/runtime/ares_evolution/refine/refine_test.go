package refine

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is an in-memory Store for tests.
type memStore struct {
	mu  sync.Mutex
	kv  map[string]any
	set int
}

func newMemStore() *memStore {
	return &memStore{kv: make(map[string]any)}
}

func (s *memStore) Get(_ context.Context, target string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.kv[target]
	return v, ok
}

func (s *memStore) Set(_ context.Context, target string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kv[target] = value
	s.set++
	return nil
}

func TestRefiner_ApplyAndRollback(t *testing.T) {
	store := newMemStore()
	store.kv["mem:exp-1"] = "old-prompt"
	r := NewRefiner(store)

	p := Proposal{
		ID:     "p1",
		Kind:   KindMemory,
		Target: "mem:exp-1",
		Before: "old-prompt",
		After:  "new-prompt",
		Reason: "harness refine",
	}
	require.NoError(t, r.Apply(context.Background(), p))
	got, ok := store.Get(context.Background(), "mem:exp-1")
	require.True(t, ok)
	assert.Equal(t, "new-prompt", got)
	assert.True(t, r.IsApplied("p1"))
	assert.Equal(t, 1, r.AppliedCount())

	require.NoError(t, r.Rollback(context.Background(), "p1"))
	got, ok = store.Get(context.Background(), "mem:exp-1")
	require.True(t, ok)
	assert.Equal(t, "old-prompt", got, "rollback must restore baseline")
	assert.False(t, r.IsApplied("p1"))
	assert.Equal(t, 0, r.AppliedCount())
}

func TestRefiner_ApplyIdempotent(t *testing.T) {
	store := newMemStore()
	store.kv["skill:s1"] = "v1"
	r := NewRefiner(store)

	p := Proposal{ID: "p1", Kind: KindSkill, Target: "skill:s1", Before: "v1", After: "v2"}
	require.NoError(t, r.Apply(context.Background(), p))
	require.NoError(t, r.Apply(context.Background(), p)) // duplicate delivery
	assert.Equal(t, 1, store.set, "second apply must be a no-op (idempotent)")
}

func TestRefiner_ApplyBaselineConflictRejected(t *testing.T) {
	store := newMemStore()
	store.kv["ctx:c1"] = "current-v2" // someone else already changed it
	r := NewRefiner(store)

	p := Proposal{ID: "p1", Kind: KindContext, Target: "ctx:c1", Before: "stale-v1", After: "v3"}
	err := r.Apply(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseline conflict")
	assert.False(t, r.IsApplied("p1"), "conflicted proposal must not be recorded")
}

func TestRefiner_ApplyCreatesNewEntry(t *testing.T) {
	store := newMemStore() // target absent: baseline is "absent", apply creates it
	r := NewRefiner(store)

	p := Proposal{ID: "p1", Kind: KindMemory, Target: "mem:new", After: "v1", Reason: "create"}
	require.NoError(t, r.Apply(context.Background(), p))
	got, ok := store.Get(context.Background(), "mem:new")
	require.True(t, ok)
	assert.Equal(t, "v1", got)
}

func TestRefiner_RollbackUnknownID(t *testing.T) {
	r := NewRefiner(newMemStore())
	err := r.Rollback(context.Background(), "ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not applied")
}

func TestRefiner_ValidateProposal(t *testing.T) {
	r := NewRefiner(newMemStore())
	err := r.Apply(context.Background(), Proposal{Kind: KindMemory, Target: "t", After: "v"})
	require.Error(t, err, "empty ID must be rejected")
}

// TestRefiner_MapBaselineNoFalseConflict verifies equal() treats maps with the
// same key-value pairs as equal regardless of iteration order. Previously the
// comparison used fmt.Sprintf("%v"), which renders map keys in nondeterministic
// order, so two equal maps could be reported as a false baseline conflict.
func TestRefiner_MapBaselineNoFalseConflict(t *testing.T) {
	store := newMemStore()
	store.kv["mem:exp-1"] = map[string]any{"a": 1, "b": 2}
	r := NewRefiner(store)

	p := Proposal{
		ID:     "p1",
		Kind:   KindMemory,
		Target: "mem:exp-1",
		Before: map[string]any{"b": 2, "a": 1}, // same pairs, different declaration order
		After:  map[string]any{"a": 1, "b": 2, "c": 3},
	}
	require.NoError(t, r.Apply(context.Background(), p),
		"equal maps with different iteration order must not be a baseline conflict")
}

// TestRefiner_MapBaselineRealConflict verifies a genuinely different map value
// still triggers the conflict detector.
func TestRefiner_MapBaselineRealConflict(t *testing.T) {
	store := newMemStore()
	store.kv["mem:exp-2"] = map[string]any{"a": 1, "b": 2}
	r := NewRefiner(store)

	p := Proposal{
		ID:     "p1",
		Kind:   KindMemory,
		Target: "mem:exp-2",
		Before: map[string]any{"a": 1, "b": 99},
		After:  map[string]any{"a": 1, "b": 2},
	}
	err := r.Apply(context.Background(), p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseline conflict")
}
