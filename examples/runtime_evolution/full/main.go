// Runtime Evolution Demo (full) — the runtime evolution chain over three
// genomes (workflow, knowledge, recovery; the scheduler dimension is retired
// per fusion plan §B1), real executors,
// and a full evolution cycle per subsystem.
//
// Purpose:
//
//	This is the middle tier of the runtime evolution examples. Unlike basic/
//	(two genomes, one loop), it registers every evolvable subsystem —
//	workflow, scheduler, knowledge, and recovery — each with its own genome,
//	differ, and patch executor, runs a mutation→fitness→snapshot→diff cycle
//	per genome, and submits all patches to the coordinator. It is the closest
//	thing to a production evolution loop.
//
// Learning objectives:
//   - How to register multiple genomes / differs / executors and how targets
//     map to executors (workflow.graph → GraphPatchExecutor, etc.).
//   - How a per-genome evolution cycle (Mutate → Fitness → Snapshot → Diff)
//     generalizes the basic example.
//   - How the coordinator evaluates and applies all patches and exposes the
//     apply + decision history as the audit trail.
//
// Core APIs (with package paths):
//   - genome.NewWorkflowGenome / NewSchedulerGenome / NewKnowledgeGenome /
//     NewRecoveryGenome (internal/evolution/genome)
//   - diff.NewWorkflowDiffer / NewKnowledgeDiffer /
//     NewRecoveryDiffer (internal/evolution/diff)
//   - patch.Registry with GraphPatchExecutor / RecoveryPatchExecutor /
//     KnowledgePatchExecutor (internal/evolution/patch + workflow/graph +
//     knowledge/runtime)
//   - coordinator.NewEvolutionCoordinator (internal/evolution/coordinator)
//
// Run:
//
//	go run ./examples/runtime_evolution/full
//
// Expected output:
//
//  1. DAG: 3 nodes, order=[A B C] → 2-4. registrations → 5-7.
//  6. Evolution cycle produced N genome changes → 7-8. submitted/applied
//     ═══ Summary ═══  Genomes/Differs/Patches proposed/applied/failed
//
// Place in the progression: basic/ (shallow) → full/ (this, complete) →
// knowledge/ (deep, knowledge-specific parameter evolution).
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	knowledgeruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
	"github.com/Timwood0x10/ares/internal/workflow/graph"
)

func main() {
	ctx := context.Background()
	fmt.Println("═══ ARES Runtime Evolution — Full Chain Demo ═══")
	fmt.Println()

	// ── Step 1: Build the initial workflow DAG ──
	// A 3-node pipeline (A → B → C) is the evolvable substrate: the workflow
	// genome mutates this topology.
	steps := []*engine.Step{
		{ID: "A", Name: "Input", AgentType: "validator", Input: "validate"},
		{ID: "B", Name: "Process", AgentType: "processor", Input: "process", DependsOn: []string{"A"}},
		{ID: "C", Name: "Output", AgentType: "formatter", Input: "format", DependsOn: []string{"B"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		log.Fatalf("create DAG: %v", err)
	}
	order, _ := dag.GetExecutionOrder()
	fmt.Printf("1. DAG: %d nodes, order=%v\n", dag.NodeCount(), order)

	// ── Step 2: Build a graph.Graph for the real GraphPatchExecutor ──
	// The graph executor mutates a live graph; mirroring the DAG into a
	// graph.Graph gives it something real to apply patches to.
	g, err := graph.NewGraph("full-demo")
	if err != nil {
		log.Fatalf("create graph: %v", err)
	}
	for _, step := range steps {
		fn, fErr := graph.NewFuncNode(step.ID, func(_ context.Context, _ *graph.State) error { return nil })
		if fErr != nil {
			log.Fatalf("func node %s: %v", step.ID, fErr)
		}
		_, _ = g.Node(step.ID, fn)
	}
	for _, step := range steps {
		for _, dep := range step.DependsOn {
			_, _ = g.Edge(dep, step.ID)
		}
	}
	_, _ = g.Start("A")

	// ── Step 3: Register ALL genomes ──
	// Workflow mutates the DAG topology, knowledge the knowledge-retrieval
	// parameters, recovery the failure handling policy. (The scheduler
	// dimension is retired — fusion plan §B1.)
	genomeReg := genome.NewRegistry()
	mustRegisterGenome(genomeReg, genome.NewWorkflowGenome(dag, genome.DefaultWorkflowGenomeConfig()))
	mustRegisterGenome(genomeReg, genome.NewKnowledgeGenome(nil, genome.DefaultKnowledgeGenomeConfig()))
	mustRegisterGenome(genomeReg, genome.NewRecoveryGenome(
		&engine.RecoveryPolicy{Strategy: engine.RecoveryRetry, MaxAttempts: 3},
		genome.DefaultRecoveryGenomeConfig(),
	))
	fmt.Printf("2. Registered genomes: %v\n", genomeReg.List())

	// ── Step 4: Register ALL differs ──
	// Each differ knows how to turn an old/new snapshot pair of its subsystem
	// into concrete runtime patches.
	diffReg := diff.NewRegistry()
	mustRegisterDiffer(diffReg, diff.NewWorkflowDiffer())
	mustRegisterDiffer(diffReg, diff.NewKnowledgeDiffer())
	mustRegisterDiffer(diffReg, diff.NewRecoveryDiffer())
	fmt.Printf("3. Registered differs: %v\n", diffReg.List())

	// ── Step 5: Register ALL executors ──
	// Executors are the apply-side of patches, keyed by target name:
	// graph targets → GraphPatchExecutor, recovery targets →
	// RecoveryPatchExecutor, knowledge targets → KnowledgePatchExecutor.
	patchReg := patch.NewRegistry()

	// Graph executor: handles insert/remove/replace/add_edge/remove_edge on any node ID.
	graphExec := graph.NewGraphPatchExecutor(g)
	// Register for common patch targets.
	for _, target := range []string{"workflow.graph", "A", "B", "C",
		"B-parallel", "A-parallel", "C-parallel",
		"wf-mut-0", "wf-mut-1", "wf-mut-2", "wf-mut-3", "wf-mut-4"} {
		_ = patchReg.Register(target, graphExec)
	}

	// Scheduler executor: reuses GraphPatchExecutor for ChangeScheduler.
	_ = patchReg.Register("graph.scheduler", graphExec)

	// Recovery executor: handles all recovery-related patches.
	recoveryExec := engine.NewRecoveryPatchExecutor(dag)
	_ = patchReg.Register("recovery.strategy", recoveryExec)
	_ = patchReg.Register("recovery.max_attempts", recoveryExec)
	_ = patchReg.Register("recovery.replacement_agent", recoveryExec)
	_ = patchReg.Register("recovery.max_retries", recoveryExec)

	// Knowledge executor: handles knowledge/planner patches.
	// Build a minimal knowledge runtime for the executor.
	knowPipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 10240}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 200}},
	)
	knowDiscovery := planner.NewSourceDiscovery(provider.NewProviderRegistry(), planner.NewQueryPlanner())
	knowRt := knowledgeruntime.New(
		planner.NewKnowledgePlanner(),
		knowDiscovery,
		provider.NewProviderRegistry(),
		knowPipe,
		[]knowledgeruntime.Linker{&knowledgeruntime.DefaultLinker{}},
		[]knowledgeruntime.Reducer{&knowledgeruntime.DefaultReducer{}},
	)
	knowledgeExec := knowledgeruntime.NewKnowledgePatchExecutor(knowRt)
	_ = patchReg.Register("knowledge.planner", knowledgeExec)
	_ = patchReg.Register("knowledge.planner.max_results", knowledgeExec)
	_ = patchReg.Register("knowledge.planner.reducer", knowledgeExec)
	_ = patchReg.Register("knowledge.planner.strategy", knowledgeExec)
	_ = patchReg.Register("knowledge.planner.summarizer", knowledgeExec)

	fmt.Println("4. Registered executors: Graph + Scheduler + Recovery + Knowledge")

	// ── Step 6: Create the coordinator ──
	// The coordinator evaluates submitted proposals against its policy and
	// applies approved ones through the matching executor.
	coord := coordinator.NewEvolutionCoordinator(coordinator.DefaultPolicy(), patchReg)

	// ── Step 7: Take snapshots of every genome ──
	// Snapshots capture each subsystem's current state; the old snapshot is
	// the diff baseline.
	snapshots := make(map[string]diff.SnapshotPair)
	for _, name := range genomeReg.List() {
		gm, _ := genomeReg.Get(name)
		snap, _ := gm.Snapshot(ctx)
		snapshots[name] = diff.SnapshotPair{Old: snap}
	}
	fmt.Println("5. Snapshots taken")

	// ── Step 8: Run one evolution cycle for each genome ──
	// For every subsystem: Mutate children → pick fittest (FitnessGenome) →
	// snapshot the child → diff old vs new into patches.
	type evolutionResult struct {
		genomeName string
		patches    []patch.RuntimePatch
	}

	var allResults []evolutionResult

	for _, name := range genomeReg.List() {
		gm, _ := genomeReg.Get(name)

		// Mutate.
		children, err := gm.Mutate(ctx, 3)
		if err != nil {
			log.Printf("  %s: mutate failed: %v", name, err)
			continue
		}

		// Pick best by fitness.
		var best genome.Genome
		var bestFit float64
		for _, child := range children {
			f, ok := child.(genome.FitnessGenome)
			if !ok {
				continue
			}
			fit, _ := f.Fitness(ctx)
			if fit > bestFit {
				bestFit = fit
				best = child
			}
		}
		if best == nil {
			continue
		}

		// Snapshot the best child.
		newSnap, _ := best.Snapshot(ctx)

		// Diff.
		pair := snapshots[name]
		pair.New = newSnap
		patches, err := diffReg.DiffAll(ctx, map[string]diff.SnapshotPair{name: pair})
		if err != nil {
			log.Printf("  %s: diff failed: %v", name, err)
			continue
		}

		if len(patches) > 0 {
			allResults = append(allResults, evolutionResult{
				genomeName: name,
				patches:    patches,
			})
		}
	}

	fmt.Printf("6. Evolution cycle produced %d genome changes:\n", len(allResults))
	for _, res := range allResults {
		fmt.Printf("   %s: %d patches\n", res.genomeName, len(res.patches))
		for _, p := range res.patches {
			fmt.Printf("       • %s on %s\n", p.Type, p.Target)
		}
	}

	// ── Step 9: Submit all patches to the coordinator ──
	// Every patch becomes a GA-source proposal the coordinator will evaluate.
	var totalPatches int
	for _, res := range allResults {
		for _, p := range res.patches {
			coord.Submit(coordinator.PatchProposal{
				Patch:     p,
				Source:    coordinator.SourceGA,
				Reason:    fmt.Sprintf("evolution: %s improved", res.genomeName),
				Priority:  5,
				Timestamp: time.Now(),
			})
			totalPatches++
		}
	}
	fmt.Printf("7. Submitted %d patches to coordinator\n", totalPatches)

	// ── Step 10: Evaluate and apply ──
	// Evaluate runs the decision policy over all proposals and applies the
	// approved ones through their registered executors.
	coord.Evaluate(ctx)
	history := coord.PatchHistory()
	fmt.Printf("8. Applied %d patches:\n", len(history))
	var okCount, failCount int
	for _, r := range history {
		if r.Error != nil {
			failCount++
			fmt.Printf("   ❌ %s → %v\n", r.Proposal.Patch.Type, r.Error)
		} else {
			okCount++
			fmt.Printf("   ✅ %s → OK\n", r.Proposal.Patch.Type)
		}
	}

	// ── Step 11: Summary ──
	// The audit trail: how many subsystems, differs, and patches flowed
	// through the loop, and how many applied/failed.
	fmt.Println()
	fmt.Println("═══ Summary ═══")
	fmt.Printf("Genomes registered: %d\n", len(genomeReg.List()))
	fmt.Printf("Differs registered: %d\n", len(diffReg.List()))
	fmt.Printf("Patches proposed:  %d\n", totalPatches)
	fmt.Printf("Patches applied:   %d ✅\n", okCount)
	fmt.Printf("Patches failed:    %d ❌\n", failCount)
	fmt.Println()
	fmt.Println("═══ Done ═══")
}

// mustRegisterGenome registers a genome and panics on failure (fatal during
// setup, which code_rules allows for initialization errors).
func mustRegisterGenome(r *genome.Registry, g genome.Genome) {
	if err := r.Register(g); err != nil {
		panic(fmt.Sprintf("register genome: %v", err))
	}
}

// mustRegisterDiffer registers a differ and panics on failure.
func mustRegisterDiffer(r *diff.Registry, d diff.Differ) {
	if err := r.Register(d); err != nil {
		panic(fmt.Sprintf("register differ: %v", err))
	}
}
