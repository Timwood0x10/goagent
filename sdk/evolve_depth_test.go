package sdk

import (
	"context"
	"testing"

	tools "github.com/Timwood0x10/ares/internal/apitools"
)

// TestApplySearchDepth covers the boundary shapes of the search_depth →
// maxIter mapping.
//
// Bug scenarios:
//  1. A missing or malformed depth silently zeroing the agent's iteration
//     budget (the executor would stop after one round).
//  2. A non-positive depth overriding a valid budget with an unusable one.
//  3. An evolved range that only shrinks the budget: candidates below
//     defaultMaxIterations (10) would permanently reduce every evolved
//     agent's budget — the range must include the default and above.
func TestApplySearchDepth(t *testing.T) {
	cases := []struct {
		name        string
		current     int
		params      map[string]any
		wantMaxIter int
	}{
		{name: "absent param preserves current", current: 7, params: map[string]any{}, wantMaxIter: 7},
		{name: "nil params preserve current", current: 7, params: nil, wantMaxIter: 7},
		{
			name:        "string type is rejected",
			current:     7,
			params:      map[string]any{paramSearchDepth: "3"},
			wantMaxIter: 7,
		},
		{
			name:        "float type is rejected",
			current:     7,
			params:      map[string]any{paramSearchDepth: 3.0},
			wantMaxIter: 7,
		},
		{
			name:        "zero depth is rejected",
			current:     7,
			params:      map[string]any{paramSearchDepth: 0},
			wantMaxIter: 7,
		},
		{
			name:        "negative depth is rejected",
			current:     7,
			params:      map[string]any{paramSearchDepth: -2},
			wantMaxIter: 7,
		},
		{
			name:        "valid depth overrides current",
			current:     7,
			params:      map[string]any{paramSearchDepth: 5},
			wantMaxIter: 5,
		},
		{
			name:        "depth equal to default budget applies",
			current:     4,
			params:      map[string]any{paramSearchDepth: 10},
			wantMaxIter: 10,
		},
		{
			name:        "depth above default budget applies",
			current:     10,
			params:      map[string]any{paramSearchDepth: 15},
			wantMaxIter: 15,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applySearchDepth(tc.current, tc.params)
			if got != tc.wantMaxIter {
				t.Fatalf("applySearchDepth(%d, %v) = %d, want %d", tc.current, tc.params, got, tc.wantMaxIter)
			}
		})
	}
}

// TestEvolvableParamsIncludeDefaultBudget is the regression for the evolved
// range fix: the candidate depths must span the default agent budget so
// evolution can PRESERVE or GROW it, not only shrink it.
//
// Bug scenario: with candidates {1..5} against a default budget of 10, every
// evolved agent ended up strictly worse than the stock configuration.
func TestEvolvableParamsIncludeDefaultBudget(t *testing.T) {
	params := evolvableParams()

	depthRange, ok := params[paramSearchDepth]
	if !ok {
		t.Fatalf("evolvableParams must carry %q", paramSearchDepth)
	}
	seen := make(map[int]bool, len(depthRange.Values))
	for _, v := range depthRange.Values {
		depth, isInt := v.(int)
		if !isInt {
			t.Fatalf("search_depth candidate %v is not an int", v)
		}
		if depth < 1 {
			t.Fatalf("search_depth candidate %d is below the usable minimum of 1", depth)
		}
		seen[depth] = true
	}
	if !seen[defaultMaxIterations] {
		t.Fatalf("search_depth candidates %v must include the default budget %d", depthRange.Values, defaultMaxIterations)
	}
	if !seen[15] {
		t.Fatalf("search_depth candidates %v must include a growth option above the default (15)", depthRange.Values)
	}

	if _, ok := params[paramToolSelector]; !ok {
		t.Fatalf("evolvableParams must carry %q", paramToolSelector)
	}
}

// extraEvoTools pads the agent's tool list past the priority-selector cap
// (3) so the truncation branch is exercised rather than the passthrough.
var extraEvoTools = []tools.Tool{
	tools.ToolFunc{ToolName: "evo-tool-a", ToolDesc: "test tool a", Fn: func(_ context.Context, _ map[string]any) (any, error) { return "a", nil }},
	tools.ToolFunc{ToolName: "evo-tool-b", ToolDesc: "test tool b", Fn: func(_ context.Context, _ map[string]any) (any, error) { return "b", nil }},
	tools.ToolFunc{ToolName: "evo-tool-c", ToolDesc: "test tool c", Fn: func(_ context.Context, _ map[string]any) (any, error) { return "c", nil }},
}

// TestApplyEvolvedParamsAppliesBothDimensions verifies the combined
// application path: tool_selector filters the tool list while search_depth
// rewrites maxIter, and a params map without search_depth leaves the
// agent's existing budget untouched.
func TestApplyEvolvedParamsAppliesBothDimensions(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	allTools := append([]tools.Tool{calcTool}, extraEvoTools...)
	agent := rt.NewAgent("evo-agent", WithTools(allTools...))
	agent.maxIter = 6

	applyEvolvedParams(agent, map[string]any{
		paramToolSelector: "priority",
		paramSearchDepth:  12,
	})
	if agent.maxIter != 12 {
		t.Fatalf("maxIter = %d, want 12 (search_depth applied)", agent.maxIter)
	}
	if len(agent.tools) != 3 {
		t.Fatalf("priority selector must truncate a %d-tool list to 3, got %d", len(allTools), len(agent.tools))
	}

	agent.maxIter = 6
	applyEvolvedParams(agent, map[string]any{paramToolSelector: "auto"})
	if agent.maxIter != 6 {
		t.Fatalf("maxIter = %d, want 6 (no search_depth in params preserves the budget)", agent.maxIter)
	}
}
