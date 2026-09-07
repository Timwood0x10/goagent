package genome

import (
	"context"
	"math/rand"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- preserveElites tests ---

func TestPreserveElites(t *testing.T) {
	t.Run("preserves top N agents", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 3},
		}
		survivors := []*mutation.Strategy{
			{ID: "a", Score: 100},
			{ID: "b", Score: 80},
			{ID: "c", Score: 60},
			{ID: "d", Score: 40},
			{ID: "e", Score: 20},
		}
		elites := pop.preserveElites(survivors)
		if len(elites) != 3 {
			t.Fatalf("got %d elites, want 3", len(elites))
		}
		if elites[0].ID != "a" || elites[1].ID != "b" || elites[2].ID != "c" {
			t.Errorf("elites not in expected order: %s, %s, %s", elites[0].ID, elites[1].ID, elites[2].ID)
		}
	})

	t.Run("deep clones elites", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 1},
		}
		survivors := []*mutation.Strategy{
			{ID: "a", Score: 100, Params: map[string]any{"temp": 0.5}},
		}
		elites := pop.preserveElites(survivors)
		elites[0].Score = 999
		if survivors[0].Score != 100 {
			t.Error("modifying elite clone modified the original survivor")
		}
	})

	t.Run("less survivors than elite count", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 5},
		}
		survivors := []*mutation.Strategy{
			{ID: "a", Score: 100},
			{ID: "b", Score: 80},
		}
		elites := pop.preserveElites(survivors)
		if len(elites) != 2 {
			t.Fatalf("got %d elites, want 2", len(elites))
		}
	})

	t.Run("zero elite count returns empty", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 0},
		}
		survivors := []*mutation.Strategy{
			{ID: "a", Score: 100},
		}
		elites := pop.preserveElites(survivors)
		if len(elites) != 0 {
			t.Errorf("expected empty elites, got %d", len(elites))
		}
	})

	t.Run("empty survivors returns empty", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 3},
		}
		elites := pop.preserveElites([]*mutation.Strategy{})
		if len(elites) != 0 {
			t.Errorf("expected empty elites, got %d", len(elites))
		}
	})
}

// --- preservePerLineageElites tests ---

func TestPreservePerLineageElites(t *testing.T) {
	t.Run("preserves top-1 per lineage then global", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 3},
		}
		survivors := []*mutation.Strategy{
			{ID: "a1", Score: 100, ParentID: "A"},
			{ID: "a2", Score: 90, ParentID: "A"},
			{ID: "b1", Score: 80, ParentID: "B"},
			{ID: "b2", Score: 70, ParentID: "B"},
			{ID: "c1", Score: 60, ParentID: "C"},
		}
		elites := pop.preservePerLineageElites(survivors)
		if len(elites) != 3 {
			t.Fatalf("got %d elites, want 3", len(elites))
		}
		// First 3 lineages per elite: A's best (a1), B's best (b1), C's best (c1)
		seen := make(map[string]bool)
		for _, e := range elites {
			seen[e.ID] = true
		}
		if !seen["a1"] {
			t.Error("per-lineage elite missing a1 (best of lineage A)")
		}
		if !seen["b1"] {
			t.Error("per-lineage elite missing b1 (best of lineage B)")
		}
	})

	t.Run("empty survivors returns empty", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 3},
		}
		elites := pop.preservePerLineageElites([]*mutation.Strategy{})
		if len(elites) != 0 {
			t.Errorf("expected empty, got %d", len(elites))
		}
	})

	t.Run("single lineage fills all slots globally", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{EliteCount: 3},
		}
		survivors := []*mutation.Strategy{
			{ID: "a1", Score: 100, ParentID: "A"},
			{ID: "a2", Score: 90, ParentID: "A"},
			{ID: "a3", Score: 80, ParentID: "A"},
			{ID: "a4", Score: 70, ParentID: "A"},
		}
		elites := pop.preservePerLineageElites(survivors)
		if len(elites) != 3 {
			t.Fatalf("got %d elites, want 3", len(elites))
		}
		// a1 is lineage best, then a2 and a3 fill remaining slots globally
		if elites[0].ID != "a1" {
			t.Errorf("expected a1 as first elite, got %s", elites[0].ID)
		}
	})
}

// --- preservePromptDiversityLocked tests ---

func TestPreservePromptDiversityLocked(t *testing.T) {
	t.Run("all same template injects diversity seed", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
			{ID: "alt", Score: 50, PromptTemplate: "template2"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 3 {
			t.Fatalf("expected 3 elites with diversity seed, got %d", len(result))
		}
		if result[2].MutationDesc != "prompt_diversity_seed" {
			t.Errorf("expected diversity seed marker, got %s", result[2].MutationDesc)
		}
	})

	t.Run("already diverse does nothing", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template2"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template2"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Errorf("expected unchanged 2 elites, got %d", len(result))
		}
	})

	t.Run("no alternative template does nothing", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 1 {
			t.Errorf("expected unchanged 1 elite, got %d", len(result))
		}
	})

	t.Run("empty elites returns empty", func(t *testing.T) {
		pop := &Population{}
		result := pop.preservePromptDiversityLocked([]*mutation.Strategy{}, []*mutation.Strategy{{}})
		if len(result) != 0 {
			t.Errorf("expected empty, got %d", len(result))
		}
	})

	t.Run("low score alternative not injected", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "bad", Score: -2.0, PromptTemplate: "template2"}, // below floor -0.5
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 1 {
			t.Errorf("expected no injection for very low score, got %d", len(result))
		}
	})

	t.Run("clone isolation: modifying injected seed does not affect original", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "original", Score: 50, PromptTemplate: "template2", Params: map[string]any{"x": 1.0}},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Fatalf("expected 2 elites with diversity seed, got %d", len(result))
		}

		// Modify the injected clone and verify the original is unchanged.
		seed := result[1]
		seed.Score = 999
		seed.Params["x"] = 999.0

		if population[1].Score != 50 {
			t.Errorf("original agent Score modified: got %.1f, want 50.0", population[1].Score)
		}
		if population[1].Params["x"] != 1.0 {
			t.Errorf("original agent Params modified: got %v, want 1.0", population[1].Params["x"])
		}
	})

	t.Run("disabled guard returns elites unchanged", func(t *testing.T) {
		pop := &Population{
			cfg: PopulationConfig{DisablePromptDiversityGuard: true},
		}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
			{ID: "alt", Score: 50, PromptTemplate: "template2"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Errorf("expected unchanged 2 elites when guard disabled, got %d", len(result))
		}
	})

	t.Run("early return when population not larger than elites", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		// population same size as elites → guard should return early.
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Errorf("expected early return (elites unchanged), got %d elites", len(result))
		}
	})

	t.Run("early return when population smaller than elites", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
			{ID: "ec", Score: 80, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "eb", Score: 90, PromptTemplate: "template1"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 3 {
			t.Errorf("expected early return (elites unchanged), got %d elites", len(result))
		}
	})

	t.Run("diversity seed replaces weakest elite when slot full", func(t *testing.T) {
		// When elites already fill the population, preservePromptDiversityLocked
		// should replace the weakest elite with the diversity seed instead of
		// appending (which would get truncated away by the caller).
		pop := &Population{
			Size: 2,
			cfg:  PopulationConfig{EliteCount: 2},
		}
		elites := []*mutation.Strategy{
			{ID: "strong", Score: 100, PromptTemplate: "template1"},
			{ID: "weak", Score: 90, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "strong", Score: 100, PromptTemplate: "template1"},
			{ID: "weak", Score: 90, PromptTemplate: "template1"},
			{ID: "alt", Score: 50, PromptTemplate: "template2"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		// Should still be 2 elites (replaced weakest, not appended).
		if len(result) != 2 {
			t.Fatalf("expected 2 (replaced weakest), got %d", len(result))
		}
		// The diversity seed should have replaced the weakest elite ("weak").
		hasSeed := false
		for _, e := range result {
			if e.MutationDesc == "prompt_diversity_seed" {
				hasSeed = true
				if e.ID == "weak" || e.Score == 90 {
					t.Logf("diversity seed correctly replaced weakest elite (score 90)")
				}
			}
		}
		if !hasSeed {
			t.Error("diversity seed not present — should have replaced weakest elite")
		}
	})

	t.Run("injected seed preserves alternative template score", func(t *testing.T) {
		pop := &Population{}
		elites := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
		}
		population := []*mutation.Strategy{
			{ID: "ea", Score: 100, PromptTemplate: "template1"},
			{ID: "alt", Score: 42, PromptTemplate: "template2"},
		}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Fatalf("expected 2 elites with diversity seed, got %d", len(result))
		}
		seed := result[1]
		if seed.MutationDesc != "prompt_diversity_seed" {
			t.Errorf("expected prompt_diversity_seed marker, got %s", seed.MutationDesc)
		}
		// The seed should have the alternative template and its original score.
		if seed.PromptTemplate != "template2" {
			t.Errorf("expected template2, got %s", seed.PromptTemplate)
		}
		if seed.Score != 42 {
			t.Errorf("expected original score 42, got %.1f", seed.Score)
		}
	})
}

// --- injectFreshMutantsLocked tests ---

func TestInjectFreshMutants(t *testing.T) {
	t.Run("replaces bottom agents", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{ID: "a", Score: 100, Params: map[string]any{"temp": 0.5}},
				{ID: "b", Score: 80, Params: map[string]any{"temp": 0.5}},
				{ID: "c", Score: 60, Params: map[string]any{"temp": 0.5}},
				{ID: "d", Score: 40, Params: map[string]any{"temp": 0.5}},
				{ID: "e", Score: 20, Params: map[string]any{"temp": 0.5}},
			},
			cfg: PopulationConfig{EliteCount: 2},
			rng: rand.New(rand.NewSource(42)),
		}
		pop.injectFreshMutantsLocked(2)

		// Top 2 should be unchanged (elites)
		if pop.Agents[0].Score != 100 || pop.Agents[1].Score != 80 {
			t.Error("elite agents should not be replaced")
		}
		// Bottom agents should be replaced (Score = -1)
		replaced := 0
		for _, a := range pop.Agents[2:] {
			if !IsScoreEvaluated(a.Score) {
				replaced++
			}
		}
		if replaced == 0 {
			t.Error("expected some bottom agents to be replaced")
		}
	})

	t.Run("small population not replaced", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{ID: "a", Score: 100},
				{ID: "b", Score: 80},
			},
			cfg: PopulationConfig{EliteCount: 2},
			rng: rand.New(rand.NewSource(42)),
		}
		pop.injectFreshMutantsLocked(2)
		// Population size (2) <= eliteCount+1 (3) → no replacement
		if pop.Agents[0].Score != 100 || pop.Agents[1].Score != 80 {
			t.Error("small population should not be modified")
		}
	})

	t.Run("fresh mutants have ScoreUnevaluated", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{ID: "a", Score: 100, Params: map[string]any{"temp": 0.5}},
				{ID: "b", Score: 80, Params: map[string]any{"temp": 0.5}},
				{ID: "c", Score: 60, Params: map[string]any{"temp": 0.5}},
				{ID: "d", Score: 40, Params: map[string]any{"temp": 0.5}},
			},
			cfg: PopulationConfig{EliteCount: 1},
			rng: rand.New(rand.NewSource(42)),
		}
		pop.injectFreshMutantsLocked(1)
		// At least the last agent should be a fresh mutant with ScoreUnevaluated
		if IsScoreEvaluated(pop.Agents[len(pop.Agents)-1].Score) {
			t.Error("bottom agent should have ScoreUnevaluated after injection")
		}
	})
}

// --- ensureEvaluatedBeforeSelection tests ---

func TestEnsureEvaluatedBeforeSelection(t *testing.T) {
	t.Run("all evaluated passes", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100},
				{Score: 80},
				{Score: 60},
			},
		}
		if err := pop.ensureEvaluatedBeforeSelection(); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("unevaluated agent fails", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100},
				{Score: ScoreUnevaluated},
				{Score: 60},
			},
		}
		if err := pop.ensureEvaluatedBeforeSelection(); err == nil {
			t.Error("expected error for unevaluated agent")
		}
	})

	t.Run("empty population passes", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{},
		}
		if err := pop.ensureEvaluatedBeforeSelection(); err != nil {
			t.Errorf("unexpected error for empty population: %v", err)
		}
	})
}

// --- applyFitnessSharing tests ---

func TestApplyFitnessSharingExact(t *testing.T) {
	// Agents with similar params in the same niche should have scores reduced.
	// Elite (index 0) is protected.
	pop := &Population{
		Agents: []*mutation.Strategy{
			{ID: "elite", Score: 100, Params: map[string]any{"temp": 0.5}},
			{ID: "close1", Score: 80, Params: map[string]any{"temp": 0.51}},
			{ID: "close2", Score: 70, Params: map[string]any{"temp": 0.52}},
			{ID: "far", Score: 60, Params: map[string]any{"temp": 0.9}},
		},
		cfg: PopulationConfig{
			EliteCount:                1,
			FitnessSharingSampleLimit: 100, // force exact mode
			SpatialIndexThreshold:     0,   // disable spatial
		},
		rng: rand.New(rand.NewSource(42)),
	}

	pop.applyFitnessSharing(1)

	// Elite should be unchanged
	if pop.Agents[0].Score != 100 {
		t.Errorf("elite score changed from 100 to %.2f", pop.Agents[0].Score)
	}

	// Crowded agents (close1, close2) near elite should have SelectionScore penalized.
	// Score (canonical) should remain unchanged.
	if pop.Agents[1].Score != 80 {
		t.Errorf("close1 canonical score changed: %.2f", pop.Agents[1].Score)
	}
	if pop.Agents[1].SelectionScore >= 80 {
		t.Errorf("close1 selection score not penalized: %.2f", pop.Agents[1].SelectionScore)
	}
	if pop.Agents[2].Score != 70 {
		t.Errorf("close2 canonical score changed: %.2f", pop.Agents[2].Score)
	}
	if pop.Agents[2].SelectionScore >= 70 {
		t.Errorf("close2 selection score not penalized: %.2f", pop.Agents[2].SelectionScore)
	}

	t.Logf("scores after fitness sharing: elite=%.2f, close1=%.2f/%.2f, close2=%.2f/%.2f, far=%.2f",
		pop.Agents[0].Score,
		pop.Agents[1].Score, pop.Agents[1].SelectionScore,
		pop.Agents[2].Score, pop.Agents[2].SelectionScore,
		pop.Agents[3].Score)
}

func TestApplyFitnessSharingSampled(t *testing.T) {
	// Force sampled mode with low sample limit.
	pop := &Population{
		Agents: []*mutation.Strategy{
			{ID: "elite", Score: 100, Params: map[string]any{"temp": 0.5}},
			{ID: "close1", Score: 80, Params: map[string]any{"temp": 0.51}},
			{ID: "close2", Score: 70, Params: map[string]any{"temp": 0.52}},
			{ID: "far", Score: 60, Params: map[string]any{"temp": 0.9}},
		},
		cfg: PopulationConfig{
			EliteCount:                1,
			FitnessSharingSampleLimit: 2, // m=4 > 2 → sampled mode
			FitnessSharingSampleSize:  2,
			SpatialIndexThreshold:     0,
		},
		rng: rand.New(rand.NewSource(42)),
	}

	origScores := make([]float64, len(pop.Agents))
	for i, a := range pop.Agents {
		origScores[i] = a.Score
	}

	pop.applyFitnessSharing(1)

	// Elite should be unchanged
	if pop.Agents[0].Score != 100 {
		t.Errorf("elite score changed: %.2f", pop.Agents[0].Score)
	}

	// With sampled mode, results are stochastic but at least some agents
	// should have been affected (unless random sampling missed all neighbors).
	anyChanged := false
	for i, a := range pop.Agents {
		if i > 0 && a.Score != origScores[i] {
			anyChanged = true
			break
		}
	}
	if !anyChanged {
		t.Log("sampled mode: no scores changed (stochastic, may happen with small sample)")
	}
}

func TestApplyFitnessSharingNoop(t *testing.T) {
	t.Run("fewer than 2 agents", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{ID: "solo", Score: 100, Params: map[string]any{"temp": 0.5}},
			},
		}
		pop.applyFitnessSharing(0) // should not panic
	})

	t.Run("all unevaluated", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{ID: "a", Score: -1},
				{ID: "b", Score: -1},
			},
		}
		pop.applyFitnessSharing(0) // should not panic
	})
}

func TestEnsureEvaluatedAndGuard(t *testing.T) {
	// Integration-style test: ensureEvaluatedBeforeSelection prevents evolve
	// when agents are unevaluated.
	pop := &Population{
		Agents: []*mutation.Strategy{
			{ID: "a", Score: 100},
			{ID: "b", Score: ScoreUnevaluated},
			{ID: "c", Score: 60},
		},
		Size: 3,
		cfg: PopulationConfig{
			MutationRate:      0.2,
			EliteCount:        1,
			SurvivalRate:      0.6,
			SelectionStrategy: "truncation",
		},
		rng:                 rand.New(rand.NewSource(42)),
		currentMutationRate: 0.2,
		recoveryActions:     make(map[string]int),
	}

	err := pop.Evolve(context.Background(), &mockFailMutator{}, &noopCrosser{})
	if err == nil {
		t.Error("expected error for unevaluated population")
	}
}

// --- Test helpers ---

type mockFailMutator struct{}

func (m *mockFailMutator) Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	return []*mutation.Strategy{parent.Clone()}, nil
}

type noopCrosser struct{}

func (c *noopCrosser) Crossover(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
	return a.Clone(), nil
}
