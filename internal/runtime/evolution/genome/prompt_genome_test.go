package genome

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aresgenome "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func newTestMutator(t *testing.T) *mutation.Mutator {
	t.Helper()
	m, err := mutation.NewMutator(
		mutation.WithParamRanges(mutation.DefaultParamRanges),
	)
	require.NoError(t, err)
	return m
}

func newTestPromptStrategy(t *testing.T) *mutation.Strategy {
	t.Helper()
	return &mutation.Strategy{
		ID:             "test-strategy",
		Name:           "test",
		Params:         map[string]any{"temperature": 0.7, "top_k": 50},
		PromptTemplate: "You are a helpful assistant.",
	}
}

func TestPromptGenome_Name(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})
	assert.Equal(t, "prompt", g.Name())
}

func TestPromptGenome_Strategy(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})
	assert.Equal(t, s.ID, g.Strategy().ID)
}

func TestPromptGenome_Mutate(t *testing.T) {
	s := newTestPromptStrategy(t)
	m := newTestMutator(t)
	g := NewPromptGenome(s, PromptGenomeConfig{Mutator: m})

	children, err := g.Mutate(context.Background(), 3)
	require.NoError(t, err)
	require.Len(t, children, 3)

	for _, child := range children {
		pg, ok := child.(*PromptGenome)
		require.True(t, ok)
		assert.NotNil(t, pg.Strategy())
	}
}

func TestPromptGenome_Mutate_Zero(t *testing.T) {
	s := newTestPromptStrategy(t)
	m := newTestMutator(t)
	g := NewPromptGenome(s, PromptGenomeConfig{Mutator: m})

	children, err := g.Mutate(context.Background(), 0)
	require.NoError(t, err)
	assert.Len(t, children, 0)
}

func TestPromptGenome_Mutate_NoMutator(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})

	_, err := g.Mutate(context.Background(), 3)
	assert.Error(t, err)
}

func TestPromptGenome_Crossover_WithCrosser(t *testing.T) {
	s := newTestPromptStrategy(t)
	m := newTestMutator(t)
	c, err := aresgenome.NewCrossover()
	require.NoError(t, err)

	parentA := NewPromptGenome(s, PromptGenomeConfig{Mutator: m, Crosser: c})
	parentB := NewPromptGenome(s, PromptGenomeConfig{Mutator: m, Crosser: c})

	child, err := parentA.Crossover(context.Background(), parentB)
	require.NoError(t, err)
	assert.NotNil(t, child)
	assert.Equal(t, "prompt", child.Name())
}

func TestPromptGenome_Crossover_Fallback(t *testing.T) {
	s := newTestPromptStrategy(t)
	m := newTestMutator(t)

	parentA := NewPromptGenome(s, PromptGenomeConfig{Mutator: m})
	parentB := NewPromptGenome(s, PromptGenomeConfig{Mutator: m})

	child, err := parentA.Crossover(context.Background(), parentB)
	require.NoError(t, err)
	assert.NotNil(t, child)
}

func TestPromptGenome_Crossover_IncompatibleType(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})

	other := &RecoveryGenome{}
	_, err := g.Crossover(context.Background(), other)
	assert.Error(t, err)
}

func TestPromptGenome_Fitness_WithScorer(t *testing.T) {
	s := newTestPromptStrategy(t)
	scorer := func(agent *mutation.Strategy) float64 {
		if agent.ID == "test-strategy" {
			return 0.85
		}
		return 0.0
	}

	g := NewPromptGenome(s, PromptGenomeConfig{Scorer: scorer})

	fit, err := g.Fitness(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.85, fit, 0.001)
}

func TestPromptGenome_Fitness_NoScorer(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})

	fit, err := g.Fitness(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.5, fit, 0.001)
}

func TestPromptGenome_Fitness_UnevaluatedScore(t *testing.T) {
	s := newTestPromptStrategy(t)
	s.Score = aresgenome.ScoreUnevaluated
	scorer := func(agent *mutation.Strategy) float64 {
		return agent.Score
	}

	g := NewPromptGenome(s, PromptGenomeConfig{Scorer: scorer})

	fit, err := g.Fitness(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0.0, fit)
}

func TestPromptGenome_Snapshot(t *testing.T) {
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})

	snap, err := g.Snapshot(context.Background())
	require.NoError(t, err)

	strategy, ok := snap.(*mutation.Strategy)
	require.True(t, ok)
	assert.Equal(t, s.ID, strategy.ID)
}

func TestPromptGenome_Immutability(t *testing.T) {
	// Verify that the genome's strategy is cloned, not shared.
	s := newTestPromptStrategy(t)
	g := NewPromptGenome(s, PromptGenomeConfig{})

	g.Strategy().Name = "modified-original"
	assert.Equal(t, "test", s.Name,
		"modifying the genome's strategy should not affect the original")
}
