package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func TestNewRecoveryPatchExecutor(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)
	require.NotNil(t, exec)
	assert.Same(t, dag, exec.dag)
}

func TestRecoveryPatchExecutor_Apply_ChangeRecoveryStrategy(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeRecoveryStrategy,
		Target: "recovery.strategy",
		Value:  string(RecoveryReplaceNode),
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeRecoveryStrategy, rollback.Type)

	// Verify all steps have the new strategy.
	for _, step := range dag.Steps() {
		if step.RecoveryPolicy != nil {
			assert.Equal(t, RecoveryReplaceNode, step.RecoveryPolicy.Strategy,
				"step %s should have ReplaceNode strategy", step.ID)
		}
	}
}

func TestRecoveryPatchExecutor_Apply_ChangeMaxRetries(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	// First set a recovery policy on step A.
	steps := dag.Steps()
	require.Greater(t, len(steps), 0)
	steps[0].RecoveryPolicy = &RecoveryPolicy{Strategy: RecoveryRetry, MaxAttempts: 2}

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeMaxRetries,
		Target: "recovery.max_attempts",
		Value:  5,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeMaxRetries, rollback.Type)

	// Verify the policy was updated.
	assert.Equal(t, 5, steps[0].RecoveryPolicy.MaxAttempts)
}

func TestRecoveryPatchExecutor_Apply_ChangeBackoff(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	// First set a recovery policy on step A.
	steps := dag.Steps()
	require.Greater(t, len(steps), 0)
	steps[0].RecoveryPolicy = &RecoveryPolicy{Strategy: RecoveryRetry, MaxAttempts: 2}

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeBackoff,
		Target: "recovery.backoff",
		Value:  500 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeBackoff, rollback.Type)

	// Verify the policy was updated and the rollback carries a snapshot.
	assert.Equal(t, 500*time.Millisecond, steps[0].RecoveryPolicy.Backoff)
	_, ok := rollback.Value.(*recoveryBackoffSnapshot)
	assert.True(t, ok, "rollback Value must be a *recoveryBackoffSnapshot")
}

func TestRecoveryPatchExecutor_Apply_UnsupportedType(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchType(999),
	})
	assert.Error(t, err)
}

func TestRecoveryPatchExecutor_CanApply(t *testing.T) {
	dag := newTestDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	tests := []struct {
		name  string
		patch patch.RuntimePatch
		want  bool
	}{
		{"change strategy valid", patch.RuntimePatch{Type: patch.PatchChangeRecoveryStrategy, Target: "strategy", Value: "retry"}, true},
		{"change strategy valid replace", patch.RuntimePatch{Type: patch.PatchChangeRecoveryStrategy, Target: "strategy", Value: "replace_node"}, true},
		{"change strategy valid fail_fast", patch.RuntimePatch{Type: patch.PatchChangeRecoveryStrategy, Target: "strategy", Value: "fail_fast"}, true},
		{"change strategy invalid value", patch.RuntimePatch{Type: patch.PatchChangeRecoveryStrategy, Value: 42}, false},
		{"change strategy unknown", patch.RuntimePatch{Type: patch.PatchChangeRecoveryStrategy, Value: "unknown"}, false},
		{"change max retries valid", patch.RuntimePatch{Type: patch.PatchChangeMaxRetries, Value: 3}, true},
		{"change max retries invalid", patch.RuntimePatch{Type: patch.PatchChangeMaxRetries, Value: "bad"}, false},
		{"change backoff valid", patch.RuntimePatch{Type: patch.PatchChangeBackoff, Value: 250 * time.Millisecond}, true},
		{"change backoff invalid", patch.RuntimePatch{Type: patch.PatchChangeBackoff, Value: "bad"}, false},
		{"change backoff wrong numeric type", patch.RuntimePatch{Type: patch.PatchChangeBackoff, Value: 250}, false},
		{"unsupported type", patch.RuntimePatch{Type: patch.PatchType(999)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exec.CanApply(context.Background(), tt.patch)
			if tt.want {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestRecoveryPatchExecutor_CanApply_NilDAG(t *testing.T) {
	exec := &RecoveryPatchExecutor{dag: nil}
	err := exec.CanApply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchChangeRecoveryStrategy, Value: "retry",
	})
	assert.Error(t, err)
}

// newTestDAG creates a simple test DAG for recovery tests.
func newTestDAG(t *testing.T) *MutableDAG {
	t.Helper()
	steps := []*Step{
		{ID: "A", Name: "Step A", AgentType: "test", Input: "a"},
		{ID: "B", Name: "Step B", AgentType: "test", Input: "b", DependsOn: []string{"A"}},
	}
	dag, err := NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}

// newHeterogeneousRecoveryDAG builds a DAG whose steps have distinct recovery
// configurations: A and B carry differing RecoveryPolicy values (including
// distinct Backoff), while C has no RecoveryPolicy at all. This exposes
// rollback bugs that capture only the last step's old value and apply it to
// every step.
func newHeterogeneousRecoveryDAG(t *testing.T) *MutableDAG {
	t.Helper()
	steps := []*Step{
		{
			ID: "A", Name: "Step A", AgentType: "test", Input: "a",
			RecoveryPolicy: &RecoveryPolicy{
				Strategy:    RecoveryRetry,
				MaxAttempts: 2,
				Backoff:     100 * time.Millisecond,
			},
		},
		{
			ID: "B", Name: "Step B", AgentType: "test", Input: "b", DependsOn: []string{"A"},
			RecoveryPolicy: &RecoveryPolicy{
				Strategy:    RecoveryFailFast,
				MaxAttempts: 7,
				Backoff:     2 * time.Second,
			},
		},
		{
			ID: "C", Name: "Step C", AgentType: "test", Input: "c", DependsOn: []string{"B"},
			// No RecoveryPolicy on purpose.
		},
	}
	dag, err := NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}

// TestRecoveryPatchExecutor_ChangeStrategy_Rollback_RestoresPerStep reproduces
// the C2 bug: the rollback patch captured only the last step's old strategy and
// reapplied that single value to every step. With heterogeneous configs the
// rollback must restore each step to its own prior value, including removing the
// policy that was created for the previously-policyless step C.
func TestRecoveryPatchExecutor_ChangeStrategy_Rollback_RestoresPerStep(t *testing.T) {
	dag := newHeterogeneousRecoveryDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeRecoveryStrategy,
		Value: string(RecoveryReplaceNode),
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)

	// Forward: every step now carries the ReplaceNode strategy (C gained a policy).
	for _, s := range dag.Steps() {
		require.NotNil(t, s.RecoveryPolicy, "step %s should have a policy after forward patch", s.ID)
		assert.Equal(t, RecoveryReplaceNode, s.RecoveryPolicy.Strategy)
	}

	// Apply rollback; each step must return to its individual prior state.
	_, err = exec.Apply(context.Background(), *rollback)
	require.NoError(t, err)

	restored := make(map[string]*RecoveryPolicy)
	for _, s := range dag.Steps() {
		restored[s.ID] = s.RecoveryPolicy
	}
	require.NotNil(t, restored["A"])
	assert.Equal(t, RecoveryRetry, restored["A"].Strategy, "step A strategy must be restored")
	assert.Equal(t, 2, restored["A"].MaxAttempts, "step A max attempts must be untouched")
	assert.Equal(t, 100*time.Millisecond, restored["A"].Backoff, "step A backoff must be untouched")
	require.NotNil(t, restored["B"])
	assert.Equal(t, RecoveryFailFast, restored["B"].Strategy, "step B strategy must be restored")
	assert.Equal(t, 7, restored["B"].MaxAttempts, "step B max attempts must be untouched")
	assert.Equal(t, 2*time.Second, restored["B"].Backoff, "step B backoff must be untouched")
	assert.Nil(t, restored["C"], "step C policy must be removed on rollback")
}

// TestRecoveryPatchExecutor_ChangeMaxRetries_Rollback_RestoresPerStep reproduces
// the C2 bug for MaxRetries: rollback captured only the last step's old
// MaxAttempts and reapplied it to every step, corrupting heterogeneous configs.
func TestRecoveryPatchExecutor_ChangeMaxRetries_Rollback_RestoresPerStep(t *testing.T) {
	dag := newHeterogeneousRecoveryDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeMaxRetries,
		Value: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)

	// Forward: every step that already had a policy now has MaxAttempts=10.
	// (Step C's policy creation is exercised by the rollback symmetry below.)
	for _, s := range dag.Steps() {
		if s.RecoveryPolicy != nil {
			assert.Equal(t, 10, s.RecoveryPolicy.MaxAttempts)
		}
	}

	// Apply rollback; each step must return to its individual prior state.
	_, err = exec.Apply(context.Background(), *rollback)
	require.NoError(t, err)

	restored := make(map[string]*RecoveryPolicy)
	for _, s := range dag.Steps() {
		restored[s.ID] = s.RecoveryPolicy
	}
	require.NotNil(t, restored["A"])
	assert.Equal(t, 2, restored["A"].MaxAttempts, "step A max attempts must be restored")
	assert.Equal(t, RecoveryRetry, restored["A"].Strategy, "step A strategy must be untouched")
	assert.Equal(t, 100*time.Millisecond, restored["A"].Backoff, "step A backoff must be untouched")
	require.NotNil(t, restored["B"])
	assert.Equal(t, 7, restored["B"].MaxAttempts, "step B max attempts must be restored")
	assert.Equal(t, RecoveryFailFast, restored["B"].Strategy, "step B strategy must be untouched")
	assert.Equal(t, 2*time.Second, restored["B"].Backoff, "step B backoff must be untouched")
	assert.Nil(t, restored["C"], "step C policy must be removed on rollback")
}

// TestRecoveryPatchExecutor_ChangeBackoff_Rollback_RestoresPerStep reproduces
// the same per-step rollback bug class for Backoff: a no-op stub would capture
// nothing and rollback would restore nothing. With heterogeneous configs the
// rollback must restore each step to its own prior Backoff, including removing
// the policy that was created for the previously-policyless step C.
func TestRecoveryPatchExecutor_ChangeBackoff_Rollback_RestoresPerStep(t *testing.T) {
	dag := newHeterogeneousRecoveryDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeBackoff,
		Value: 750 * time.Millisecond,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)

	// Forward: every step now carries Backoff=750ms (C gained a policy).
	for _, s := range dag.Steps() {
		require.NotNil(t, s.RecoveryPolicy, "step %s should have a policy after forward patch", s.ID)
		assert.Equal(t, 750*time.Millisecond, s.RecoveryPolicy.Backoff)
	}

	// Apply rollback; each step must return to its individual prior state.
	_, err = exec.Apply(context.Background(), *rollback)
	require.NoError(t, err)

	restored := make(map[string]*RecoveryPolicy)
	for _, s := range dag.Steps() {
		restored[s.ID] = s.RecoveryPolicy
	}
	require.NotNil(t, restored["A"])
	assert.Equal(t, 100*time.Millisecond, restored["A"].Backoff, "step A backoff must be restored")
	assert.Equal(t, RecoveryRetry, restored["A"].Strategy, "step A strategy must be untouched")
	assert.Equal(t, 2, restored["A"].MaxAttempts, "step A max attempts must be untouched")
	require.NotNil(t, restored["B"])
	assert.Equal(t, 2*time.Second, restored["B"].Backoff, "step B backoff must be restored")
	assert.Equal(t, RecoveryFailFast, restored["B"].Strategy, "step B strategy must be untouched")
	assert.Equal(t, 7, restored["B"].MaxAttempts, "step B max attempts must be untouched")
	assert.Nil(t, restored["C"], "step C policy must be removed on rollback")
}

// TestRecoveryPatchExecutor_ChangeStrategy_Concurrent_NoRace reproduces the C3
// bug: applyChangeStrategy called Steps() (which released the read lock) and
// then mutated the live *Step pointers without any lock. Two concurrent
// applyChangeStrategy calls therefore race on step.RecoveryPolicy. Under -race
// this must stay clean once the whole read-modify-write runs under the write lock.
// Backoff patches are included so the same guarantee is verified for the new
// applyChangeBackoff path.
func TestRecoveryPatchExecutor_ChangeStrategy_Concurrent_NoRace(t *testing.T) {
	dag := newHeterogeneousRecoveryDAG(t)
	exec := NewRecoveryPatchExecutor(dag)

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, _ = exec.Apply(context.Background(), patch.RuntimePatch{
				Type:  patch.PatchChangeRecoveryStrategy,
				Value: string(RecoveryReplaceNode),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = exec.Apply(context.Background(), patch.RuntimePatch{
				Type:  patch.PatchChangeMaxRetries,
				Value: 4,
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = exec.Apply(context.Background(), patch.RuntimePatch{
				Type:  patch.PatchChangeBackoff,
				Value: 300 * time.Millisecond,
			})
		}()
	}
	wg.Wait()
}
