package evolution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// profileTargetPrefix is the registry target prefix for agent role profiles.
// A runtime patch targeting profile instructions uses Target "profile:<role>".
const profileTargetPrefix = "profile:"

// ErrInvalidProfilePatch is returned when a patch does not target a profile.
var ErrInvalidProfilePatch = errors.New("evolution: patch does not target a profile")

// ProfileExecutor applies PatchChangeInstruction patches to the candidate
// profile region of a ProfileStore. It implements patch.Executor so it can be
// registered in a patch.Registry and driven by the EvolutionCoordinator.
type ProfileExecutor struct {
	store *ProfileStore
}

// NewProfileExecutor creates an executor that mutates candidate profiles.
// Args:
//
//	store - the profile store whose candidate region receives instruction
//	  changes; must be non-nil.
//
// Returns:
//
//	executor - the ready-to-use executor.
func NewProfileExecutor(store *ProfileStore) *ProfileExecutor {
	return &ProfileExecutor{store: store}
}

// Apply changes the Instructions of the target role's candidate profile and
// returns a rollback patch that restores the previous instructions.
// The patch Target must be "profile:<role>" and Value must be a string.
// Args:
//
//	ctx - timeout and cancellation context.
//	p - the instruction-change patch; Type must be PatchChangeInstruction.
//
// Returns:
//
//	rollback - a patch restoring the previous instructions, or nil when the
//	  change is a no-op.
//	err - ErrInvalidProfilePatch for a wrong target/type/value, or the store
//	  error when persisting the change.
func (e *ProfileExecutor) Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	if e.store == nil {
		return nil, errors.New("evolution: profile executor has nil store")
	}
	if err := e.CanApply(ctx, p); err != nil {
		return nil, err
	}

	role, err := profileRoleFromTarget(p.Target)
	if err != nil {
		return nil, err
	}

	profile := e.store.Get(role)
	if profile == nil {
		return nil, fmt.Errorf("evolution: candidate profile not found for role %q", role)
	}

	newInstructions, ok := p.Value.(string)
	if !ok {
		return nil, fmt.Errorf("evolution: instruction patch value must be a string, got %T", p.Value)
	}

	oldInstructions := profile.Instructions
	if oldInstructions == newInstructions {
		//nolint:nilnil // nil rollback + nil error is the documented "no-op change" contract.
		return nil, nil
	}
	profile.Instructions = newInstructions
	if err := e.store.Update(profile); err != nil {
		return nil, fmt.Errorf("evolution: persist profile %q: %w", role, err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeInstruction,
		Target: p.Target,
		Value:  oldInstructions,
		Reason: "rollback: restore previous instructions",
		Source: "rollback",
	}, nil
}

// CanApply validates that the patch targets an existing profile with a string
// value and a PatchChangeInstruction type.
// Args:
//
//	ctx - unused, kept for interface compatibility.
//	patch - the patch to validate.
//
// Returns:
//
//	err - nil when applicable, otherwise a descriptive error.
func (e *ProfileExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	if e.store == nil {
		return errors.New("evolution: profile executor has nil store")
	}
	if p.Type != patch.PatchChangeInstruction {
		return fmt.Errorf("evolution: unsupported patch type %s", p.Type)
	}
	role, err := profileRoleFromTarget(p.Target)
	if err != nil {
		return err
	}
	if e.store.Get(role) == nil {
		return fmt.Errorf("evolution: candidate profile not found for role %q", role)
	}
	if _, ok := p.Value.(string); !ok {
		return fmt.Errorf("evolution: instruction patch value must be a string, got %T", p.Value)
	}
	return nil
}

// profileRoleFromTarget extracts the role from a "profile:<role>" target.
func profileRoleFromTarget(target string) (string, error) {
	if !strings.HasPrefix(target, profileTargetPrefix) {
		return "", fmt.Errorf("%w: target %q", ErrInvalidProfilePatch, target)
	}
	role := strings.TrimPrefix(target, profileTargetPrefix)
	if role == "" {
		return "", fmt.Errorf("%w: empty role in target %q", ErrInvalidProfilePatch, target)
	}
	return role, nil
}
