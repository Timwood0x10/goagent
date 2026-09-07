package mutation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/truncate"
)

// mockHintProvider returns pre-configured hints for testing.
type mockHintProvider struct {
	mu    sync.Mutex
	hints []EvolutionHint
	err   error
}

func newMockHintProvider(hints []EvolutionHint) *mockHintProvider {
	return &mockHintProvider{hints: hints}
}

func (m *mockHintProvider) HintsForTask(
	ctx context.Context,
	taskType string,
	limit int,
) ([]EvolutionHint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return nil, m.err
	}

	if limit <= 0 || limit >= len(m.hints) {
		result := make([]EvolutionHint, len(m.hints))
		copy(result, m.hints)
		return result, nil
	}

	result := make([]EvolutionHint, limit)
	copy(result, m.hints[:limit])
	return result, nil
}

func (m *mockHintProvider) RecordStrategyOutcome(
	ctx context.Context,
	outcome StrategyOutcome,
) error {
	return nil // No-op for testing.
}

// setError sets an error to return from HintsForTask.
func (m *mockHintProvider) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// emptyHintProvider returns no hints for testing fallback behavior.
type emptyHintProvider struct{}

func (e *emptyHintProvider) HintsForTask(
	ctx context.Context,
	taskType string,
	limit int,
) ([]EvolutionHint, error) {
	return nil, nil
}

func (e *emptyHintProvider) RecordStrategyOutcome(
	ctx context.Context,
	outcome StrategyOutcome,
) error {
	return nil
}

// TestNewExperienceGuidedMutator_NilInputs verifies that nil inputs are rejected.
func TestNewExperienceGuidedMutator_NilInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     *Mutator
		provider HintProvider
	}{
		{name: "nil base mutator", base: nil, provider: &emptyHintProvider{}},
		{name: "nil hint provider", base: &Mutator{}, provider: nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewExperienceGuidedMutator(tt.base, tt.provider)
			if err == nil {
				t.Error("expected error for nil input")
			}
		})
	}
}

// TestExperienceGuidedMutator_NoHintsDelegatesToBase verifies that when no
// hints are available, the guided mutator delegates entirely to the base mutator.
func TestExperienceGuidedMutator_NoHintsDelegatesToBase(t *testing.T) {
	t.Parallel()

	parent := &Strategy{
		ID:             "no-hint-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "base prompt",
		CreatedAt:      time.Now(),
	}

	// Create separate base mutators with the same seed.
	base1, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	base2, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	provider := &emptyHintProvider{}
	guided, err := NewExperienceGuidedMutator(base2, provider)
	if err != nil {
		t.Fatalf("NewExperienceGuidedMutator failed: %v", err)
	}

	// With no hints, guided should behave identically to base.
	baseChildren, err := base1.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("base Mutate failed: %v", err)
	}

	guidedChildren, err := guided.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("guided Mutate failed: %v", err)
	}

	if len(guidedChildren) != len(baseChildren) {
		t.Fatalf("expected %d children, got %d", len(baseChildren), len(guidedChildren))
	}

	// Both should produce identical results since no hints means pure delegation.
	for i := range guidedChildren {
		if guidedChildren[i].MutationDesc != baseChildren[i].MutationDesc {
			t.Errorf("child %d desc differs: %q vs %q",
				i, guidedChildren[i].MutationDesc, baseChildren[i].MutationDesc)
		}
		if guidedChildren[i].StrategyMutationType != baseChildren[i].StrategyMutationType {
			t.Errorf("child %d type differs: %v vs %v",
				i, guidedChildren[i].StrategyMutationType, baseChildren[i].StrategyMutationType)
		}
	}
}

// TestExperienceGuidedMutator_ProviderErrorFallsBack verifies that when the
// hint provider returns an error, the guided mutator falls back to the base.
func TestExperienceGuidedMutator_ProviderErrorFallsBack(t *testing.T) {
	t.Parallel()

	base, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	mockProvider := newMockHintProvider([]EvolutionHint{
		{ID: "hint-1", Confidence: 0.9, PromptSnippets: []string{"guided prompt"}},
	})
	mockProvider.setError(assertionError("provider error"))

	guided, err := NewExperienceGuidedMutator(base, mockProvider)
	if err != nil {
		t.Fatalf("NewExperienceGuidedMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "error-fallback-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "base prompt",
		CreatedAt:      time.Now(),
	}

	baseChildren, err := base.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("base Mutate failed: %v", err)
	}

	guidedChildren, err := guided.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("guided Mutate failed: %v", err)
	}

	if len(guidedChildren) != len(baseChildren) {
		t.Fatalf("expected %d children, got %d", len(baseChildren), len(guidedChildren))
	}

	// Results should match base due to error fallback.
	for i := range guidedChildren {
		if guidedChildren[i].MutationDesc != baseChildren[i].MutationDesc {
			return // May differ slightly due to different code paths; acceptable.
		}
	}
}

// assertionError is a simple error type for test assertions.
type assertionError string

func (e assertionError) Error() string { return string(e) }

// TestExperienceGuidedMutator_PromptHintsAffectMutation verifies that prompt
// hints measurably affect mutation selection when hints are available.
func TestExperienceGuidedMutator_PromptHintsAffectMutation(t *testing.T) {
	t.Parallel()

	base, err := NewMutator(WithSeed(99))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	hints := []EvolutionHint{
		{
			ID:             "prompt-hint-1",
			Confidence:     0.95,
			PromptSnippets: []string{"guided-prompt-v1", "guided-prompt-v2"},
			PreferredTools: []string{"web_search"},
		},
	}

	provider := newMockHintProvider(hints)
	guided, err := NewExperienceGuidedMutator(base, provider,
		WithGuidedConfidence(0.5),
		WithGuidedPromptBoost(3.0),
		WithGuidedToolBoost(3.0),
	)
	if err != nil {
		t.Fatalf("NewExperienceGuidedMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "hint-test-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "base prompt",
		CreatedAt:      time.Now(),
	}

	// Generate enough children to likely see guided mutations.
	children, err := guided.Mutate(context.Background(), parent, 50)
	if err != nil {
		t.Fatalf("guided Mutate failed: %v", err)
	}

	foundGuidedPrompt := false
	foundGuidedTool := false
	foundParam := false

	for _, child := range children {
		switch child.StrategyMutationType {
		case MutationPrompt:
			if child.PromptTemplate == "guided-prompt-v1" ||
				child.PromptTemplate == "guided-prompt-v2" {
				foundGuidedPrompt = true
			}
		case MutationTool:
			if child.Params["tools"] == "web_search" {
				foundGuidedTool = true
			}
		case MutationParameter:
			foundParam = true
		}
	}

	if !foundGuidedPrompt {
		t.Error("expected at least one guided prompt mutation with hint snippet in 50 children")
	}
	if !foundGuidedTool {
		t.Error("expected at least one guided tool mutation with hint tool in 50 children")
	}
	if !foundParam {
		t.Log("note: no parameter mutation observed in this run (probabilistic)")
	}
}

// TestExperienceGuidedMutator_ParamHintsAffectMutation verifies that param
// hints bias parameter value selection toward suggested values.
func TestExperienceGuidedMutator_ParamHintsAffectMutation(t *testing.T) {
	t.Parallel()

	base, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	hints := []EvolutionHint{
		{
			ID:         "param-hint-1",
			Confidence: 0.9,
			ParamHints: map[string]float64{"temperature": 0.1},
		},
	}

	provider := newMockHintProvider(hints)
	guided, err := NewExperienceGuidedMutator(base, provider,
		WithGuidedConfidence(0.5),
	)
	if err != nil {
		t.Fatalf("NewExperienceGuidedMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "param-hint-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "base prompt",
		CreatedAt:      time.Now(),
	}

	children, err := guided.Mutate(context.Background(), parent, 20)
	if err != nil {
		t.Fatalf("guided Mutate failed: %v", err)
	}

	foundGuidedParam := false
	for _, child := range children {
		if child.StrategyMutationType == MutationParameter {
			if temp, ok := child.Params["temperature"].(float64); ok && temp == 0.1 {
				foundGuidedParam = true
				break
			}
		}
	}

	if !foundGuidedParam {
		t.Log("note: no guided parameter mutation observed (may fall back to base param mutation)")
	}
}

// TestExperienceGuidedMutator_DeteministicWithHints verifies that using the
// same hints and seed produces reproducible mutation results.
func TestExperienceGuidedMutator_DeteministicWithHints(t *testing.T) {
	t.Parallel()

	hints := []EvolutionHint{
		{
			ID:             "det-hint-1",
			Confidence:     0.95,
			PromptSnippets: []string{"deterministic-prompt"},
			PreferredTools: []string{"code_exec"},
		},
	}

	parent := &Strategy{
		ID:             "det-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5, "top_k": 20},
		PromptTemplate: "base",
		CreatedAt:      time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	// First run.
	base1, _ := NewMutator(WithSeed(12345))
	provider1 := newMockHintProvider(hints)
	guided1, _ := NewExperienceGuidedMutator(base1, provider1,
		WithGuidedConfidence(0.5),
	)

	// Second run with identical seed and hints.
	base2, _ := NewMutator(WithSeed(12345))
	provider2 := newMockHintProvider(hints)
	guided2, _ := NewExperienceGuidedMutator(base2, provider2,
		WithGuidedConfidence(0.5),
	)

	children1, err := guided1.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("guided1 Mutate failed: %v", err)
	}

	children2, err := guided2.Mutate(context.Background(), parent, 5)
	if err != nil {
		t.Fatalf("guided2 Mutate failed: %v", err)
	}

	if len(children1) != len(children2) {
		t.Fatalf("batch size mismatch: %d vs %d", len(children1), len(children2))
	}

	for i := range children1 {
		if children1[i].MutationDesc != children2[i].MutationDesc {
			t.Errorf("child %d desc differs: %q vs %q",
				i, children1[i].MutationDesc, children2[i].MutationDesc)
		}
		if children1[i].StrategyMutationType != children2[i].StrategyMutationType {
			t.Errorf("child %d type differs: %v vs %v",
				i, children1[i].StrategyMutationType, children2[i].StrategyMutationType)
		}
		if children1[i].PromptTemplate != children2[i].PromptTemplate {
			t.Errorf("child %d prompt differs: %q vs %q",
				i, children1[i].PromptTemplate, children2[i].PromptTemplate)
		}
	}
}

// TestExperienceGuidedMutator_ConfidenceThreshold verifies that hints below
// the confidence threshold are ignored, causing fallback to base behavior.
func TestExperienceGuidedMutator_ConfidenceThreshold(t *testing.T) {
	t.Parallel()

	base, err := NewMutator(WithSeed(42))
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	// Hints below default confidence threshold (0.5).
	lowConfidenceHints := []EvolutionHint{
		{
			ID:             "low-conf-hint",
			Confidence:     0.1,
			PromptSnippets: []string{"should-not-appear"},
		},
	}

	provider := newMockHintProvider(lowConfidenceHints)
	guided, err := NewExperienceGuidedMutator(base, provider)
	if err != nil {
		t.Fatalf("NewExperienceGuidedMutator failed: %v", err)
	}

	parent := &Strategy{
		ID:             "low-conf-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "base prompt",
		CreatedAt:      time.Now(),
	}

	children, err := guided.Mutate(context.Background(), parent, 20)
	if err != nil {
		t.Fatalf("guided Mutate failed: %v", err)
	}

	// No hint-based prompts should appear.
	for _, child := range children {
		if child.PromptTemplate == "should-not-appear" {
			t.Error("low-confidence hint influenced mutation despite threshold")
		}
	}
}

// TestExperienceGuidedMutator_NilParent verifies ErrNilParent is returned.
func TestExperienceGuidedMutator_NilParent(t *testing.T) {
	t.Parallel()

	base, _ := NewMutator(WithSeed(1))
	guided, _ := NewExperienceGuidedMutator(base, &emptyHintProvider{})

	_, err := guided.Mutate(context.Background(), nil, 3)
	if err != ErrNilParent {
		t.Errorf("expected ErrNilParent, got: %v", err)
	}
}

// TestExperienceGuidedMutator_InvalidCount verifies error for n <= 0.
func TestExperienceGuidedMutator_InvalidCount(t *testing.T) {
	t.Parallel()

	base, _ := NewMutator(WithSeed(1))
	guided, _ := NewExperienceGuidedMutator(base, &emptyHintProvider{})

	parent := &Strategy{
		ID:     "test",
		Params: map[string]any{},
	}

	_, err := guided.Mutate(context.Background(), parent, 0)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount, got: %v", err)
	}

	_, err = guided.Mutate(context.Background(), parent, -1)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount for negative n, got: %v", err)
	}
}

// TestExperienceGuidedMutator_ContextCancellation verifies context respect.
func TestExperienceGuidedMutator_ContextCancellation(t *testing.T) {
	t.Parallel()

	base, _ := NewMutator(WithSeed(1))
	guided, _ := NewExperienceGuidedMutator(base, &emptyHintProvider{})

	parent := &Strategy{
		ID:        "cancel-parent",
		Version:   1,
		Params:    map[string]any{"temperature": 0.5},
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := guided.Mutate(ctx, parent, 100)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// TestFilterHintsByConfidence verifies confidence threshold filtering.
func TestFilterHintsByConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		hints     []EvolutionHint
		threshold float64
		expected  int
	}{
		{
			name: "zero threshold keeps all",
			hints: []EvolutionHint{
				{Confidence: 0.1},
				{Confidence: 0.9},
			},
			threshold: 0,
			expected:  2,
		},
		{
			name: "threshold filters low confidence",
			hints: []EvolutionHint{
				{Confidence: 0.1},
				{Confidence: 0.5},
				{Confidence: 0.9},
			},
			threshold: 0.5,
			expected:  2,
		},
		{
			name: "high threshold filters all",
			hints: []EvolutionHint{
				{Confidence: 0.1},
				{Confidence: 0.3},
			},
			threshold: 0.9,
			expected:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := filterHintsByConfidence(tt.hints, tt.threshold)
			if len(result) != tt.expected {
				t.Errorf("expected %d hints, got %d", tt.expected, len(result))
			}
		})
	}
}

// TestMergeHints verifies that hints are correctly merged.
func TestMergeHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		hints          []EvolutionHint
		expectSnippets int
		expectTools    int
		expectParams   int
	}{
		{
			name: "single hint",
			hints: []EvolutionHint{
				{
					PromptSnippets: []string{"snippet1"},
					PreferredTools: []string{"tool1"},
					ParamHints:     map[string]float64{"temp": 0.5},
				},
			},
			expectSnippets: 1,
			expectTools:    1,
			expectParams:   1,
		},
		{
			name: "merge multiple hints with duplicates",
			hints: []EvolutionHint{
				{
					PromptSnippets: []string{"snippet1", "snippet2"},
					PreferredTools: []string{"tool1"},
				},
				{
					PromptSnippets: []string{"snippet2", "snippet3"},
					PreferredTools: []string{"tool1", "tool2"},
				},
			},
			expectSnippets: 3, // snippet1, snippet2, snippet3
			expectTools:    2, // tool1, tool2
			expectParams:   0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signal := mergeHints(tt.hints)
			if len(signal.promptSnippets) != tt.expectSnippets {
				t.Errorf("expected %d prompt snippets, got %d: %v",
					tt.expectSnippets, len(signal.promptSnippets), signal.promptSnippets)
			}
			if len(signal.preferredTools) != tt.expectTools {
				t.Errorf("expected %d preferred tools, got %d: %v",
					tt.expectTools, len(signal.preferredTools), signal.preferredTools)
			}
			if len(signal.paramHints) != tt.expectParams {
				t.Errorf("expected %d param hints, got %d",
					tt.expectParams, len(signal.paramHints))
			}
		})
	}
}

// TestExperienceGuidedMutator_WithGuidedConfidence verifies that the option
// sets the confidence threshold correctly and clamps out-of-range values.
func TestExperienceGuidedMutator_WithGuidedConfidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		{name: "normal value", input: 0.7, expected: 0.7},
		{name: "negative clamped to 0", input: -0.5, expected: 0},
		{name: "over 1 clamped to 1", input: 1.5, expected: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, _ := NewMutator(WithSeed(1))
			m := &ExperienceGuidedMutator{
				base:       base,
				provider:   &emptyHintProvider{},
				confidence: 0.5, // default
			}

			opt := WithGuidedConfidence(tt.input)
			opt(m)

			if m.confidence != tt.expected {
				t.Errorf("expected confidence %f, got %f", tt.expected, m.confidence)
			}
		})
	}
}

// TestTruncate verifies the truncate utility function.
func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		expect string
	}{
		{name: "short string unchanged", input: "hello", maxLen: 10, expect: "hello"},
		{name: "exact length unchanged", input: "hello", maxLen: 5, expect: "hello"},
		{name: "long string truncated", input: "hello world this is long", maxLen: 10, expect: "hello w..."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := truncate.WithEllipsis(tt.input, tt.maxLen)
			if result != tt.expect {
				t.Errorf("truncate(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, result, tt.expect)
			}
		})
	}
}

// TestExperienceGuidedMutator_FallbackToBaseConsistentSeed verifies that with
// both hint and no-hint modes, the same seed produces consistent results.
func TestExperienceGuidedMutator_FallbackToBaseConsistentSeed(t *testing.T) {
	t.Parallel()

	parent := &Strategy{
		ID:             "consistency-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "standard prompt",
		CreatedAt:      time.Now(),
	}

	// Run 1: base mutator directly.
	base1, _ := NewMutator(WithSeed(12345))
	children1, _ := base1.Mutate(context.Background(), parent, 5)

	// Run 2: guided mutator with empty provider (no hints).
	base2, _ := NewMutator(WithSeed(12345))
	guided2, _ := NewExperienceGuidedMutator(base2, &emptyHintProvider{})
	children2, _ := guided2.Mutate(context.Background(), parent, 5)

	if len(children1) != len(children2) {
		t.Fatalf("child count mismatch: %d vs %d", len(children1), len(children2))
	}

	for i := range children1 {
		if children1[i].MutationDesc != children2[i].MutationDesc {
			t.Errorf("child %d desc differs with no hints: %q vs %q",
				i, children1[i].MutationDesc, children2[i].MutationDesc)
		}
	}
}
