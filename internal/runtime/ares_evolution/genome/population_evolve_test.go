package genome

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestEvolveAfterScoring(t *testing.T) {
	t.Parallel()

	t.Run("scores_before_and_after_evolution", func(t *testing.T) {
		ctx := context.Background()
		base := &mutation.Strategy{
			ID: "base", Version: 1,
			Params: map[string]any{"temperature": 0.7},
			Score:  50.0, CreatedAt: testNow(),
		}
		pop, err := NewPopulation(ctx, base, &mockGenomeMutator{},
			WithPopulationSize(8), WithPopulationSeed(42))
		if err != nil {
			t.Fatalf("create population: %v", err)
		}

		callCount := 0
		scorer := func(agent *mutation.Strategy) float64 {
			callCount++
			return 75.0
		}

		crosser, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("create crossover: %v", err)
		}

		err = pop.EvolveAfterScoring(ctx, scorer, &mockGenomeMutator{}, crosser)
		if err != nil {
			t.Fatalf("EvolveAfterScoring failed: %v", err)
		}

		if pop.Generation != 1 {
			t.Errorf("generation = %d, want 1", pop.Generation)
		}
		if callCount == 0 {
			t.Error("scorer was never called")
		}
	})

	t.Run("rejects_nil_scorer", func(t *testing.T) {
		ctx := context.Background()
		base := testBaseStrategy()
		pop, err := NewPopulation(ctx, base, &mockGenomeMutator{}, WithPopulationSize(5))
		if err != nil {
			t.Fatalf("create population: %v", err)
		}
		crosser, err := NewCrossover(WithSeed(1))
		if err != nil {
			t.Fatalf("create crossover: %v", err)
		}

		err = pop.EvolveAfterScoring(ctx, nil, &mockGenomeMutator{}, crosser)
		if err == nil {
			t.Fatal("expected error for nil scorer")
		}
	})

	t.Run("rejects_unevaluated_without_scorer_fallback", func(t *testing.T) {
		// Even with NoopScorer, if agents start unevaluated they stay unevaluated
		// and ensureEvaluatedBeforeSelection should catch this.
		ctx := context.Background()
		base := testBaseStrategy()
		mut := &mockGenomeMutator{}
		pop, err := NewPopulation(ctx, base, mut, WithPopulationSize(5))
		if err != nil {
			t.Fatalf("create population: %v", err)
		}

		// Deliberately set all scores to -1
		for _, a := range pop.Agents {
			a.Score = ScoreUnevaluated
		}

		crosser, err := NewCrossover(WithSeed(1))
		if err != nil {
			t.Fatalf("create crossover: %v", err)
		}

		// NoopScorer preserves ScoreUnevaluated, so evolve should fail.
		err = pop.EvolveAfterScoring(ctx, NoopScorer, mut, crosser)
		if err == nil {
			t.Error("expected failure when NoopScorer leaves agents unevaluated")
		}
	})

	t.Run("ConstantScorer_works_in_loop", func(t *testing.T) {
		ctx := context.Background()
		base := testBaseStrategy()
		pop, err := NewPopulation(ctx, base, &mockGenomeMutator{},
			WithPopulationSize(6), WithEliteCount(1), WithPopulationSeed(42),
		)
		if err != nil {
			t.Fatalf("create population: %v", err)
		}

		crosser, err := NewCrossover(WithSeed(99))
		if err != nil {
			t.Fatalf("create crossover: %v", err)
		}

		for i := 0; i < 5; i++ {
			err := pop.EvolveAfterScoring(ctx, ConstantScorer(60.0), &mockGenomeMutator{}, crosser)
			if err != nil {
				t.Fatalf("gen %d failed: %v", i, err)
			}
		}
		if pop.Generation != 5 {
			t.Errorf("generation = %d, want 5", pop.Generation)
		}
	})

	t.Run("post_scoring_assigns_scores_to_offspring", func(t *testing.T) {
		ctx := context.Background()
		base := testBaseStrategy()
		mut := &mockGenomeMutator{}
		pop, err := NewPopulation(ctx, base, mut,
			WithPopulationSize(4), WithEliteCount(0), WithPopulationSeed(42))
		if err != nil {
			t.Fatalf("create population: %v", err)
		}

		crosser, err := NewCrossover(WithSeed(7))
		if err != nil {
			t.Fatalf("create crossover: %v", err)
		}

		const targetScore = 88.0
		err = pop.EvolveAfterScoring(ctx, ConstantScorer(targetScore), mut, crosser)
		if err != nil {
			t.Fatalf("EvolveAfterScoring failed: %v", err)
		}

		for _, agent := range pop.Agents {
			if agent.Score != targetScore {
				t.Errorf("agent %s score = %f, want %f (post-scoring)", agent.ID, agent.Score, targetScore)
			}
		}
	})
}

func TestNoopScorer(t *testing.T) {
	t.Parallel()

	t.Run("preserves_existing_score", func(t *testing.T) {
		agent := &mutation.Strategy{Score: 42.0}
		result := NoopScorer(agent)
		if result != 42.0 {
			t.Errorf("NoopScorer = %f, want 42.0", result)
		}
	})

	t.Run("preserves_unevaluated", func(t *testing.T) {
		agent := &mutation.Strategy{Score: ScoreUnevaluated}
		result := NoopScorer(agent)
		if result != ScoreUnevaluated {
			t.Errorf("NoopScorer = %f, want ScoreUnevaluated", result)
		}
	})
}

func TestConstantScorer(t *testing.T) {
	t.Parallel()

	t.Run("returns_constant_value", func(t *testing.T) {
		scorer := ConstantScorer(77.0)
		agent := &mutation.Strategy{Score: 10.0}
		result := scorer(agent)
		if result != 77.0 {
			t.Errorf("ConstantScorer = %f, want 77.0", result)
		}
	})

	t.Run("ignores_agent_state", func(t *testing.T) {
		scorer := ConstantScorer(99.0)
		agent1 := &mutation.Strategy{Score: -1.0}
		agent2 := &mutation.Strategy{Score: 50.0}
		if s := scorer(agent1); s != 99.0 {
			t.Errorf("agent1 score = %f, want 99.0", s)
		}
		if s := scorer(agent2); s != 99.0 {
			t.Errorf("agent2 score = %f, want 99.0", s)
		}
	})
}

// mockGenomeMutator implements MutatorInterface for evolve-after-scoring tests.
type mockGenomeMutator struct{}

func (m *mockGenomeMutator) Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	result := make([]*mutation.Strategy, n)
	for i := range result {
		result[i] = &mutation.Strategy{
			ID:       parent.ID + "-mut",
			ParentID: parent.ID,
			Version:  parent.Version + 1,
			Params:   make(map[string]any),
			Score:    -1,
		}
	}
	return result, nil
}

func testBaseStrategy() *mutation.Strategy {
	return &mutation.Strategy{
		ID:             "test-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7},
		PromptTemplate: "test prompt",
		Score:          50.0,
		CreatedAt:      testNow(),
	}
}

func testNow() time.Time { return time.Unix(1000000, 0) }
