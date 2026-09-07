// Package engine provides the workflow execution engine with recoverable step execution.
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// RecoveryPatchExecutor handles recovery-related runtime patches.
// It wraps a MutableDAG and applies ChangeRecoveryStrategy/ChangeMaxRetries/
// ChangeBackoff. Implements patch.RuntimeComponent for unified runtime evolution.
type RecoveryPatchExecutor struct {
	dag *MutableDAG
}

// NewRecoveryPatchExecutor creates a new RecoveryPatchExecutor.
func NewRecoveryPatchExecutor(dag *MutableDAG) *RecoveryPatchExecutor {
	return &RecoveryPatchExecutor{dag: dag}
}

// SetDAG replaces the executor's DAG reference with a live one.
// Called after agents are created so recovery patches mutate the
// agent's real DAG rather than the bootstrap placeholder.
func (e *RecoveryPatchExecutor) SetDAG(dag *MutableDAG) {
	if dag == nil {
		return
	}
	e.dag = dag
}

// Name returns "recovery" as the component identifier for patch routing.
func (e *RecoveryPatchExecutor) Name() string { return "recovery" }

// Snapshot returns the live DAG reference. This is a deliberate contract:
// after SetDAG, recovery patches must mutate the agent's REAL DAG (not a
// copy), which is guarded by TestUpdateLiveDAG_DoesNotFailOnRegisteredExecutors
// (assert.Same(liveDAG, snapshot)). The live reference is only handed to the
// recovery patch executor, never to arbitrary observers.
func (e *RecoveryPatchExecutor) Snapshot(_ context.Context) (any, error) {
	if e.dag == nil {
		return nil, patch.ErrNoSnapshot
	}
	return e.dag, nil
}

// Ensure RecoveryPatchExecutor implements patch.RuntimeComponent.
var _ patch.RuntimeComponent = (*RecoveryPatchExecutor)(nil)

// Apply applies a runtime patch to the DAG's recovery configuration.
func (e *RecoveryPatchExecutor) Apply(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	switch p.Type {
	case patch.PatchChangeRecoveryStrategy:
		return e.applyChangeStrategy(p)
	case patch.PatchChangeMaxRetries:
		return e.applyChangeMaxRetries(p)
	case patch.PatchChangeBackoff:
		return e.applyChangeBackoff(p)
	default:
		return nil, fmt.Errorf("recovery executor: unsupported patch type %s", p.Type)
	}
}

// CanApply checks whether a patch can be applied.
func (e *RecoveryPatchExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	if e.dag == nil {
		return errors.New("recovery executor: dag is nil")
	}
	switch p.Type {
	case patch.PatchChangeRecoveryStrategy:
		// Rollback patches carry a *recoveryStrategySnapshot; forward patches
		// carry a strategy string. Accept both so a rollback patch re-submitted
		// through a CanApply-gated path is not rejected.
		if _, ok := p.Value.(*recoveryStrategySnapshot); ok {
			return nil
		}
		strategy, ok := p.Value.(string)
		if !ok {
			return errors.New("recovery executor: ChangeRecoveryStrategy value must be string or strategy snapshot")
		}
		switch RecoveryStrategy(strategy) {
		case RecoveryRetry, RecoveryReplaceNode, RecoveryFailFast:
			return nil
		default:
			return fmt.Errorf("recovery executor: unknown strategy %q", strategy)
		}
	case patch.PatchChangeMaxRetries:
		// Rollback patches carry a *recoveryMaxAttemptsSnapshot; forward
		// patches carry an int. Accept both for the same reason as above.
		if _, ok := p.Value.(*recoveryMaxAttemptsSnapshot); ok {
			return nil
		}
		if _, ok := p.Value.(int); !ok {
			return errors.New("recovery executor: ChangeMaxRetries value must be int or max-attempts snapshot")
		}
		return nil
	case patch.PatchChangeBackoff:
		// Rollback patches carry a *recoveryBackoffSnapshot; forward patches
		// carry a time.Duration. Accept both for the same reason as above.
		if _, ok := p.Value.(*recoveryBackoffSnapshot); ok {
			return nil
		}
		if _, ok := p.Value.(time.Duration); !ok {
			return errors.New("recovery executor: ChangeBackoff value must be time.Duration")
		}
		return nil
	default:
		return fmt.Errorf("recovery executor: unsupported patch type %s", p.Type)
	}
}

// recoveryStrategySnapshot captures the per-step recovery strategy state
// before a ChangeRecoveryStrategy patch is applied. It enables a symmetric
// rollback that restores each step to its individual prior configuration,
// including removing policies the patch created for previously-policyless steps.
//
// strategies maps every touched step ID to its prior Strategy (zero value ""
// when the step had no RecoveryPolicy). hadPolicy records whether each step
// owned a RecoveryPolicy before the patch; rollback uses this to decide
// between restoring the field value and clearing the policy entirely so
// steps that gained a policy are returned to nil.
//
// Guarding lock: e.dag.mu (write) must be held while reading or applying a
// snapshot, since it covers e.dag.steps and each step's RecoveryPolicy field.
type recoveryStrategySnapshot struct {
	strategies map[string]RecoveryStrategy
	hadPolicy  map[string]bool
}

// recoveryMaxAttemptsSnapshot captures per-step MaxAttempts state for rollback,
// mirroring recoveryStrategySnapshot. Steps that had no RecoveryPolicy before
// the patch have hadPolicy=false so rollback removes the policy it created.
//
// Guarding lock: e.dag.mu (write).
type recoveryMaxAttemptsSnapshot struct {
	maxAttempts map[string]int
	hadPolicy   map[string]bool
}

// recoveryBackoffSnapshot captures per-step Backoff state for rollback,
// mirroring recoveryStrategySnapshot/recoveryMaxAttemptsSnapshot. Steps that
// had no RecoveryPolicy before the patch have hadPolicy=false so rollback
// removes the policy it created.
//
// Guarding lock: e.dag.mu (write).
type recoveryBackoffSnapshot struct {
	backoff   map[string]time.Duration
	hadPolicy map[string]bool
}

// applyChangeStrategy applies a ChangeRecoveryStrategy patch. A forward patch
// carries a string strategy applied to every step; a rollback patch carries a
// *recoveryStrategySnapshot that restores each step to its individual prior
// value. The whole read-modify-write runs under e.dag.mu (write) so concurrent
// DAG reads/mutations cannot observe a half-applied strategy or race on the
// live *Step pointers.
func (e *RecoveryPatchExecutor) applyChangeStrategy(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Rollback path: restore each step from the per-step snapshot.
	if snap, ok := p.Value.(*recoveryStrategySnapshot); ok {
		return e.restoreStrategySnapshot(snap)
	}

	strategy, ok := p.Value.(string)
	if !ok {
		return nil, errors.New("recovery executor: ChangeRecoveryStrategy value must be string")
	}
	newStrategy := RecoveryStrategy(strategy)

	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	if len(e.dag.steps) == 0 {
		return nil, errors.New("recovery executor: no steps in DAG to apply strategy")
	}

	snap := &recoveryStrategySnapshot{
		strategies: make(map[string]RecoveryStrategy, len(e.dag.steps)),
		hadPolicy:  make(map[string]bool, len(e.dag.steps)),
	}
	for id, step := range e.dag.steps {
		snap.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			snap.strategies[id] = step.RecoveryPolicy.Strategy
		}
		// Create a policy for steps without one so the strategy applies
		// uniformly; rollback clears it via hadPolicy=false.
		if step.RecoveryPolicy == nil {
			step.RecoveryPolicy = &RecoveryPolicy{}
		}
		step.RecoveryPolicy.Strategy = newStrategy
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeRecoveryStrategy,
		Value:  snap,
		Reason: "rollback: restore previous recovery strategy",
	}, nil
}

// restoreStrategySnapshot restores per-step strategy state from a snapshot
// under the write lock. It returns a fresh snapshot of the pre-restoration
// state so the rollback is itself reversible.
func (e *RecoveryPatchExecutor) restoreStrategySnapshot(snap *recoveryStrategySnapshot) (*patch.RuntimePatch, error) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	current := &recoveryStrategySnapshot{
		strategies: make(map[string]RecoveryStrategy, len(snap.hadPolicy)),
		hadPolicy:  make(map[string]bool, len(snap.hadPolicy)),
	}
	for id, hadPolicy := range snap.hadPolicy {
		step, ok := e.dag.steps[id]
		if !ok {
			// Step was removed between patch and rollback; skip it.
			continue
		}
		current.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			current.strategies[id] = step.RecoveryPolicy.Strategy
		}
		if hadPolicy {
			// Step had a policy before the patch; restore the field value.
			if step.RecoveryPolicy == nil {
				step.RecoveryPolicy = &RecoveryPolicy{}
			}
			step.RecoveryPolicy.Strategy = snap.strategies[id]
		} else {
			// Step gained a policy from the patch; remove it to restore state.
			step.RecoveryPolicy = nil
		}
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeRecoveryStrategy,
		Value:  current,
		Reason: "rollback: re-apply recovery strategy",
	}, nil
}

// applyChangeMaxRetries applies a ChangeMaxRetries patch. A forward patch
// carries an int applied to every step; a rollback patch carries a
// *recoveryMaxAttemptsSnapshot that restores each step individually. The whole
// read-modify-write runs under e.dag.mu (write) for the same reasons as
// applyChangeStrategy. Policy creation for previously-policyless steps is
// consistent with applyChangeStrategy so rollback stays symmetric.
func (e *RecoveryPatchExecutor) applyChangeMaxRetries(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Rollback path: restore each step from the per-step snapshot.
	if snap, ok := p.Value.(*recoveryMaxAttemptsSnapshot); ok {
		return e.restoreMaxAttemptsSnapshot(snap)
	}

	newMax, ok := p.Value.(int)
	if !ok {
		return nil, errors.New("recovery executor: ChangeMaxRetries value must be int")
	}

	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	if len(e.dag.steps) == 0 {
		return nil, errors.New("recovery executor: no steps in DAG to apply max retries")
	}

	snap := &recoveryMaxAttemptsSnapshot{
		maxAttempts: make(map[string]int, len(e.dag.steps)),
		hadPolicy:   make(map[string]bool, len(e.dag.steps)),
	}
	for id, step := range e.dag.steps {
		snap.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			snap.maxAttempts[id] = step.RecoveryPolicy.MaxAttempts
		}
		// Create a policy for steps without one so max retries applies
		// uniformly (consistent with ChangeRecoveryStrategy); rollback
		// clears it via hadPolicy=false.
		if step.RecoveryPolicy == nil {
			step.RecoveryPolicy = &RecoveryPolicy{}
		}
		step.RecoveryPolicy.MaxAttempts = newMax
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeMaxRetries,
		Value:  snap,
		Reason: "rollback: restore previous max retries",
	}, nil
}

// restoreMaxAttemptsSnapshot restores per-step MaxAttempts state from a
// snapshot under the write lock. It returns a fresh snapshot of the
// pre-restoration state so the rollback is itself reversible.
func (e *RecoveryPatchExecutor) restoreMaxAttemptsSnapshot(
	snap *recoveryMaxAttemptsSnapshot,
) (*patch.RuntimePatch, error) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	current := &recoveryMaxAttemptsSnapshot{
		maxAttempts: make(map[string]int, len(snap.hadPolicy)),
		hadPolicy:   make(map[string]bool, len(snap.hadPolicy)),
	}
	for id, hadPolicy := range snap.hadPolicy {
		step, ok := e.dag.steps[id]
		if !ok {
			continue
		}
		current.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			current.maxAttempts[id] = step.RecoveryPolicy.MaxAttempts
		}
		if hadPolicy {
			if step.RecoveryPolicy == nil {
				step.RecoveryPolicy = &RecoveryPolicy{}
			}
			step.RecoveryPolicy.MaxAttempts = snap.maxAttempts[id]
		} else {
			step.RecoveryPolicy = nil
		}
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeMaxRetries,
		Value:  current,
		Reason: "rollback: re-apply max retries",
	}, nil
}

// applyChangeBackoff applies a ChangeBackoff patch. A forward patch carries a
// time.Duration applied to every step; a rollback patch carries a
// *recoveryBackoffSnapshot that restores each step individually. The whole
// read-modify-write runs under e.dag.mu (write) for the same reasons as
// applyChangeStrategy/applyChangeMaxRetries. Policy creation for
// previously-policyless steps is consistent with the sibling apply functions so
// rollback stays symmetric: a step that gained a policy here is returned to nil
// on rollback rather than keeping a zero-backoff policy.
func (e *RecoveryPatchExecutor) applyChangeBackoff(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Rollback path: restore each step from the per-step snapshot.
	if snap, ok := p.Value.(*recoveryBackoffSnapshot); ok {
		return e.restoreBackoffSnapshot(snap)
	}

	newBackoff, ok := p.Value.(time.Duration)
	if !ok {
		return nil, errors.New("recovery executor: ChangeBackoff value must be time.Duration")
	}

	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	if len(e.dag.steps) == 0 {
		return nil, errors.New("recovery executor: no steps in DAG to apply backoff")
	}

	snap := &recoveryBackoffSnapshot{
		backoff:   make(map[string]time.Duration, len(e.dag.steps)),
		hadPolicy: make(map[string]bool, len(e.dag.steps)),
	}
	for id, step := range e.dag.steps {
		snap.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			snap.backoff[id] = step.RecoveryPolicy.Backoff
		}
		// Create a policy for steps without one so backoff applies uniformly
		// (consistent with ChangeRecoveryStrategy/ChangeMaxRetries); rollback
		// clears it via hadPolicy=false.
		if step.RecoveryPolicy == nil {
			step.RecoveryPolicy = &RecoveryPolicy{}
		}
		step.RecoveryPolicy.Backoff = newBackoff
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeBackoff,
		Value:  snap,
		Reason: "rollback: restore previous backoff",
	}, nil
}

// restoreBackoffSnapshot restores per-step Backoff state from a snapshot under
// the write lock. It returns a fresh snapshot of the pre-restoration state so
// the rollback is itself reversible.
func (e *RecoveryPatchExecutor) restoreBackoffSnapshot(
	snap *recoveryBackoffSnapshot,
) (*patch.RuntimePatch, error) {
	e.dag.mu.Lock()
	defer e.dag.mu.Unlock()

	current := &recoveryBackoffSnapshot{
		backoff:   make(map[string]time.Duration, len(snap.hadPolicy)),
		hadPolicy: make(map[string]bool, len(snap.hadPolicy)),
	}
	for id, hadPolicy := range snap.hadPolicy {
		step, ok := e.dag.steps[id]
		if !ok {
			continue
		}
		current.hadPolicy[id] = step.RecoveryPolicy != nil
		if step.RecoveryPolicy != nil {
			current.backoff[id] = step.RecoveryPolicy.Backoff
		}
		if hadPolicy {
			if step.RecoveryPolicy == nil {
				step.RecoveryPolicy = &RecoveryPolicy{}
			}
			step.RecoveryPolicy.Backoff = snap.backoff[id]
		} else {
			step.RecoveryPolicy = nil
		}
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeBackoff,
		Value:  current,
		Reason: "rollback: re-apply backoff",
	}, nil
}
