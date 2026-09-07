package evolution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestNewGenomePopulationAdapter(t *testing.T) {
	t.Run("valid_dependencies", func(t *testing.T) {
		base := &mutation.Strategy{ID: "base", Params: map[string]any{"t": 0.7}, Score: 50.0, CreatedAt: time.Now()}
		pop, err := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
		if err != nil {
			t.Fatal(err)
		}
		mut := &mockGenomeMutator{}
		crosser, _ := genome.NewCrossover(genome.WithSeed(1))

		adapter, err := NewGenomePopulationAdapter(pop, mut, crosser)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if adapter.Population() != pop {
			t.Error("Population() returned wrong instance")
		}
	})

	t.Run("nil_population_returns_error", func(t *testing.T) {
		_, err := NewGenomePopulationAdapter(nil, &mockGenomeMutator{}, &genome.Crossover{})
		if err == nil {
			t.Fatal("expected error for nil population")
		}
	})

	t.Run("nil_mutator_returns_error", func(t *testing.T) {
		base := &mutation.Strategy{ID: "base", Params: map[string]any{"t": 0.7}, Score: 50.0, CreatedAt: time.Now()}
		pop, _ := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
		_, err := NewGenomePopulationAdapter(pop, nil, &genome.Crossover{})
		if err == nil {
			t.Fatal("expected error for nil mutator")
		}
	})

	t.Run("nil_crosser_returns_error", func(t *testing.T) {
		base := &mutation.Strategy{ID: "base", Params: map[string]any{"t": 0.7}, Score: 50.0, CreatedAt: time.Now()}
		pop, _ := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
		_, err := NewGenomePopulationAdapter(pop, &mockGenomeMutator{}, nil)
		if err == nil {
			t.Fatal("expected error for nil crosser")
		}
	})
}

func TestGenomePopulationAdapter_Run(t *testing.T) {
	ctx := context.Background()
	base := &mutation.Strategy{
		ID: "adapter-test", Version: 1,
		Params: map[string]any{"temperature": 0.7, "top_k": 40},
		Score:  50.0, CreatedAt: time.Now(),
	}
	mut := &mockGenomeMutator{}
	crosser, _ := genome.NewCrossover(genome.WithSeed(42))

	pop, err := genome.NewPopulation(ctx, base, mut,
		genome.WithPopulationSize(10),
		genome.WithEliteCount(1),
	)
	if err != nil {
		t.Fatal(err)
	}

	adapter, err := NewGenomePopulationAdapter(pop, mut, crosser)
	if err != nil {
		t.Fatal(err)
	}

	genBefore := pop.Generation
	if err := adapter.Run(ctx); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if pop.Generation != genBefore+1 {
		t.Errorf("generation = %d, want %d", pop.Generation, genBefore+1)
	}
	stats := pop.Stats()
	if stats.Size != 10 {
		t.Errorf("population size = %d, want 10", stats.Size)
	}
}

func TestGenomeMutatorAdapter(t *testing.T) {
	t.Run("delegates_to_wrapped_mutator", func(t *testing.T) {
		raw, err := mutation.NewMutator(mutation.WithSeed(42))
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := NewGenomeMutatorAdapter(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		parent := &mutation.Strategy{
			ID: "parent", Version: 1,
			Params:         map[string]any{"temperature": 0.7},
			PromptTemplate: "test",
			CreatedAt:      time.Now(),
		}
		children, err := adapter.Mutate(context.Background(), parent, 3)
		if err != nil {
			t.Fatalf("Mutate failed: %v", err)
		}
		if len(children) != 3 {
			t.Errorf("got %d children, want 3", len(children))
		}
		for i, c := range children {
			if c.ParentID != parent.ID {
				t.Errorf("child %d ParentID = %q, want %q", i, c.ParentID, parent.ID)
			}
		}
	})

	t.Run("nil_mutator_returns_error", func(t *testing.T) {
		_, err := NewGenomeMutatorAdapter(nil)
		if err == nil {
			t.Fatal("expected error for nil mutator")
		}
	})
}

func TestPopulationGenealogyRecorder(t *testing.T) {
	t.Run("record_and_retrieve", func(t *testing.T) {
		r := NewPopulationGenealogyRecorder()
		ctx := context.Background()

		if r.Count() != 0 {
			t.Errorf("initial count = %d, want 0", r.Count())
		}

		_ = r.Record(ctx, StrategyLineage{
			ParentID: "p1", ChildID: "c1", MutationType: "crossover", WinRate: 0.8,
		})
		if r.Count() != 1 {
			t.Errorf("count after 1 record = %d, want 1", r.Count())
		}

		_ = r.Record(ctx, StrategyLineage{
			ParentID: "p2", ChildID: "c2", MutationType: "mutation", WinRate: 0.6,
		})
		if r.Count() != 2 {
			t.Errorf("count after 2 records = %d, want 2", r.Count())
		}

		lineages := r.Lineages()
		if len(lineages) != 2 {
			t.Fatalf("Lineages() returned %d, want 2", len(lineages))
		}
		if lineages[0].ChildID != "c1" || lineages[1].ChildID != "c2" {
			t.Errorf("unexpected lineage order: %+v", lineages)
		}
	})

	t.Run("concurrent_safety", func(t *testing.T) {
		r := NewPopulationGenealogyRecorder()
		ctx := context.Background()
		done := make(chan struct{})

		go func() {
			for i := 0; i < 50; i++ {
				_ = r.Record(ctx, StrategyLineage{ChildID: "c", ParentID: "p"})
			}
			close(done)
		}()

		for i := 0; i < 50; i++ {
			_ = r.Lineages()
			_ = r.Count()
		}
		<-done
		if r.Count() != 50 {
			t.Errorf("concurrent count = %d, want 50", r.Count())
		}
	})
}

func TestRecordPopulationLineage(t *testing.T) {
	ctx := context.Background()

	t.Run("records_agents_with_parents", func(t *testing.T) {
		pop := &genome.Population{
			Agents: []*mutation.Strategy{
				{ID: "child-1", ParentID: "parent×other", Version: 3, Score: 80, CreatedAt: time.Now()},
				{ID: "child-2", ParentID: "parent×other", Version: 2, Score: 70, CreatedAt: time.Now()},
				{ID: "root", ParentID: "", Version: 1, Score: 50, CreatedAt: time.Now()},
			},
			Generation: 5,
		}
		recorder := NewPopulationGenealogyRecorder()

		count, err := RecordPopulationLineage(ctx, pop, recorder, nil, 4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 2 {
			t.Errorf("recorded %d lineages, want 2", count)
		}
	})

	t.Run("skips_empty_parents", func(t *testing.T) {
		pop := &genome.Population{
			Agents: []*mutation.Strategy{
				{ID: "root", ParentID: "", Version: 1, CreatedAt: time.Now()},
			},
		}
		recorder := NewPopulationGenealogyRecorder()

		count, err := RecordPopulationLineage(ctx, pop, recorder, nil, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("recorded %d lineages, want 0", count)
		}
	})

	t.Run("nil_pop_or_recorder_is_noop", func(t *testing.T) {
		count, err := RecordPopulationLineage(ctx, nil, NewPopulationGenealogyRecorder(), nil, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("recorded %d lineages for nil pop", count)
		}

		pop := &genome.Population{Agents: []*mutation.Strategy{}}
		count, err = RecordPopulationLineage(ctx, pop, nil, nil, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if count != 0 {
			t.Errorf("recorded %d lineages for nil recorder", count)
		}
	})
}

func TestWiredEvolutionSystem(t *testing.T) {
	base := &mutation.Strategy{
		ID: "wired-root", Version: 1,
		Params:         map[string]any{"temperature": 0.7, "top_k": 40},
		PromptTemplate: "You are helpful.",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	t.Run("creates_system_with_default_config", func(t *testing.T) {
		cfg := DefaultSystemConfig()
		cfg.PopulationSize = 8
		cfg.EnableDreamCycle = false
		cfg.EnableScheduler = false

		system, err := NewWiredEvolutionSystem(base, cfg)
		if err != nil {
			t.Fatalf("NewWiredEvolutionSystem failed: %v", err)
		}
		if system.Population == nil {
			t.Fatal("Population is nil")
		}
		if system.PopAdapter == nil {
			t.Fatal("PopAdapter is nil")
		}
		if system.Genealogy == nil {
			t.Fatal("Genealogy is nil")
		}
		if system.Population.Size != 8 {
			t.Errorf("population size = %d, want 8", system.Population.Size)
		}
		if system.Scheduler != nil {
			t.Error("scheduler should be nil when disabled")
		}
		if system.DreamCycle != nil {
			t.Error("dream cycle should be nil when disabled")
		}
	})

	t.Run("nil_base_returns_error", func(t *testing.T) {
		_, err := NewWiredEvolutionSystem(nil, DefaultSystemConfig())
		if err == nil {
			t.Fatal("expected error for nil base strategy")
		}
	})
}

func TestRunIdleEvolution(t *testing.T) {
	base := &mutation.Strategy{
		ID: "idle-root", Version: 1,
		Params:         map[string]any{"temperature": 0.7},
		PromptTemplate: "test prompt.",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 10
	cfg.EliteCount = 1
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range system.Population.Agents {
		a.Score = float64(int(a.Score) % 100)
	}

	ctx := context.Background()
	if err := RunIdleEvolution(ctx, system, 5); err != nil {
		t.Fatalf("RunIdleEvolution failed: %v", err)
	}

	if system.Population.Generation != 5 {
		t.Errorf("generation = %d, want 5", system.Population.Generation)
	}

	stats := system.Population.Stats()
	if stats.Size != 10 {
		t.Errorf("population size = %d, want 10", stats.Size)
	}
	if stats.BestScore < 0 {
		t.Error("best score should be non-negative")
	}

	lineages := system.Genealogy.Lineages()
	if len(lineages) == 0 {
		t.Error("expected at least one lineage record after evolution")
	}

	best, err := BestStrategyFromSystem(system)
	if err != nil {
		t.Fatalf("BestStrategyFromSystem failed: %v", err)
	}
	if best == nil {
		t.Fatal("best strategy is nil")
	}
	if best.ID == "" {
		t.Error("best strategy has empty ID")
	}
}

func TestRunIdleEvolution_Cancellation(t *testing.T) {
	base := &mutation.Strategy{
		ID: "cancel-root", Version: 1,
		Params:    map[string]any{"temperature": 0.7},
		Score:     50.0,
		CreatedAt: time.Now(),
	}

	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 10
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range system.Population.Agents {
		a.Score = float64(int(a.Score) % 100)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = RunIdleEvolution(ctx, system, 100)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestBestStrategyFromSystem(t *testing.T) {
	t.Run("returns_best_strategy", func(t *testing.T) {
		base := &mutation.Strategy{
			ID: "best-root", Version: 1,
			Params:    map[string]any{"temperature": 0.7},
			Score:     50.0,
			CreatedAt: time.Now(),
		}
		cfg := DefaultSystemConfig()
		cfg.PopulationSize = 5
		cfg.EnableDreamCycle = false
		cfg.EnableScheduler = false

		system, err := NewWiredEvolutionSystem(base, cfg)
		if err != nil {
			t.Fatal(err)
		}

		system.Population.Agents[0].Score = 99.0
		for i := 1; i < len(system.Population.Agents); i++ {
			system.Population.Agents[i].Score = float64(i * 10)
		}

		best, err := BestStrategyFromSystem(system)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if best.Score != 99.0 {
			t.Errorf("best.Score = %f, want 99.0", best.Score)
		}
		best.Score = 0
		if system.Population.Agents[0].Score == 0 {
			t.Error("BestStrategy must return a clone, not the original")
		}
	})

	t.Run("nil_system_returns_error", func(t *testing.T) {
		_, err := BestStrategyFromSystem(nil)
		if err == nil {
			t.Fatal("expected error for nil system")
		}
	})
}

func TestShutdown(t *testing.T) {
	base := &mutation.Strategy{
		ID: "shutdown-root", Version: 1,
		Params:    map[string]any{"temperature": 0.7},
		Score:     50.0,
		CreatedAt: time.Now(),
	}
	cfg := DefaultSystemConfig()
	cfg.EnableScheduler = false
	cfg.EnableDreamCycle = false

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatal(err)
	}

	Shutdown(system)
	Shutdown(nil)
}

func TestFullWiredEvolutionLifecycle(t *testing.T) {
	base := &mutation.Strategy{
		ID: "lifecycle-root", Version: 1,
		Params:         map[string]any{"temperature": 0.7, "top_k": 40, "max_steps": 10},
		PromptTemplate: "You are a helpful assistant.",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 12
	cfg.EliteCount = 2
	cfg.MutationRate = 0.2
	cfg.SurvivalRate = 0.6
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatal(err)
	}

	for _, a := range system.Population.Agents {
		a.Score = float64(int(a.Score) % 100)
	}

	firstBest := system.Population.Best().Score

	ctx := context.Background()
	if err := RunIdleEvolution(ctx, system, 10); err != nil {
		t.Fatalf("full lifecycle evolution failed: %v", err)
	}

	if system.Population.Generation != 10 {
		t.Errorf("generation = %d, want 10", system.Population.Generation)
	}
	if len(system.Population.Agents) != 12 {
		t.Errorf("population size = %d, want 12", len(system.Population.Agents))
	}

	lineages := system.Genealogy.Lineages()
	if len(lineages) == 0 {
		t.Error("expected lineage records after lifecycle evolution")
	}

	best, err := BestStrategyFromSystem(system)
	if err != nil {
		t.Fatal(err)
	}
	if best.Score < firstBest-50 {
		t.Logf("score evolved: %.2f -> %.2f", firstBest, best.Score)
	}
	if best.Params == nil {
		t.Error("best strategy has nil params")
	}

	Shutdown(system)
}
