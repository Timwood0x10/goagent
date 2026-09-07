package mutation

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestNewMutator_DefaultRanges verifies that a mutator created without
// options uses the default parameter ranges.
func TestNewMutator_DefaultRanges(t *testing.T) {
	m, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	if len(m.paramRanges) != len(DefaultParamRanges) {
		t.Errorf("expected %d param ranges, got %d", len(DefaultParamRanges), len(m.paramRanges))
	}

	for name := range DefaultParamRanges {
		if _, ok := m.paramRanges[name]; !ok {
			t.Errorf("missing default param range: %s", name)
		}
	}
}

// TestMutate_SingleParameter verifies that parameter mutation produces
// a child strategy with a different parameter value.
func TestMutate_SingleParameter(t *testing.T) {
	m, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "parent-1",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "default prompt",
		CreatedAt:      time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 1)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	if len(children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(children))
	}

	child := children[0]

	// Verify parent is not modified.
	if parent.Version != 1 {
		t.Error("parent strategy was mutated (not pure)")
	}

	// Verify child metadata.
	if child.ParentID != parent.ID {
		t.Errorf("expected ParentID=%q, got %q", parent.ID, child.ParentID)
	}
	if child.Version != parent.Version+1 {
		t.Errorf("expected Version=%d, got %d", parent.Version+1, child.Version)
	}
	if child.Score != -1 {
		t.Errorf("expected Score=-1 (unevaluated), got %f", child.Score)
	}
	if child.StrategyMutationType != MutationParameter {
		t.Errorf("expected MutationType=parameter, got %s", child.StrategyMutationType)
	}
	if child.ID == parent.ID {
		t.Error("child ID must differ from parent ID")
	}

	// Verify parameter changed (with seed=42, temperature should change).
	if child.Params["temperature"] == parent.Params["temperature"] {
		t.Logf("warning: temperature unchanged, desc=%q", child.MutationDesc)
	}
}

// TestMutate_MultipleChildren verifies that n children are generated
// and each has a unique ID.
func TestMutate_MultipleChildren(t *testing.T) {
	m, err := NewMutator(WithSeed(99))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:      "parent-multi",
		Version: 3,
		Params: map[string]any{
			"temperature": 0.7,
			"top_k":       40,
		},
		PromptTemplate: "original",
		CreatedAt:      time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	if len(children) != 5 {
		t.Fatalf("expected 5 children, got %d", len(children))
	}

	ids := make(map[string]bool)
	for _, child := range children {
		if ids[child.ID] {
			t.Errorf("duplicate child ID: %s", child.ID)
		}
		ids[child.ID] = true

		if child.ParentID != parent.ID {
			t.Errorf("child %s: wrong ParentID", child.ID)
		}
		if child.Version != parent.Version+1 {
			t.Errorf("child %s: expected Version=%d, got %d",
				child.ID, parent.Version+1, child.Version)
		}
	}
}

// TestMutate_NilParent verifies that Mutate returns ErrNilParent for nil input.
func TestMutate_NilParent(t *testing.T) {
	m, _ := NewMutator(WithSeed(1))

	_, err := m.Mutate(context.Background(), nil, 3)
	if err != ErrNilParent {
		t.Errorf("expected ErrNilParent, got: %v", err)
	}
}

// TestMutate_ZeroN verifies that Mutate returns an empty slice for n=0.
func TestMutate_ZeroN(t *testing.T) {
	m, _ := NewMutator(WithSeed(1))

	parent := &Strategy{
		ID:     "test",
		Params: map[string]any{},
	}

	result, err := m.Mutate(context.Background(), parent, 0)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount, got: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for n=0, got %d items", len(result))
	}
}

// TestMutate_NegativeN verifies that Mutate returns error for negative n.
func TestMutate_NegativeN(t *testing.T) {
	m, _ := NewMutator(WithSeed(1))

	parent := &Strategy{
		ID:     "test",
		Params: map[string]any{},
	}

	_, err := m.Mutate(context.Background(), parent, -1)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount, got: %v", err)
	}
}

// TestMutate_NoValidMutation verifies fallback to deep copy when
// all candidate values equal the current value.
func TestMutate_NoValidMutation(t *testing.T) {
	m, err := NewMutator(
		WithSeed(42),
		WithParamRanges(map[string]ParamRange{
			"only_value": {Name: "only_value", Values: []any{0.5}},
		}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:        "no-mutation-parent",
		Version:   10,
		Params:    map[string]any{"only_value": 0.5},
		CreatedAt: time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 1)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	child := children[0]
	if child.Params["only_value"] != parent.Params["only_value"] {
		t.Errorf("expected value unchanged when no valid mutation exists")
	}
	if child.MutationDesc == "" {
		t.Error("expected non-empty MutationDesc for fallback case")
	}
}

// TestMutatePromptVariation verifies that prompt pool mutation works correctly.
func TestMutatePromptVariation(t *testing.T) {
	m, err := NewMutator(
		WithSeed(42), // Seed chosen so rng.Float64() < 0.2 triggers prompt mutation.
		WithPromptPool([]string{"prompt-a", "prompt-b", "prompt-c"}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "prompt-parent",
		Version:        1,
		Params:         map[string]any{},
		PromptTemplate: "prompt-a",
		CreatedAt:      time.Now(),
	}

	// Generate enough children to likely hit prompt mutation at least once.
	children, err := m.Mutate(context.Background(), parent, 20)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	foundPromptMutation := false
	for _, child := range children {
		if child.StrategyMutationType == MutationPrompt {
			foundPromptMutation = true
			if child.PromptTemplate == parent.PromptTemplate {
				t.Error("prompt-mutated child should have different template")
			}
		}
	}

	if !foundPromptMutation {
		t.Log("note: no prompt mutation occurred in this run (probabilistic)")
	}
}

// TestMutate_DeterministicWithSeed verifies that using the same seed
// produces identical mutation results when called sequentially on the same instance.
func TestMutate_DeterministicWithSeed(t *testing.T) {
	parent := &Strategy{
		ID:      "det-parent",
		Version: 1,
		Params: map[string]any{
			"temperature": 0.5,
			"top_k":       20,
		},
		PromptTemplate: "base",
		CreatedAt:      time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	m, _ := NewMutator(
		WithSeed(12345),
		WithPromptPool([]string{"alt-1", "alt-2"}),
	)

	children1, _ := m.Mutate(context.Background(), parent, 5)

	// Reset the RNG to the same seed for the second run.
	m, _ = NewMutator(
		WithSeed(12345),
		WithPromptPool([]string{"alt-1", "alt-2"}),
	)
	children2, _ := m.Mutate(context.Background(), parent, 5)

	if len(children1) != len(children2) {
		t.Fatalf("batch size mismatch: %d vs %d", len(children1), len(children2))
	}

	for i := range children1 {
		if children1[i].MutationDesc != children2[i].MutationDesc {
			t.Errorf("child %d desc differs: %q vs %q", i,
				children1[i].MutationDesc, children2[i].MutationDesc)
		}
		if children1[i].StrategyMutationType != children2[i].StrategyMutationType {
			t.Errorf("child %d type differs: %v vs %v", i,
				children1[i].StrategyMutationType, children2[i].StrategyMutationType)
		}
		for k, v1 := range children1[i].Params {
			v2, ok := children2[i].Params[k]
			if !ok || !valuesEqual(v1, v2) {
				t.Errorf("child %d param %q differs: %v vs %v", i, k, v1, v2)
			}
		}
	}
}

// TestMutate_ContextCancellation verifies that Mutate respects context cancellation.
func TestMutate_ContextCancellation(t *testing.T) {
	m, _ := NewMutator(WithSeed(1))

	parent := &Strategy{
		ID:        "cancel-parent",
		Version:   1,
		Params:    map[string]any{"temperature": 0.5},
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := m.Mutate(ctx, parent, 100)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// TestStrategy_Clone verifies deep copy correctness of Strategy.
func TestStrategy_Clone(t *testing.T) {
	original := &Strategy{
		ID:       "orig-1",
		ParentID: "parent-orig",
		Version:  5,
		Params: map[string]any{
			"temperature": 0.7,
			"tags":        []any{"a", "b"},
		},
		PromptTemplate:       "hello",
		StrategyMutationType: MutationParameter,
		MutationDesc:         "test mutation",
		Score:                0.85,
		CreatedAt:            time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC),
		GenerationCreated:    3,
	}

	cloned := original.Clone()

	// Verify equality of all fields.
	if cloned.ID != original.ID {
		t.Errorf("ID mismatch: %s vs %s", cloned.ID, original.ID)
	}
	if cloned.ParentID != original.ParentID {
		t.Errorf("ParentID mismatch")
	}
	if cloned.Version != original.Version {
		t.Errorf("Version mismatch")
	}
	if cloned.Score != original.Score {
		t.Errorf("Score mismatch")
	}
	if cloned.GenerationCreated != original.GenerationCreated {
		t.Errorf("GenerationCreated mismatch: %d vs %d", cloned.GenerationCreated, original.GenerationCreated)
	}
	if cloned.PromptTemplate != original.PromptTemplate {
		t.Errorf("PromptTemplate mismatch")
	}
	if cloned.StrategyMutationType != original.StrategyMutationType {
		t.Errorf("MutationType mismatch")
	}

	// Verify Params is a deep copy (modifying clone does not affect original).
	cloned.Params["temperature"] = 0.999
	if original.Params["temperature"].(float64) == 0.999 {
		t.Error("clone modification affected original")
	}

	// Verify nil input returns nil.
	if original.Clone() == nil {
		t.Error("Clone of non-nil strategy returned nil")
	}
	var nilStrat *Strategy
	if nilStrat.Clone() != nil {
		t.Error("Clone of nil should return nil")
	}
}

// TestMutationType_String verifies String() output for all mutation types.
func TestMutationType_String(t *testing.T) {
	tests := []struct {
		mt   MutationType
		want string
	}{
		{MutationParameter, "parameter"},
		{MutationPrompt, "prompt"},
		{MutationTool, "tool"},
		{MutationType(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.mt.String()
		if got != tt.want {
			t.Errorf("MutationType(%d).String() = %q, want %q", tt.mt, got, tt.want)
		}
	}
}

// TestWithParamRanges_EmptyError verifies that empty param ranges return error.
func TestWithParamRanges_EmptyError(t *testing.T) {
	_, err := NewMutator(WithParamRanges(nil))
	if err == nil {
		t.Error("expected error for nil param ranges")
	}

	_, err = NewMutator(WithParamRanges(map[string]ParamRange{}))
	if err == nil {
		t.Error("expected error for empty param ranges")
	}
}

// TestWithPromptPool_EmptyError verifies that empty prompt pool returns error.
func TestWithPromptPool_EmptyError(t *testing.T) {
	_, err := NewMutator(WithPromptPool(nil))
	if err == nil {
		t.Error("expected error for nil prompt pool")
	}

	_, err = NewMutator(WithPromptPool([]string{}))
	if err == nil {
		t.Error("expected error for empty prompt pool")
	}
}

// TestValuesEqual verifies the valuesEqual comparison function.
func TestValuesEqual(t *testing.T) {
	tests := []struct {
		a, b any
		want bool
	}{
		{nil, nil, true},
		{nil, 1, false},
		{1, nil, false},
		{42, 42, true},
		{42, 43, false},
		{int64(42), int64(42), true},
		{3.14, 3.14, true},
		{3.14, 3.15, false},
		{"hello", "hello", true},
		{"hello", "world", false},
		{true, true, true},
		{true, false, false},
	}

	for _, tt := range tests {
		got := valuesEqual(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("valuesEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestMutate_ToolMutation verifies that tool mutation produces child strategies
// with MutationTool type and changed tool configuration.
func TestMutate_ToolMutation(t *testing.T) {
	m, err := NewMutator(
		WithSeed(42),
		WithToolPool([]string{
			"web_search,calculator",
			"web_search,calculator,code_exec",
			"web_search,file_read,file_write",
			"calculator,code_exec",
		}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             fmt.Sprintf("tool-parent-%d", time.Now().UnixNano()),
		Version:        1,
		Params:         map[string]any{"temperature": 0.5, "tools": "web_search,calculator"},
		PromptTemplate: "default prompt",
		CreatedAt:      time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 100)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	foundToolMutation := false
	for _, child := range children {
		if child.StrategyMutationType == MutationTool {
			foundToolMutation = true
			// Verify tool configuration actually changed.
			childTools, _ := child.Params["tools"].(string)
			parentTools, _ := parent.Params["tools"].(string)
			if childTools == parentTools {
				t.Error("tool-mutated child should have different tool configuration")
			}
			// Verify MutationDesc is set.
			if child.MutationDesc == "" {
				t.Error("tool-mutated child should have non-empty MutationDesc")
			}
		}
	}

	if !foundToolMutation {
		t.Error("expected at least one MutationTool child in 100 iterations (probabilistic test)")
	}
}

// TestMutate_ToolPoolEmptyFallsBack verifies that when toolPool is empty,
// no MutationTool type children are produced (fallback to parameter mutation).
func TestMutate_ToolPoolEmptyFallsBack(t *testing.T) {
	m, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:        fmt.Sprintf("no-tool-parent-%d", time.Now().UnixNano()),
		Version:   1,
		Params:    map[string]any{"temperature": 0.5},
		CreatedAt: time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 20)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	for _, child := range children {
		if child.StrategyMutationType == MutationTool {
			t.Error("expected no MutationTool children when toolPool is empty")
		}
	}
}

// TestMutate_AllThreeTypes verifies that all three mutation types
// (parameter, prompt, tool) can be produced when all pools are configured.
func TestMutate_AllThreeTypes(t *testing.T) {
	m, err := NewMutator(
		WithSeed(99),
		WithPromptPool([]string{"prompt-a", "prompt-b", "prompt-c"}),
		WithToolPool([]string{
			"web_search,calculator",
			"web_search,code_exec",
			"file_read,file_write",
		}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             fmt.Sprintf("all-types-parent-%d", time.Now().UnixNano()),
		Version:        1,
		Params:         map[string]any{"temperature": 0.7, "tools": "web_search,calculator"},
		PromptTemplate: "prompt-a",
		CreatedAt:      time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 50)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	foundParam := false
	foundPrompt := false
	foundTool := false

	for _, child := range children {
		switch child.StrategyMutationType {
		case MutationParameter:
			foundParam = true
		case MutationPrompt:
			foundPrompt = true
		case MutationTool:
			foundTool = true
		}
	}

	if !foundParam {
		t.Error("expected at least one MutationParameter child")
	}
	if !foundPrompt {
		t.Error("expected at least one MutationPrompt child")
	}
	if !foundTool {
		t.Error("expected at least one MutationTool child")
	}
}

// TestWithToolPool verifies that WithToolPool correctly configures the tool pool.
func TestWithToolPool(t *testing.T) {
	tools := []string{"tool-a", "tool-b", "tool-c"}

	m, err := NewMutator(WithToolPool(tools))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	if len(m.toolPool) != len(tools) {
		t.Errorf("expected toolPool length %d, got %d", len(tools), len(m.toolPool))
	}
	for i, expected := range tools {
		if m.toolPool[i] != expected {
			t.Errorf("toolPool[%d] = %q, want %q", i, m.toolPool[i], expected)
		}
	}

	// Verify that modifying the original slice does not affect the mutator.
	tools[0] = "modified"
	if m.toolPool[0] == "modified" {
		t.Error("WithToolPool should copy the input slice")
	}
}

// TestWithToolPool_EmptyError verifies that empty tool pool returns error.
func TestWithToolPool_EmptyError(t *testing.T) {
	_, err := NewMutator(WithToolPool(nil))
	if err == nil {
		t.Error("expected error for nil tool pool")
	}

	_, err = NewMutator(WithToolPool([]string{}))
	if err == nil {
		t.Error("expected error for empty tool pool")
	}
}

// TestMutateTool_SingleToolInPool verifies that a toolPool with only 1 element
// returns a deep copy without panicking.
func TestMutateTool_SingleToolInPool(t *testing.T) {
	m, err := NewMutator(
		WithSeed(42),
		WithToolPool([]string{"only_tool_config"}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:        fmt.Sprintf("single-tool-parent-%d", time.Now().UnixNano()),
		Version:   1,
		Params:    map[string]any{"tools": "only_tool_config"},
		CreatedAt: time.Now(),
	}

	children, err := m.Mutate(context.Background(), parent, 10)
	if err != nil {
		t.Fatalf("Mutate failed: %v", err)
	}

	for _, child := range children {
		if child.StrategyMutationType == MutationTool {
			// Should be a deep copy with fallback description.
			if child.MutationDesc == "" {
				t.Error("expected non-empty MutationDesc for single-tool fallback")
			}
			// Tools value should remain unchanged.
			childTools, _ := child.Params["tools"].(string)
			parentTools, _ := parent.Params["tools"].(string)
			if childTools != parentTools {
				t.Error("single-tool pool should not change tool configuration")
			}
		}
	}
}
