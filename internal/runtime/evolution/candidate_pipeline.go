package evolution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/deployment"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// ErrCandidateNotVerified is returned when releasing a candidate that has not
// passed verification.
var ErrCandidateNotVerified = errors.New("evolution: candidate is not verified")

// ErrCandidateNotFound is returned when the candidate ID is unknown.
var ErrCandidateNotFound = errors.New("evolution: candidate not found")

// ReleasePriority is the coordinator priority for verified candidate patches.
// It must be >= coordinator.DefaultPolicy().AutoApplyThreshold (8) so the
// non-GA priority rule decides Apply.
const ReleasePriority = 8

// CandidatePipeline wires the verified-candidate release into the existing
// coordinator decision + deployment canary pipeline (Ch.8 release flow):
//
//	Candidate(verified) → RuntimePatch → coordinator.Submit → Evaluate
//	  → DecisionApply → deployment.Deploy (staging → live)
//	  → ProfileStore.SetStable + audit history
//
// Only DecisionApply releases; DecisionReject/DecisionDrop keep the candidate
// rejected. The coordinator's decision policy and the deployment thresholds
// are outside candidate control (Ch.8 trust root isolation).
type CandidatePipeline struct {
	store        *CandidateStore
	profileStore *ProfileStore
	registry     *patch.Registry
	coordinator  *coordinator.EvolutionCoordinator
	deployer     *deployment.DeploymentPipeline
	executor     *ProfileExecutor
	priority     int

	// regressionCheck is the release-time gate-3 preserved-case regression
	// check. When set, a verified candidate that regresses the preserved suite
	// is rejected instead of promoted (final release gate). When nil the gate
	// is skipped (backward compatible).
	regressionCheck func(c *Candidate) error
}

// CandidatePipelineOption configures a CandidatePipeline.
type CandidatePipelineOption func(*CandidatePipeline)

// WithReleaseRegressionCheck injects the release-time gate-3 preserved-case
// regression check into CandidatePipeline.Release: a verified candidate that
// regresses the preserved suite is rejected before promotion. This forms the
// full release gate (verify → release → regression confirm → promote).
func WithReleaseRegressionCheck(check func(c *Candidate) error) CandidatePipelineOption {
	return func(p *CandidatePipeline) {
		p.regressionCheck = check
	}
}

// NewCandidatePipeline wires the release path.
//
// Args:
//
//	store - candidate store (must be non-nil).
//	profileStore - profile store with candidate/stable regions (must be non-nil).
//	registry - patch registry where the profile executor is registered (must be non-nil).
//	coord - evolution coordinator making apply/reject decisions (must be non-nil).
//	dep - optional canary deployment pipeline; may be nil to apply directly.
//
// Returns:
//
//	pipeline - the ready-to-use release pipeline.
func NewCandidatePipeline(
	store *CandidateStore,
	profileStore *ProfileStore,
	registry *patch.Registry,
	coord *coordinator.EvolutionCoordinator,
	dep *deployment.DeploymentPipeline,
) *CandidatePipeline {
	p := &CandidatePipeline{
		store:        store,
		profileStore: profileStore,
		registry:     registry,
		coordinator:  coord,
		deployer:     dep,
		executor:     NewProfileExecutor(profileStore),
		priority:     ReleasePriority,
	}
	if coord != nil {
		// The DeploymentPipeline returns a record; the coordinator's
		// PatchDeployer only returns an error, so adapt the signatures.
		coord.SetDeployer(newDeploymentAdapter(dep))
	}
	return p
}

// NewCandidatePipelineWithOptions wires the release path with functional
// options, e.g. WithReleaseRegressionCheck for the release-time gate-3 gate.
// Args are the same as NewCandidatePipeline.
func NewCandidatePipelineWithOptions(
	store *CandidateStore,
	profileStore *ProfileStore,
	registry *patch.Registry,
	coord *coordinator.EvolutionCoordinator,
	dep *deployment.DeploymentPipeline,
	opts ...CandidatePipelineOption,
) *CandidatePipeline {
	p := NewCandidatePipeline(store, profileStore, registry, coord, dep)
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// deploymentAdapter adapts *deployment.DeploymentPipeline to the
// coordinator.PatchDeployer interface, discarding the deployment record on
// success (a normal reject/rollback is not an error per PatchDeployer).
type deploymentAdapter struct {
	pipeline *deployment.DeploymentPipeline
}

// newDeploymentAdapter wraps a deployment pipeline into a PatchDeployer.
// A nil pipeline produces a disabled adapter.
func newDeploymentAdapter(pipeline *deployment.DeploymentPipeline) *deploymentAdapter {
	return &deploymentAdapter{pipeline: pipeline}
}

// Enabled reports whether auto-promotion is active.
func (a *deploymentAdapter) Enabled() bool {
	if a.pipeline == nil {
		return false
	}
	return a.pipeline.IsEnabled()
}

// Deploy promotes the patch through the canary pipeline.
func (a *deploymentAdapter) Deploy(ctx context.Context, p patch.RuntimePatch) error {
	if a.pipeline == nil {
		return errors.New("evolution: deployment adapter has nil pipeline")
	}
	_, err := a.pipeline.Deploy(ctx, p)
	return err
}

// Release submits a verified candidate through the coordinator and, on a
// DecisionApply, promotes it via the deployment pipeline into the stable
// profile region. The release outcome is written to the coordinator's
// decision/patch history (audit trail) and the candidate's own status.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	candidateID - the verified candidate to release.
//
// Returns:
//
//	released - true when the patch was applied and promoted to stable.
//	err - ErrCandidateNotFound / ErrCandidateNotVerified, a patch-build error,
//	  or a deployment error.
func (p *CandidatePipeline) Release(ctx context.Context, candidateID string) (bool, error) {
	if p.store == nil || p.profileStore == nil || p.registry == nil || p.coordinator == nil {
		return false, errors.New("evolution: candidate pipeline is not fully wired")
	}

	c := p.store.Get(candidateID)
	if c == nil {
		return false, fmt.Errorf("%w: %s", ErrCandidateNotFound, candidateID)
	}
	if c.Status != StatusVerified {
		return false, fmt.Errorf("%w (status: %s)", ErrCandidateNotVerified, c.Status)
	}

	// Release-time gate-3: a verified candidate must not regress the preserved
	// suite before ANY patch is built/applied. When the check is wired and
	// fails, the candidate is rejected and no patch reaches the runtime or the
	// stable region (full release gate).
	if p.regressionCheck != nil {
		if regressErr := p.regressionCheck(c); regressErr != nil {
			c.Reject("release regression gate: " + regressErr.Error())
			return false, nil
		}
	}

	rp, err := p.buildRuntimePatch(c)
	if err != nil {
		return false, err
	}

	// Register the profile executor for this target so the coordinator can
	// apply (or the deployment pipeline can stage) the instruction change.
	// Replace, not Register (#25): the registry has no unregister path, so a
	// second verified candidate for the same TargetRole must be able to
	// supersede the first — Register would fail with "already registered".
	target := profileTargetPrefix + c.TargetRole
	if err := p.registry.Replace(target, p.executor); err != nil {
		return false, fmt.Errorf("evolution: register profile executor: %w", err)
	}

	// Submit and evaluate the decision.
	p.coordinator.Submit(coordinator.PatchProposal{
		Patch:     rp,
		Source:    coordinator.SourceCandidate,
		Reason:    c.Reason,
		Priority:  p.priority,
		Timestamp: time.Now(),
	})
	p.coordinator.Evaluate(ctx)

	// Find the decision for this patch.
	decision := p.lastDecision(rp.ID)
	switch decision.Decision {
	case coordinator.DecisionApply:
		released, applyErr := p.applyAndPromote(ctx, rp, c)
		return released, applyErr
	case coordinator.DecisionReject, coordinator.DecisionDrop:
		c.Reject(decision.Reason)
		return false, nil
	default: // DecisionDelay
		return false, nil
	}
}

// buildRuntimePatch converts a verified candidate into a patch that changes
// the target role's Instructions, carrying a rollback of the previous text.
func (p *CandidatePipeline) buildRuntimePatch(c *Candidate) (patch.RuntimePatch, error) {
	stable := p.profileStore.GetStable(c.TargetRole)
	var rollback *patch.RuntimePatch
	if stable != nil {
		rollback = &patch.RuntimePatch{
			Type:   patch.PatchChangeInstruction,
			Target: profileTargetPrefix + c.TargetRole,
			Value:  stable.Instructions,
			Reason: "rollback: restore stable instructions",
			Source: "rollback",
		}
	}
	return patch.RuntimePatch{
		ID:       "patch-" + c.ID,
		Type:     patch.PatchChangeInstruction,
		Target:   profileTargetPrefix + c.TargetRole,
		Value:    c.Diff,
		Reason:   c.Reason,
		Source:   string(coordinator.SourceCandidate),
		Rollback: rollback,
	}, nil
}

// applyAndPromote runs the deployment pipeline (or direct registry apply) and
// moves the released profile into the stable region on success.
func (p *CandidatePipeline) applyAndPromote(ctx context.Context, rp patch.RuntimePatch, c *Candidate) (bool, error) {
	if p.deployer != nil && p.deployer.IsEnabled() {
		record, err := p.deployer.Deploy(ctx, rp)
		if err != nil {
			c.Reject("deployment failed: " + err.Error())
			return false, err
		}
		if record.Status != deployment.DeploymentPromoted {
			c.Reject("deployment " + record.Status.String() + ": " + record.Reason)
			return false, nil
		}
	} else if err := p.registry.Apply(ctx, rp); err != nil {
		c.Reject("apply failed: " + err.Error())
		return false, err
	}

	// Promote the applied (candidate-region) profile to stable.
	applied := p.profileStore.Get(c.TargetRole)
	if applied == nil {
		c.Reject("applied profile missing after release")
		return false, errors.New("evolution: applied profile missing after release")
	}
	if err := p.profileStore.SetStable(c.TargetRole, applied); err != nil {
		c.Reject("promote to stable failed: " + err.Error())
		return false, err
	}

	c.Promote()
	return true, nil
}

// lastDecision returns the most recent coordinator decision for a patch ID.
func (p *CandidatePipeline) lastDecision(patchID string) coordinator.PatchDecision {
	history := p.coordinator.DecisionHistory()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Proposal.Patch.ID == patchID {
			return history[i]
		}
	}
	return coordinator.PatchDecision{Decision: coordinator.DecisionDelay}
}
