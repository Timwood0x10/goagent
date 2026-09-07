package evolution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

type stubMutator struct{}

func (m *stubMutator) Mutate(_ context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	out := make([]*mutation.Strategy, n)
	for i := 0; i < n; i++ {
		out[i] = &mutation.Strategy{ID: parent.ID + "-child", Score: parent.Score, Params: parent.Params}
	}
	return out, nil
}

func TestNewPopulation(t *testing.T) {
	s := &mutation.Strategy{ID: "base", Params: map[string]any{}}
	inner, err := genome.NewPopulation(context.Background(), s, &stubMutator{},
		genome.WithPopulationSize(10),
		genome.WithEliteCount(2),
		genome.WithMutationRate(0.2),
	)
	if err != nil {
		t.Fatalf("NewPopulation: %v", err)
	}
	if inner == nil {
		t.Fatal("expected non-nil population")
	}
	if inner.Size != 10 {
		t.Fatalf("expected size 10, got %d", inner.Size)
	}
}

func TestDefaultPopluationConfigValues(t *testing.T) {
	cfg := DefaultPopulationConfig()
	if cfg.Size != 20 {
		t.Fatalf("expected 20, got %d", cfg.Size)
	}
}

func TestLineageStruct(t *testing.T) {
	l := Lineage{
		ParentID:         "parent",
		ChildID:          "child",
		MutationType:     "crossover",
		WinRate:          0.8,
		ScoreImprovement: 0.15,
	}
	if l.ParentID != "parent" {
		t.Fatal("expected ParentID to be set")
	}
}

func TestNewMutator(t *testing.T) {
	mut, err := NewMutator("test-model", MutationConfig{
		ParamMutationProb:  0.3,
		PromptMutationProb: 0.3,
	})
	assert.NoError(t, err)
	assert.NotNil(t, mut)

	parent := &Strategy{
		ID:     "parent-1",
		Score:  0.5,
		Params: map[string]any{"temperature": 0.7},
	}
	child, err := mut.Mutate(context.Background(), parent)
	assert.NoError(t, err)
	assert.NotNil(t, child)
	assert.NotEqual(t, parent.ID, child.ID)
}

func TestNewMutatorDefaults(t *testing.T) {
	mut, err := NewMutator("", MutationConfig{})
	assert.NoError(t, err)
	assert.NotNil(t, mut)
}

func TestPromoterEvaluate(t *testing.T) {
	criteria := DefaultPromotionCriteria()
	adapter := NewPromoter(&criteria)
	result, err := adapter.Evaluate(context.Background(), "test-strategy", 0.9, 0.8)
	assert.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "candidate")
}

func TestPromoterDemote(t *testing.T) {
	criteria := DefaultPromotionCriteria()
	adapter := NewPromoter(&criteria)
	_, err := adapter.Evaluate(context.Background(), "test-strategy-demote", 0.5, 0.3)
	assert.NoError(t, err)

	err = adapter.Demote(context.Background(), "test-strategy-demote")
	assert.NoError(t, err)
}

func TestPromoterPromote(t *testing.T) {
	criteria := DefaultPromotionCriteria()
	adapter := NewPromoter(&criteria)
	_, err := adapter.Evaluate(context.Background(), "test-strategy-promote", 0.9, 0.8)
	assert.NoError(t, err)

	err = adapter.Promote(context.Background(), "test-strategy-promote")
	assert.NoError(t, err)
}
