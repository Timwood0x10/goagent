package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func buildTestRuntime(t *testing.T) *KnowledgeRuntime {
	t.Helper()
	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 10240}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 200}},
	)
	discovery := planner.NewSourceDiscovery(provider.NewProviderRegistry(), planner.NewQueryPlanner())
	return New(
		planner.NewKnowledgePlanner(),
		discovery,
		provider.NewProviderRegistry(),
		pipe,
		[]Linker{&DefaultLinker{}},
		[]Reducer{&DefaultReducer{}},
	)
}

func TestNewKnowledgePatchExecutor(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)
	require.NotNil(t, exec)
	assert.Same(t, rt, exec.runtime)
}

func TestKnowledgePatchExecutor_Apply_ChangeBudget(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	// Seed an initial budget so the rollback has a real old value to capture
	// (instead of the previous hardcoded 0).
	rt.SetPlanConfig(PlanConfig{MaxResults: 10, ReducerStrategy: "strict"})

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeBudget,
		Target: "knowledge.planner.max_results",
		Value:  42,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeBudget, rollback.Type)
	// Rollback must capture the previous budget, not a hardcoded 0.
	assert.Equal(t, 10, rollback.Value)

	// A budget change must not wipe the unrelated reducer strategy.
	cfg := rt.PlanConfig()
	assert.Equal(t, 42, cfg.MaxResults)
	assert.Equal(t, "strict", cfg.ReducerStrategy)

	// Applying the rollback restores the previous budget precisely.
	_, err = exec.Apply(context.Background(), *rollback)
	require.NoError(t, err)
	assert.Equal(t, 10, rt.PlanConfig().MaxResults)
}

func TestKnowledgePatchExecutor_Apply_ChangeBudget_InvalidValue(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeBudget,
		Value: "not-an-int",
	})
	assert.Error(t, err)
}

func TestKnowledgePatchExecutor_Apply_ChangePlanner(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	// Seed an initial strategy so the rollback has a real old value.
	rt.SetPlanConfig(PlanConfig{MaxResults: 5, ReducerStrategy: "strict"})

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Target: "knowledge.planner.strategy",
		Value:  "memory-first",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangePlanner, rollback.Type)
	// Rollback captures the previous strategy (not a hardcoded "default").
	assert.Equal(t, "strict", rollback.Value)

	cfg := rt.PlanConfig()
	assert.Equal(t, "memory-first", cfg.ReducerStrategy)
	// A planner change must not wipe the unrelated budget.
	assert.Equal(t, 5, cfg.MaxResults)
}

func TestKnowledgePatchExecutor_Apply_ChangeReducer(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	// Seed an initial strategy so the rollback has a real old value.
	rt.SetPlanConfig(PlanConfig{MaxResults: 7, ReducerStrategy: "relaxed"})

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeReducer,
		Target: "knowledge.planner.reducer",
		Value:  "strict",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeReducer, rollback.Type)
	// Rollback captures the previous strategy, not a hardcoded default.
	assert.Equal(t, "relaxed", rollback.Value)

	cfg := rt.PlanConfig()
	assert.Equal(t, "strict", cfg.ReducerStrategy)
	// A reducer change must not wipe the unrelated budget.
	assert.Equal(t, 7, cfg.MaxResults)
}

func TestKnowledgePatchExecutor_Apply_ChangeReducer_InvalidValue(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeReducer,
		Value: 42,
	})
	assert.Error(t, err)
}

func TestKnowledgePatchExecutor_Apply_UnsupportedType(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchType(999),
	})
	assert.Error(t, err)
}

func TestKnowledgePatchExecutor_CanApply(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	tests := []struct {
		name  string
		patch patch.RuntimePatch
		want  bool
	}{
		{"change budget valid", patch.RuntimePatch{Type: patch.PatchChangeBudget, Value: 50}, true},
		{"change budget invalid value", patch.RuntimePatch{Type: patch.PatchChangeBudget, Value: "bad"}, false},
		{"change planner valid", patch.RuntimePatch{Type: patch.PatchChangePlanner, Value: "balanced"}, true},
		{"change planner invalid", patch.RuntimePatch{Type: patch.PatchChangePlanner, Value: 42}, false},
		{"change reducer valid", patch.RuntimePatch{Type: patch.PatchChangeReducer, Value: "strict"}, true},
		{"change reducer invalid", patch.RuntimePatch{Type: patch.PatchChangeReducer, Value: 42}, false},
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

func TestKnowledgePatchExecutor_CanApply_NilRuntime(t *testing.T) {
	exec := &KnowledgePatchExecutor{runtime: nil}
	err := exec.CanApply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchChangeBudget, Value: 50,
	})
	assert.Error(t, err)
}

func TestKnowledgPatchExecutor_Apply_UsesSetPlanConfig(t *testing.T) {
	rt := buildTestRuntime(t)
	exec := NewKnowledgePatchExecutor(rt)

	// Apply a ChangeBudget patch — should update the runtime's planner config.
	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeBudget,
		Value: 77,
	})
	require.NoError(t, err)

	// Verify the runtime's planner was reconfigured.
	plan, err := rt.planner.Plan(context.Background(), "test query", knowledge.TokenBudget{ForGraph: 5000})
	require.NoError(t, err)
	if len(plan.Requirements) > 0 {
		assert.Equal(t, 77, plan.Requirements[0].MaxResults)
	}
}

func TestKnowledgeRuntime_WithPatchRegistry(t *testing.T) {
	rt := buildTestRuntime(t)
	patchReg := patch.NewRegistry()

	rt.WithPatchRegistry(patchReg)
	assert.NotNil(t, rt.patchReg)

	// Verify the KnowledgePatchExecutor can be registered and used through it.
	exec := NewKnowledgePatchExecutor(rt)
	require.NoError(t, patchReg.Register("knowledge.planner.max_results", exec))

	err := patchReg.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeBudget,
		Target: "knowledge.planner.max_results",
		Value:  99,
	})
	require.NoError(t, err)

	// Run a plan to confirm the new Config takes effect.
	plan, planErr := rt.planner.Plan(context.Background(), "test", knowledge.TokenBudget{ForGraph: 5000})
	require.NoError(t, planErr)
	if len(plan.Requirements) > 0 {
		assert.Equal(t, 99, plan.Requirements[0].MaxResults)
	}
}
