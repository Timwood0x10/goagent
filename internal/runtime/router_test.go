package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpressionRouter_Name(t *testing.T) {
	r := NewExpressionRouter("test-router", nil)
	assert.Equal(t, "test-router", r.Name())

	r2 := NewExpressionRouter("", nil)
	assert.Equal(t, "expression-router", r2.Name())
}

func TestExpressionRouter_Capabilities(t *testing.T) {
	r := NewExpressionRouter("test", nil)
	caps := r.Capabilities()
	assert.Contains(t, caps, CapRouter)
}

func TestExpressionRouter_NoRules_ReturnsNil(t *testing.T) {
	r := NewExpressionRouter("test", nil)
	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID: "s1",
	})
	// Per the RouterPlugin contract: no matching rule → (nil, nil), not an
	// error. A nil decision means "no routing is needed".
	require.NoError(t, err)
	assert.Nil(t, decision)
}

func TestExpressionRouter_MatchingRule(t *testing.T) {
	r := NewExpressionRouter("test", []RouteRule{
		{
			FromStepID: "s1",
			ToStepID:   "s2",
			Condition: func(output string, vars map[string]any) bool {
				return output == "error"
			},
			Reason: "error path",
		},
		{
			FromStepID: "s1",
			ToStepID:   "s3",
			Condition: func(output string, vars map[string]any) bool {
				return true
			},
			Reason: "default path",
		},
	})

	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID:     "s1",
		CurrentStepOutput: "error",
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "s2", decision.NextStepID)
	assert.Equal(t, "error path", decision.Reason)
	assert.Equal(t, "expression", decision.Source)
}

func TestExpressionRouter_FallthroughToDefault(t *testing.T) {
	r := NewExpressionRouter("test", []RouteRule{
		{
			FromStepID: "s1",
			ToStepID:   "s2",
			Condition: func(output string, vars map[string]any) bool {
				return output == "error"
			},
		},
		{
			FromStepID: "s1",
			ToStepID:   "s3",
			Condition: func(output string, vars map[string]any) bool {
				return true // catch-all
			},
		},
	})

	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID:     "s1",
		CurrentStepOutput: "success",
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "s3", decision.NextStepID)
}

func TestExpressionRouter_NoMatchingFromStep(t *testing.T) {
	r := NewExpressionRouter("test", []RouteRule{
		{
			FromStepID: "s1",
			ToStepID:   "s2",
			Condition: func(output string, vars map[string]any) bool {
				return true
			},
		},
	})

	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID: "s99",
	})
	// No rule matches step s99 → (nil, nil) per the documented contract.
	require.NoError(t, err)
	assert.Nil(t, decision)
}

func TestExpressionRouter_ConditionWithVars(t *testing.T) {
	r := NewExpressionRouter("test", []RouteRule{
		{
			ToStepID: "retry",
			Condition: func(output string, vars map[string]any) bool {
				v, ok := vars["attempts"]
				return ok && v.(int) < 3
			},
			Reason: "under max attempts",
		},
	})

	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID: "s1",
		Variables:     map[string]any{"attempts": 2},
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "retry", decision.NextStepID)
}

func TestExpressionRouter_AddRule(t *testing.T) {
	r := NewExpressionRouter("test", nil)
	assert.Len(t, r.Rules(), 0)

	r.AddRule(RouteRule{FromStepID: "s1", ToStepID: "s2"})
	assert.Len(t, r.Rules(), 1)
}

func TestExpressionRouter_PluginLifecycle(t *testing.T) {
	r := NewExpressionRouter("test", nil)
	assert.NoError(t, r.Start(context.Background(), nil))
	assert.NoError(t, r.Stop(context.Background()))
}

func TestExpressionRouter_EmptyFromStepMatchesAny(t *testing.T) {
	r := NewExpressionRouter("test", []RouteRule{
		{
			ToStepID: "s2",
			Condition: func(output string, vars map[string]any) bool {
				return output == "trigger"
			},
			Reason: "global rule",
		},
	})

	// Should match regardless of which step we're on
	decision, err := r.Route(context.Background(), RouteState{
		CurrentStepID:     "s99",
		CurrentStepOutput: "trigger",
	})
	require.NoError(t, err)
	require.NotNil(t, decision)
	assert.Equal(t, "s2", decision.NextStepID)
}

func TestValidateRouterFound(t *testing.T) {
	err := ValidateRouterFound(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "router plugin not found")

	r := NewExpressionRouter("test", nil)
	err = ValidateRouterFound(r)
	require.NoError(t, err)
}
