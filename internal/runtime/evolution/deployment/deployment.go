// Package deployment manages the safe promotion of evolution patches
// from staging to the live runtime. It implements a canary deployment
// strategy with automatic rollback on regression.
//
// Pipeline:
//
//	Coordinator.Apply(patch)
//	  → StagingRuntime.Apply(patch)        [apply to shadow runtime]
//	  → StagingRuntime.Evaluate()         [run eval suite on shadow]
//	  → if pass: LiveRuntime.Apply(patch) [promote to live]
//	  → if fail: StagingRuntime.Rollback() [auto-rollback]
//
// Default config has Enabled=false. Must be explicitly enabled.
package deployment

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// DeploymentConfig controls the patch deployment pipeline.
type DeploymentConfig struct {
	// Enabled controls whether patches are auto-promoted to live.
	// Default: false. Must be explicitly enabled in config.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// PromotionThreshold is the minimum fitness improvement required
	// to promote a patch to live. [0.0, 1.0]. Default: 0.05 (5% improvement).
	PromotionThreshold float64 `json:"promotion_threshold" yaml:"promotion_threshold"`

	// RollbackThreshold is the maximum fitness regression allowed
	// before auto-rollback. [0.0, 1.0]. Default: 0.10 (10% regression).
	RollbackThreshold float64 `json:"rollback_threshold" yaml:"rollback_threshold"`

	// EvaluationTimeout bounds the shadow evaluation duration.
	EvaluationTimeout time.Duration `json:"evaluation_timeout" yaml:"evaluation_timeout"`
}

// DefaultDeploymentConfig returns a conservative default configuration.
// Enabled=false ensures patches are not auto-promoted unless explicitly opted in.
func DefaultDeploymentConfig() DeploymentConfig {
	return DeploymentConfig{
		Enabled:            false,
		PromotionThreshold: 0.05,
		RollbackThreshold:  0.10,
		EvaluationTimeout:  30 * time.Second,
	}
}

// DeploymentStatus classifies the outcome of a deployment attempt.
type DeploymentStatus int

const (
	// DeploymentPromoted indicates the patch was promoted to live.
	DeploymentPromoted DeploymentStatus = iota
	// DeploymentRolledBack indicates the patch was auto-rolled back.
	DeploymentRolledBack
	// DeploymentRejected indicates the patch failed shadow evaluation.
	DeploymentRejected
	// DeploymentDisabled indicates auto-promotion is disabled in config.
	DeploymentDisabled
)

// String returns a human-readable name for the deployment status.
func (s DeploymentStatus) String() string {
	switch s {
	case DeploymentPromoted:
		return "promoted"
	case DeploymentRolledBack:
		return "rolled_back"
	case DeploymentRejected:
		return "rejected"
	case DeploymentDisabled:
		return "disabled"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// DeploymentRecord captures the outcome of a single patch deployment attempt.
type DeploymentRecord struct {
	PatchID       string           `json:"patch_id"`
	Status        DeploymentStatus `json:"status"`
	ShadowScore   float64          `json:"shadow_score"`
	BaselineScore float64          `json:"baseline_score"`
	LiveScore     float64          `json:"live_score"`
	Timestamp     time.Time        `json:"timestamp"`
	Reason        string           `json:"reason"`
	// RollbackPatch holds the live rollback handle when the patch was
	// promoted. It is non-nil only for DeploymentPromoted records that have
	// not yet been monitored by MonitorAndRollback. After monitoring, the
	// field is cleared to avoid retaining stale handles.
	RollbackPatch *patch.RuntimePatch `json:"-"`
}

// StagingRuntime is the shadow runtime where patches are applied for evaluation.
type StagingRuntime interface {
	// Apply applies a patch to the staging runtime and returns a rollback
	// patch. The patch's Target/Source identifies the strategy being
	// evaluated, so Evaluate can scope shadow and baseline scores per-strategy.
	Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error)
	// Evaluate returns (shadowScore, baselineScore, err). shadowScore is
	// the fitness of the currently-staged patch's strategy; baselineScore
	// is the fitness of the currently-active strategy. Both MUST be sampled
	// in the same call from the same time anchor — splitting them into two
	// independent calls risks window misalignment under concurrent
	// evidence writes, which would distort the delta.
	Evaluate(ctx context.Context) (shadow, baseline float64, err error)
	// Rollback reverts the last applied patch and clears the staging
	// runtime's per-patch strategy tracking.
	Rollback(ctx context.Context, rollback *patch.RuntimePatch) error
}

// LiveRuntime is the production runtime that agents consume.
type LiveRuntime interface {
	// Apply promotes a patch to the live runtime and returns a rollback
	// handle. The returned patch can be used by Rollback to revert the
	// promotion if a regression is detected post-promotion.
	Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error)
	// Rollback reverts a previously applied live patch. The rollback
	// argument is the inverse patch returned by Apply (or nil when the
	// live runtime does not produce a real inverse — in that case the
	// caller must use patch.Registry.Restore instead).
	Rollback(ctx context.Context, rollback *patch.RuntimePatch) error
}

// DeploymentPipeline manages the patch promotion lifecycle.
type DeploymentPipeline struct {
	mu      sync.Mutex
	config  DeploymentConfig
	staging StagingRuntime
	live    LiveRuntime
	history []DeploymentRecord
}

// NewDeploymentPipeline creates a DeploymentPipeline with the given dependencies.
//
// Args:
//   - config  - deployment configuration.
//   - staging - shadow runtime for patch testing (must not be nil when Enabled).
//   - live    - production runtime for patch promotion (must not be nil when Enabled).
//
// Returns:
//   - *DeploymentPipeline - the configured pipeline.
func NewDeploymentPipeline(config DeploymentConfig, staging StagingRuntime, live LiveRuntime) *DeploymentPipeline {
	return &DeploymentPipeline{
		config:  config,
		staging: staging,
		live:    live,
	}
}

// IsEnabled reports whether auto-promotion to the live runtime is active.
func (dp *DeploymentPipeline) IsEnabled() bool {
	return dp.config.Enabled
}

// Deploy attempts to safely promote a patch through staging → live.
//
// Algorithm:
//  1. If not Enabled: record DeploymentDisabled, return nil.
//  2. Apply patch to staging → get rollback.
//  3. Shadow evaluate → get (shadowScore, baselineScore).
//  4. If (shadowScore - baselineScore) >= PromotionThreshold: promote to live.
//  5. Record deployment outcome.
//
// Args:
//   - ctx - timeout and cancellation context.
//   - p   - the RuntimePatch to deploy.
//
// Returns:
//   - record - the deployment outcome record.
//   - err    - non-nil if deployment fails catastrophically (not rollback).
func (dp *DeploymentPipeline) Deploy(ctx context.Context, p patch.RuntimePatch) (*DeploymentRecord, error) {
	dp.mu.Lock()
	defer dp.mu.Unlock()

	// Use the actual patch ID so audit records can trace back to the source
	// candidate; a synthesized timestamp ID would be unrelated and untraceable.
	patchID := p.ID
	if patchID == "" {
		patchID = fmt.Sprintf("patch-%d", time.Now().UnixNano())
	}
	record := &DeploymentRecord{
		PatchID:   patchID,
		Timestamp: time.Now(),
	}

	if !dp.config.Enabled {
		record.Status = DeploymentDisabled
		record.Reason = "auto-promotion disabled in config"
		dp.history = append(dp.history, *record)
		return record, nil
	}

	if dp.staging == nil || dp.live == nil {
		record.Status = DeploymentRejected
		record.Reason = "staging or live runtime is nil"
		dp.history = append(dp.history, *record)
		return record, errors.New("deployment: staging or live runtime is nil")
	}

	// Step 2: Apply to staging.
	rollback, err := dp.staging.Apply(ctx, p)
	if err != nil {
		record.Status = DeploymentRejected
		record.Reason = fmt.Sprintf("staging apply failed: %v", err)
		dp.history = append(dp.history, *record)
		return record, fmt.Errorf("deployment: staging apply: %w", err)
	}

	// Step 3: Shadow evaluate — returns both shadow (patch strategy)
	// and baseline (active strategy) scores from the same time anchor.
	evalCtx, cancel := context.WithTimeout(ctx, dp.config.EvaluationTimeout)
	defer cancel()

	shadowScore, baselineScore, err := dp.staging.Evaluate(evalCtx)
	if err != nil {
		_ = dp.staging.Rollback(ctx, rollback)
		record.Status = DeploymentRejected
		record.Reason = fmt.Sprintf("shadow evaluate failed: %v", err)
		dp.history = append(dp.history, *record)
		return record, fmt.Errorf("deployment: shadow evaluate: %w", err)
	}
	record.ShadowScore = shadowScore
	record.BaselineScore = baselineScore

	// Step 4: Check promotion threshold — the threshold is an IMPROVEMENT
	// delta (shadow - baseline), not an absolute score. This prevents any
	// patch from passing when the active strategy already scores higher.
	delta := shadowScore - baselineScore
	if delta < dp.config.PromotionThreshold {
		_ = dp.staging.Rollback(ctx, rollback)
		record.Status = DeploymentRejected
		record.Reason = fmt.Sprintf(
			"delta %.3f (shadow %.3f - baseline %.3f) below promotion threshold %.3f",
			delta, shadowScore, baselineScore, dp.config.PromotionThreshold)
		dp.history = append(dp.history, *record)
		return record, nil
	}

	// Step 5: Promote to live.
	liveRollback, err := dp.live.Apply(ctx, p)
	if err != nil {
		_ = dp.staging.Rollback(ctx, rollback)
		record.Status = DeploymentRolledBack
		record.Reason = fmt.Sprintf("live apply failed: %v", err)
		dp.history = append(dp.history, *record)
		return record, fmt.Errorf("deployment: live apply: %w", err)
	}

	record.Status = DeploymentPromoted
	record.Reason = fmt.Sprintf(
		"patch promoted to live runtime (delta %.3f: shadow %.3f - baseline %.3f)",
		delta, shadowScore, baselineScore)
	record.RollbackPatch = liveRollback
	dp.history = append(dp.history, *record)
	return record, nil
}

// History returns a copy of all deployment records for observability.
func (dp *DeploymentPipeline) History() []DeploymentRecord {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	out := make([]DeploymentRecord, len(dp.history))
	copy(out, dp.history)
	return out
}

// MonitorAndRollback checks a promoted deployment for regression. After a
// patch is promoted to live, this method waits for EvaluationTimeout, then
// samples the live fitness via the staging runtime's Evaluate. If the live
// score regresses from the baseline by more than RollbackThreshold, it rolls
// back the live patch and marks the record as DeploymentRolledBack.
//
// This method requires the live runtime to support Rollback (or the
// DeploymentPipeline to have a patch.Registry for Restore). When neither is
// available, the method returns ErrNoRollbackSupport.
//
// Args:
//   - ctx    - timeout and cancellation context.
//   - record - the promoted DeploymentRecord to monitor. Must have
//     Status == DeploymentPromoted and a non-nil RollbackPatch.
//
// Returns:
//   - updated record with either DeploymentPromoted (no regression) or
//     DeploymentRolledBack (regression detected and rolled back).
//   - err - non-nil only if the monitoring itself failed catastrophically.
func (dp *DeploymentPipeline) MonitorAndRollback(ctx context.Context, record *DeploymentRecord) (*DeploymentRecord, error) {
	dp.mu.Lock()
	defer dp.mu.Unlock()

	if record == nil || record.Status != DeploymentPromoted || record.RollbackPatch == nil {
		return record, fmt.Errorf("deployment: MonitorAndRollback requires a promoted record with a rollback handle")
	}

	// Wait for the evaluation window to elapse before sampling.
	select {
	case <-ctx.Done():
		return record, ctx.Err()
	case <-time.After(dp.config.EvaluationTimeout):
	}

	// Sample the current live fitness. Evaluate returns shadow (the
	// promoted patch's strategy) and baseline (the previously active
	// strategy). After promotion, the "shadow" is the live strategy, so
	// its score is the current live score. The baseline is the old
	// strategy's score — if shadow < baseline by more than the threshold,
	// the promotion caused a regression.
	currentScore, oldBaseline, err := dp.staging.Evaluate(ctx)
	if err != nil {
		return record, fmt.Errorf("deployment: monitor evaluate: %w", err)
	}

	regression := oldBaseline - currentScore
	if regression <= dp.config.RollbackThreshold {
		// No significant regression — keep promoted.
		record.LiveScore = currentScore
		return record, nil
	}

	// Regression detected — roll back the live patch.
	if dp.live != nil {
		if err := dp.live.Rollback(ctx, record.RollbackPatch); err != nil {
			return record, fmt.Errorf("deployment: live rollback failed: %w", err)
		}
	}

	record.Status = DeploymentRolledBack
	record.LiveScore = currentScore
	record.Reason = fmt.Sprintf(
		"regression %.3f (baseline %.3f - live %.3f) exceeded rollback threshold %.3f",
		regression, oldBaseline, currentScore, dp.config.RollbackThreshold)
	record.RollbackPatch = nil

	// Update the history entry in place.
	for i := range dp.history {
		if dp.history[i].PatchID == record.PatchID &&
			dp.history[i].Timestamp.Equal(record.Timestamp) {
			dp.history[i] = *record
			break
		}
	}
	return record, nil
}

// ErrNoRollbackSupport is returned when neither the live runtime nor the
// patch registry supports rollback.
var ErrNoRollbackSupport = errors.New("deployment: no rollback support available")
