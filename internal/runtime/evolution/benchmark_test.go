package evolution

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// ── Benchmarks for the new evolution system ──

func BenchmarkWorkflowGenome_Mutate(b *testing.B) {
	steps := []*engine.Step{
		{ID: "A", AgentType: "test"}, {ID: "B", AgentType: "test", DependsOn: []string{"A"}},
		{ID: "C", AgentType: "test", DependsOn: []string{"B"}}, {ID: "D", AgentType: "test", DependsOn: []string{"C"}},
		{ID: "E", AgentType: "test", DependsOn: []string{"D"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		b.Fatal(err)
	}
	g := genome.NewWorkflowGenome(dag, genome.DefaultWorkflowGenomeConfig())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		children, err := g.Mutate(ctx, 10)
		if err != nil {
			b.Fatal(err)
		}
		_ = children
	}
}

func BenchmarkKnowledgeGenome_Mutate(b *testing.B) {
	g := genome.NewKnowledgeGenome(nil, genome.DefaultKnowledgeGenomeConfig())
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		children, err := g.Mutate(ctx, 10)
		if err != nil {
			b.Fatal(err)
		}
		_ = children
	}
}

func BenchmarkRecoveryGenome_Mutate(b *testing.B) {
	g := genome.NewRecoveryGenome(
		&engine.RecoveryPolicy{Strategy: engine.RecoveryRetry, MaxAttempts: 3},
		genome.DefaultRecoveryGenomeConfig(),
	)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		children, err := g.Mutate(ctx, 10)
		if err != nil {
			b.Fatal(err)
		}
		_ = children
	}
}

func BenchmarkDiffEngine_Workflow(b *testing.B) {
	oldDAG := &engine.DAG{
		Nodes: map[string]*engine.DAGNode{
			"A": {}, "B": {}, "C": {},
		},
		Edges: map[string][]string{"A": {"B"}, "B": {"C"}},
	}
	newDAG := &engine.DAG{
		Nodes: map[string]*engine.DAGNode{
			"A": {}, "B": {}, "C": {}, "D": {},
		},
		Edges: map[string][]string{"A": {"B"}, "B": {"C"}, "C": {"D"}},
	}
	d := diff.NewWorkflowDiffer()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		patches, err := d.Diff(ctx, oldDAG, newDAG)
		if err != nil {
			b.Fatal(err)
		}
		_ = patches
	}
}

func BenchmarkCoordinator_Evaluate(b *testing.B) {
	patchReg := patch.NewRegistry()
	exec := &benchExecutor{}
	patchReg.Register("bench", exec) //nolint:errcheck
	coord := coordinator.NewEvolutionCoordinator(coordinator.DefaultPolicy(), patchReg)

	for i := 0; i < 100; i++ {
		coord.Submit(coordinator.PatchProposal{
			Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "bench"},
			Source:   coordinator.SourceGA,
			Priority: 5,
		})
	}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		coord.Evaluate(ctx)
	}
}

func BenchmarkFullEvolutionCycle(b *testing.B) {
	// Build a realistic DAG.
	steps := []*engine.Step{
		{ID: "A", AgentType: "validator"},
		{ID: "B", AgentType: "processor", DependsOn: []string{"A"}},
		{ID: "C", AgentType: "formatter", DependsOn: []string{"B"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		b.Fatal(err)
	}

	// Build genomes.
	wfGenome := genome.NewWorkflowGenome(dag, genome.DefaultWorkflowGenomeConfig())
	knowledgeGenome := genome.NewKnowledgeGenome(nil, genome.DefaultKnowledgeGenomeConfig())
	recoveryGenome := genome.NewRecoveryGenome(
		&engine.RecoveryPolicy{Strategy: engine.RecoveryRetry, MaxAttempts: 3},
		genome.DefaultRecoveryGenomeConfig(),
	)

	genomes := []genome.Genome{wfGenome, knowledgeGenome, recoveryGenome}
	diffReg := diff.NewRegistry()
	for _, d := range []diff.Differ{
		diff.NewWorkflowDiffer(),
		diff.NewKnowledgeDiffer(), diff.NewRecoveryDiffer(),
	} {
		diffReg.Register(d) //nolint:errcheck
	}

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, g := range genomes {
			oldSnap, _ := g.Snapshot(ctx)

			children, mErr := g.Mutate(ctx, 3)
			if mErr != nil || len(children) == 0 {
				continue
			}

			best := children[0]
			newSnap, _ := best.Snapshot(ctx)

			patches, dErr := diffReg.DiffAll(ctx, map[string]diff.SnapshotPair{
				g.Name(): {Old: oldSnap, New: newSnap},
			})
			if dErr != nil {
				continue
			}
			_ = patches
		}
	}
}

// ── Helper ──────────────────────────────────

type benchExecutor struct{}

func (e *benchExecutor) Apply(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	return &patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: p.Target}, nil
}
func (e *benchExecutor) CanApply(_ context.Context, _ patch.RuntimePatch) error { return nil }
