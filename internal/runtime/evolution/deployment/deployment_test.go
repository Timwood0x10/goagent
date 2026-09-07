package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// fakeStaging is a test double for StagingRuntime.
type fakeStaging struct {
	applyErr      error
	evaluateErr   error
	rollbackErr   error
	shadowScore   float64
	baselineScore float64
	applyCalls    int
	evalCalls     int
	rollbackCalls int
}

func (s *fakeStaging) Apply(_ context.Context, _ patch.RuntimePatch) (*patch.RuntimePatch, error) {
	s.applyCalls++
	if s.applyErr != nil {
		return nil, s.applyErr
	}
	return &patch.RuntimePatch{Type: patch.PatchChangePlanner, Target: "rollback"}, nil
}

func (s *fakeStaging) Evaluate(_ context.Context) (float64, float64, error) {
	s.evalCalls++
	if s.evaluateErr != nil {
		return 0, 0, s.evaluateErr
	}
	return s.shadowScore, s.baselineScore, nil
}

func (s *fakeStaging) Rollback(_ context.Context, _ *patch.RuntimePatch) error {
	s.rollbackCalls++
	return s.rollbackErr
}

// fakeLive is a test double for LiveRuntime.
type fakeLive struct {
	applyErr      error
	applyCalls    int
	rollbackErr   error
	rollbackCalls int
}

func (l *fakeLive) Apply(_ context.Context, _ patch.RuntimePatch) (*patch.RuntimePatch, error) {
	l.applyCalls++
	if l.applyErr != nil {
		return nil, l.applyErr
	}
	return &patch.RuntimePatch{Type: patch.PatchChangePlanner, Target: "rollback"}, nil
}

func (l *fakeLive) Rollback(_ context.Context, _ *patch.RuntimePatch) error {
	l.rollbackCalls++
	return l.rollbackErr
}

// TestDeploy_DisabledReturnsDisabled verifies that when Enabled=false,
// the pipeline records DeploymentDisabled and does not touch staging/live.
func TestDeploy_DisabledReturnsDisabled(t *testing.T) {
	dp := NewDeploymentPipeline(DeploymentConfig{Enabled: false}, nil, nil)
	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	assert.Equal(t, DeploymentDisabled, rec.Status)
}

// TestDeploy_PromotionPasses verifies the happy path: staging apply →
// shadow eval delta ≥ threshold → live apply.
func TestDeploy_PromotionPasses(t *testing.T) {
	staging := &fakeStaging{shadowScore: 0.80, baselineScore: 0.60}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.15,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	assert.Equal(t, DeploymentPromoted, rec.Status)
	assert.Equal(t, 0.80, rec.ShadowScore)
	assert.Equal(t, 0.60, rec.BaselineScore)
	assert.Equal(t, 1, staging.applyCalls)
	assert.Equal(t, 1, staging.evalCalls)
	assert.Equal(t, 1, live.applyCalls)
}

// TestDeploy_BelowThresholdRollsBackStaging verifies that a shadow score
// delta below PromotionThreshold rejects the patch and rolls back staging.
func TestDeploy_BelowThresholdRollsBackStaging(t *testing.T) {
	// delta = 0.62 - 0.60 = 0.02 < 0.05 threshold → reject
	staging := &fakeStaging{shadowScore: 0.62, baselineScore: 0.60}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.05,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	assert.Equal(t, DeploymentRejected, rec.Status)
	assert.Equal(t, 1, staging.rollbackCalls, "staging should be rolled back on rejection")
	assert.Equal(t, 0, live.applyCalls, "live should not be touched on rejection")
}

// TestDeploy_HighAbsoluteScoreButBaselineHigherRejects verifies the
// regression-direction fix: a high shadow absolute score (0.9) must be
// REJECTED when the baseline is even higher (0.95), because the delta is
// negative. Before the fix, this case would promote (0.9 ≥ 0.05).
func TestDeploy_HighAbsoluteScoreButBaselineHigherRejects(t *testing.T) {
	staging := &fakeStaging{shadowScore: 0.90, baselineScore: 0.95}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.05,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	assert.Equal(t, DeploymentRejected, rec.Status)
	assert.Equal(t, 0, live.applyCalls, "live must not be touched when baseline is higher")
}

// TestDeploy_StagingApplyFails verifies that a staging apply error rejects
// the patch without touching live.
func TestDeploy_StagingApplyFails(t *testing.T) {
	staging := &fakeStaging{applyErr: errors.New("staging boom")}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{Enabled: true, EvaluationTimeout: 0}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.Error(t, err)
	assert.Equal(t, DeploymentRejected, rec.Status)
	assert.Equal(t, 0, live.applyCalls)
}

// TestDeploy_ShadowEvalFails verifies that a shadow eval error rolls back
// staging and returns an error.
func TestDeploy_ShadowEvalFails(t *testing.T) {
	staging := &fakeStaging{evaluateErr: errors.New("eval boom")}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{Enabled: true, EvaluationTimeout: 0}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.Error(t, err)
	assert.Equal(t, DeploymentRejected, rec.Status)
	assert.Equal(t, 1, staging.rollbackCalls, "staging should be rolled back on eval failure")
}

// TestDeploy_LiveApplyFails verifies that a live apply error rolls back
// staging and marks the deployment as rolled back.
func TestDeploy_LiveApplyFails(t *testing.T) {
	staging := &fakeStaging{shadowScore: 0.80, baselineScore: 0.60}
	live := &fakeLive{applyErr: errors.New("live boom")}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.15,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.Error(t, err)
	assert.Equal(t, DeploymentRolledBack, rec.Status)
	assert.Equal(t, 1, staging.rollbackCalls, "staging should be rolled back on live failure")
}

// TestHistory_RecordsAllDeployments verifies that History returns all
// deployment records in order.
func TestHistory_RecordsAllDeployments(t *testing.T) {
	dp := NewDeploymentPipeline(DeploymentConfig{Enabled: false}, nil, nil)
	for i := 0; i < 3; i++ {
		_, _ = dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	}
	history := dp.History()
	assert.Len(t, history, 3)
	for _, rec := range history {
		assert.Equal(t, DeploymentDisabled, rec.Status)
	}
}

// TestMonitorAndRollback_RegressionExceedsThreshold verifies that after
// promotion, a regression exceeding RollbackThreshold triggers a live
// rollback and the record status changes to DeploymentRolledBack.
func TestMonitorAndRollback_RegressionExceedsThreshold(t *testing.T) {
	staging := &fakeStaging{shadowScore: 0.80, baselineScore: 0.60}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.15,
		RollbackThreshold:  0.10,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	require.Equal(t, DeploymentPromoted, rec.Status)

	// Simulate regression: live drops to 0.40, baseline stays 0.80.
	// regression = 0.80 - 0.40 = 0.40 > 0.10 threshold.
	staging.shadowScore = 0.40
	staging.baselineScore = 0.80

	updated, err := dp.MonitorAndRollback(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, DeploymentRolledBack, updated.Status)
	assert.Equal(t, 1, live.rollbackCalls, "live rollback should be called")
	assert.Nil(t, updated.RollbackPatch, "rollback handle should be cleared")
}

// TestMonitorAndRollback_NoRegressionStaysPromoted verifies that a small
// regression within the threshold does not trigger rollback.
func TestMonitorAndRollback_NoRegressionStaysPromoted(t *testing.T) {
	staging := &fakeStaging{shadowScore: 0.80, baselineScore: 0.60}
	live := &fakeLive{}
	dp := NewDeploymentPipeline(DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.15,
		RollbackThreshold:  0.10,
		EvaluationTimeout:  0,
	}, staging, live)

	rec, err := dp.Deploy(context.Background(), patch.RuntimePatch{Target: "memory"})
	require.NoError(t, err)
	require.Equal(t, DeploymentPromoted, rec.Status)

	// Small regression: live drops by 0.05, within 0.10 threshold.
	staging.shadowScore = 0.55
	staging.baselineScore = 0.60

	updated, err := dp.MonitorAndRollback(context.Background(), rec)
	require.NoError(t, err)
	assert.Equal(t, DeploymentPromoted, updated.Status)
	assert.Equal(t, 0, live.rollbackCalls, "live rollback should NOT be called")
}
