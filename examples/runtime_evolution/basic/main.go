// Runtime Evolution Demo (basic) — the shallowest of the three runtime
// evolution example: a WorkflowGenome evolves through
// mutate → fitness → diff → coordinator, showing the minimal closed loop.
//
// Purpose:
//
//	This example teaches the CORE evolution loop with only two genomes:
//	the DAG topology. It mutates candidates, picks the
//	fittest, diffs old vs new snapshots into patches, and submits them to the
//	evolution coordinator for evaluation — the same loop the full example
//	generalizes to four subsystems.
//
// Learning objectives:
//   - How a Genome exposes Mutate + Snapshot and how a
//     FitnessGenome scores candidates.
//   - How the Diff Engine turns snapshot pairs into patch.RuntimePatch.
//   - How the coordinator evaluates and applies patches (SourceGA).
//
// Core APIs (with package paths):
//   - genome.NewWorkflowGenome / Registry
//     (internal/evolution/genome)
//   - diff.NewWorkflowDiffer / Registry / DiffAll
//     (internal/evolution/diff)
//   - patch.NewRegistry / RuntimePatch (internal/evolution/patch)
//   - coordinator.NewEvolutionCoordinator / Submit / Evaluate / PatchHistory /
//     DecisionHistory (internal/evolution/coordinator)
//   - engine.NewMutableDAG (internal/workflow/engine)
//   - graph.NewGraphPatchExecutor (internal/workflow/graph)
//
// Run:
//
//	go run ./examples/runtime_evolution/basic
//
// Expected output:
//
//  1. Initial DAG: 3 nodes, order=[A B C]
//  2. Registered genomes: [workflow] → 3. differs → ...
//  6. Generated N candidate workflow genomes ... → 7. patches → 8-10.
//
// Place in the progression: this is the SHALLOWEST step (basic); see full/
// for all four subsystems and knowledge/ for knowledge-specific evolution.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
	"github.com/Timwood0x10/ares/internal/workflow/graph"
)

func main() {
	ctx := context.Background()
	fmt.Println("═══ ARES Runtime Evolution Full Demo ═══")
	fmt.Println()

	// ── Step 1: Build the initial workflow DAG ──
	// A 3-node pipeline (A → B → C) is the evolvable substrate: the workflow
	// genome mutates this topology.
	dag := buildDAG()
	genomeReg, diffReg, patchReg := registerComponents(dag)
	coord := coordinator.NewEvolutionCoordinator(coordinator.DefaultPolicy(), patchReg)
	fmt.Println("5. Created coordinator")

	// ── Step 2: Run one evolution cycle and apply the patches ──
	patches := runEvolutionCycle(ctx, dag, genomeReg, diffReg)
	applyPatches(coord, patches)
	printSummary(coord)
}

// buildDAG constructs the initial 3-node workflow DAG (validator → processor
// → formatter) that the workflow genome will mutate.
func buildDAG() *engine.MutableDAG {
	steps := []*engine.Step{
		{ID: "A", Name: "Input Validator", AgentType: "validator", Input: "validate request"},
		{ID: "B", Name: "Business Logic", AgentType: "processor", Input: "process", DependsOn: []string{"A"}},
		{ID: "C", Name: "Output Formatter", AgentType: "formatter", Input: "format", DependsOn: []string{"B"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		log.Fatalf("create DAG: %v", err)
	}
	order, err := dag.GetExecutionOrder()
	if err != nil {
		log.Fatalf("get execution order: %v", err)
	}
	fmt.Printf("1. Initial DAG: %d nodes, order=%v\n", dag.NodeCount(), order)
	return dag
}

// registerComponents registers the genomes (workflow), the differs
// that turn snapshot pairs into patches, and the patch executors that apply
// them to a live graph.
func registerComponents(dag *engine.MutableDAG) (*genome.Registry, *diff.Registry, *patch.Registry) {
	// ── Register genomes ──
	// A genome knows how to mutate its subsystem and snapshot its state;
	// TODO(evolution-dim): the scheduler dimension was retired (fusion §B1);
	// a future concurrency genome may evolve sdk.Graph.MaxRoundConcurrency.
	// The workflow genome mutates the DAG topology;
	// node scheduling.
	genomeReg := genome.NewRegistry()
	wfGenome := genome.NewWorkflowGenome(dag, genome.DefaultWorkflowGenomeConfig())
	if err := genomeReg.Register(wfGenome); err != nil {
		log.Fatalf("register workflow genome: %v", err)
	}
	fmt.Printf("2. Registered genomes: %v\n", genomeReg.List())

	// ── Register differs ──
	// A differ compares an old/new snapshot pair and produces runtime patches
	// describing the concrete changes.
	diffReg := diff.NewRegistry()
	if err := diffReg.Register(diff.NewWorkflowDiffer()); err != nil {
		log.Fatalf("register workflow differ: %v", err)
	}
	fmt.Printf("3. Registered differ: %v\n", diffReg.List())

	// ── Register patch executors ──
	// Executors are the apply-side of a patch: the graph executor mutates the
	// live graph for both workflow.graph and graph.scheduler targets.
	evolvedGraph := makeGraphFromDAG(dag)

	patchReg := patch.NewRegistry()
	graphExec := graph.NewGraphPatchExecutor(evolvedGraph)
	if err := patchReg.Register("workflow.graph", graphExec); err != nil {
		log.Fatalf("register graph executor: %v", err)
	}
	if err := patchReg.Register("graph.scheduler", graphExec); err != nil {
		log.Fatalf("register scheduler executor: %v", err)
	}
	return genomeReg, diffReg, patchReg
}

// makeGraphFromDAG mirrors the MutableDAG into a graph.Graph so the graph
// patch executor can apply mutations to a live, executable graph.
func makeGraphFromDAG(dag *engine.MutableDAG) *graph.Graph {
	evolvedGraph, err := graph.NewGraph("demo-evolution")
	if err != nil {
		log.Fatalf("create evolution graph: %v", err)
	}
	for _, step := range dag.Steps() {
		fn, fErr := graph.NewFuncNode(step.ID, func(_ context.Context, _ *graph.State) error { return nil })
		if fErr != nil {
			log.Fatalf("create func node %s: %v", step.ID, fErr)
		}
		if _, fErr = evolvedGraph.Node(step.ID, fn); fErr != nil {
			log.Fatalf("add node %s: %v", step.ID, fErr)
		}
		for _, dep := range step.DependsOn {
			if _, eErr := evolvedGraph.Edge(dep, step.ID); eErr != nil {
				log.Fatalf("add edge %s→%s: %v", dep, step.ID, eErr)
			}
		}
	}
	if _, err = evolvedGraph.Start("A"); err != nil {
		log.Fatalf("set start node: %v", err)
	}
	return evolvedGraph
}

// runEvolutionCycle executes the mutate → fitness → snapshot → diff loop and
// returns the patches describing the evolved state.
func runEvolutionCycle(
	ctx context.Context,
	dag *engine.MutableDAG,
	genomeReg *genome.Registry,
	diffReg *diff.Registry,
) []patch.RuntimePatch {
	wfGenome, err := genomeReg.Get("workflow")
	if err != nil {
		log.Fatalf("get workflow genome: %v", err)
	}
	schedGenome, err := genomeReg.Get("scheduler")
	if err != nil {
		log.Fatalf("get scheduler genome: %v", err)
	}

	// ── Snapshot the initial (old) state ──
	oldSnapshot, err := wfGenome.Snapshot(ctx)
	if err != nil {
		log.Fatalf("snapshot: %v", err)
	}
	oldSchedSnapshot, err := schedGenome.Snapshot(ctx)
	if err != nil {
		log.Fatalf("scheduler snapshot: %v", err)
	}

	// ── Mutate the workflow genome until a real change appears ──
	// Mutation is probabilistic; we retry up to 10 times to get a child whose
	// node count differs from the parent (a genuine topology change).
	var children []genome.Genome
	for attempt := 0; attempt < 10; attempt++ {
		children, err = wfGenome.Mutate(ctx, 5)
		if err != nil {
			log.Fatalf("mutate: %v", err)
		}
		for _, child := range children {
			cs, _ := child.Snapshot(ctx)
			cDAG := cs.(*engine.DAG)
			if len(cDAG.Nodes) != len(oldSnapshot.(*engine.DAG).Nodes) {
				goto foundMutation
			}
		}
	}
	log.Fatalf("mutate: failed to produce a change after 10 attempts")
foundMutation:
	fmt.Printf("6. Generated %d candidate workflow genomes (after mutation)\n", len(children))

	// ── Pick the fittest candidate ──
	var bestChild genome.Genome
	var bestFit float64
	for _, child := range children {
		f, ok := child.(genome.FitnessGenome)
		if !ok {
			continue
		}
		fit, _ := f.Fitness(ctx)
		fmt.Printf("   Candidate %q fitness: %.2f\n", child.Name(), fit)
		if fit > bestFit {
			bestFit = fit
			bestChild = child
		}
	}

	// ── Diff old vs new snapshots into patches ──
	newSnapshot, err := bestChild.Snapshot(ctx)
	if err != nil {
		log.Fatalf("new snapshot: %v", err)
	}
	schedChildren, _ := schedGenome.Mutate(ctx, 1)
	bestSched := schedChildren[0]
	newSchedSnapshot, _ := bestSched.Snapshot(ctx)

	snapshots := map[string]diff.SnapshotPair{
		"workflow":  {Old: oldSnapshot, New: newSnapshot},
		"scheduler": {Old: oldSchedSnapshot, New: newSchedSnapshot},
	}

	patches, err := diffReg.DiffAll(ctx, snapshots)
	if err != nil {
		log.Fatalf("diff all: %v", err)
	}
	fmt.Printf("7. Diff Engine produced %d patches:\n", len(patches))
	for _, p := range patches {
		fmt.Printf("   • %s on %s (value: %v)\n", p.Type, p.Target, p.Value)
	}
	return patches
}

// applyPatches submits every patch to the coordinator as a GA-source proposal
// with a fitness score.
func applyPatches(coord *coordinator.EvolutionCoordinator, patches []patch.RuntimePatch) {
	for _, p := range patches {
		coord.Submit(coordinator.PatchProposal{
			Patch:     p,
			Source:    coordinator.SourceGA,
			Reason:    "GA evaluation: fitness improved",
			Priority:  5,
			Fitness:   85.0, // Example fitness score (0-100 scale)
			Timestamp: time.Now(),
		})
	}
	fmt.Printf("8. Submitted %d patch proposals to coordinator (with fitness scores)\n", len(patches))
}

// printSummary evaluates the coordinator and prints the apply + decision
// history — the audit trail of the evolution loop.
func printSummary(coord *coordinator.EvolutionCoordinator) {
	coord.Evaluate(context.Background())
	history := coord.PatchHistory()
	fmt.Printf("9. Applied %d patches\n", len(history))
	for _, r := range history {
		status := "OK"
		if r.Error != nil {
			status = fmt.Sprintf("ERROR: %v", r.Error)
		}
		fmt.Printf("   • %s from %s → %s", r.Proposal.Patch.Type, r.Proposal.Source, status)
		if r.Proposal.Fitness > 0 {
			fmt.Printf(" (fitness: %.1f)", r.Proposal.Fitness)
		}
		fmt.Println()
	}

	decisions := coord.DecisionHistory()
	fmt.Printf("10. Decision history: %d total\n", len(decisions))
	for _, d := range decisions {
		fmt.Printf("   • %s: %s\n", d.Decision, d.Reason)
	}

	fmt.Println()
	fmt.Println("═══ Demo Complete ═══")
}
