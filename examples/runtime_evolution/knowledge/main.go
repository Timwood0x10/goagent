// Runtime Evolution Demo (knowledge) — the deepest of the three runtime
// evolution examples: it evolves the KNOWLEDGE subsystem's parameters
// (MaxResults, ReducerStrategy, PlannerStrategy) through the KnowledgeGenome,
// diffs the config drift into patches, and applies them via the real
// KnowledgePatchExecutor through the coordinator.
//
// Purpose:
//
//	While basic/ and full/ focus on workflow/scheduler topologies, this
//	example shows subsystem-specific parameter evolution: the KnowledgeGenome
//	mutates the knowledge-retrieval configuration, the KnowledgeDiffer turns
//	the old/new config into patches, and KnowledgePatchExecutor applies them.
//	It is the template for evolving any parameterized subsystem.
//
// Learning objectives:
//   - How KnowledgeGenome mutates knowledge config (MaxResults / Reducer /
//     Planner strategy) and how FitnessGenome ranks candidates.
//   - How KnowledgeDiffer produces patches for knowledge.planner.* targets.
//   - How a SourceAKF proposal is evaluated and applied by the coordinator.
//
// Core APIs (with package paths):
//   - genome.NewKnowledgeGenome / DefaultKnowledgeGenomeConfig
//     (internal/evolution/genome)
//   - diff.NewKnowledgeDiffer (internal/evolution/diff)
//   - knowledgeruntime.NewKnowledgePatchExecutor (internal/knowledge/runtime)
//   - coordinator.NewEvolutionCoordinator / Submit / Evaluate / PatchHistory
//     (internal/evolution/coordinator)
//
// Run:
//
//	go run ./examples/runtime_evolution/knowledge
//
// Expected output:
//
//  1. Created KnowledgeRuntime → 2. Initial config → 3. N candidates
//  4. Diff Engine produced N patches → 5. Submitted → 6. Applied
//     ═══ Demo Complete ═══
//
// Place in the progression: basic/ (shallow) → full/ (complete) →
// knowledge/ (this, deep, knowledge-specific evolution).
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	knowledgeruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func main() {
	ctx := context.Background()
	fmt.Println("═══ Knowledge Evolution Demo ═══")
	fmt.Println()

	// ── Step 1: Create a minimal KnowledgeRuntime ──
	// The runtime backs the KnowledgePatchExecutor; in production it would be
	// fully configured with real providers, here it is a minimal demo pipe.
	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 10240}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 200}},
	)
	discovery := planner.NewSourceDiscovery(provider.NewProviderRegistry(), planner.NewQueryPlanner())
	rt := knowledgeruntime.New(
		planner.NewKnowledgePlanner(),
		discovery,
		provider.NewProviderRegistry(),
		pipe,
		[]knowledgeruntime.Linker{&knowledgeruntime.DefaultLinker{}},
		[]knowledgeruntime.Reducer{&knowledgeruntime.DefaultReducer{}},
	)
	_ = rt // used by KnowledgePatchExecutor

	fmt.Println("1. Created KnowledgeRuntime")

	// ── Step 2: Create the patch registry with the real knowledge executor ──
	// KnowledgePatchExecutor applies patches targeting the knowledge.planner.*
	// config keys; registering it makes those targets applicable.
	patchReg := patch.NewRegistry()
	knowledgeExec := knowledgeruntime.NewKnowledgePatchExecutor(rt)
	if err := patchReg.Register("knowledge.planner", knowledgeExec); err != nil {
		log.Fatalf("register knowledge executor: %v", err)
	}
	if err := patchReg.Register("knowledge.planner.max_results", knowledgeExec); err != nil {
		log.Fatalf("register max_results executor: %v", err)
	}
	if err := patchReg.Register("knowledge.planner.reducer", knowledgeExec); err != nil {
		log.Fatalf("register reducer executor: %v", err)
	}
	if err := patchReg.Register("knowledge.planner.strategy", knowledgeExec); err != nil {
		log.Fatalf("register strategy executor: %v", err)
	}

	// ── Step 3: Create the KnowledgeGenome with a starting config ──
	// The genome mutates these three parameters; starting values are the
	// "stable" config before evolution.
	kgCfg := genome.DefaultKnowledgeGenomeConfig()
	kgCfg.MaxResults = 100
	kgCfg.ReducerStrategy = "default"
	kgCfg.PlannerStrategy = "balanced"

	wfGenome := genome.NewKnowledgeGenome(nil, kgCfg)

	// ── Step 4: Take the initial snapshot ──
	// The snapshot captures the current config; it is the OLD side of the
	// diff.
	oldSnap, err := wfGenome.Snapshot(ctx)
	if err != nil {
		log.Fatalf("snapshot: %v", err)
	}
	fmt.Printf("2. Initial config: MaxResults=%d, Reducer=%s, Planner=%s\n",
		kgCfg.MaxResults, kgCfg.ReducerStrategy, kgCfg.PlannerStrategy)

	// ── Step 5: Mutate the knowledge genome ──
	// Mutate produces candidate children with drifted configs.
	children, err := wfGenome.Mutate(ctx, 4)
	if err != nil {
		log.Fatalf("mutate: %v", err)
	}
	fmt.Printf("3. Generated %d candidate knowledge genomes\n", len(children))

	// ── Step 6: Evaluate fitness and pick the best child ──
	// Each candidate's fitness is scored; the fittest becomes the NEW side of
	// the diff.
	var bestChild genome.Genome
	var bestFit float64
	for _, child := range children {
		f, ok := child.(genome.FitnessGenome)
		if !ok {
			continue
		}
		fit, _ := f.Fitness(ctx)
		kgChild := child.(*genome.KnowledgeGenome)
		fmt.Printf("   Candidate fitness=%.2f  MaxResults=%d Reducer=%s Planner=%s\n",
			fit, kgChild.Config().MaxResults, kgChild.Config().ReducerStrategy, kgChild.Config().PlannerStrategy)
		if fit > bestFit {
			bestFit = fit
			bestChild = child
		}
	}

	// ── Step 7: Diff old vs new config into patches ──
	// The KnowledgeDiffer compares the snapshots and emits patches for the
	// changed knowledge.planner.* keys.
	newSnap, err := bestChild.Snapshot(ctx)
	if err != nil {
		log.Fatalf("new snapshot: %v", err)
	}

	diffReg := diff.NewRegistry()
	if err := diffReg.Register(diff.NewKnowledgeDiffer()); err != nil {
		log.Fatalf("register knowledge differ: %v", err)
	}

	patches, err := diffReg.DiffAll(ctx, map[string]diff.SnapshotPair{
		"knowledge": {Old: oldSnap, New: newSnap},
	})
	if err != nil {
		log.Fatalf("diff: %v", err)
	}
	fmt.Printf("4. Diff Engine produced %d patches:\n", len(patches))
	for _, p := range patches {
		fmt.Printf("   • %s on %s (value: %v)\n", p.Type, p.Target, p.Value)
	}

	// ── Step 8: Apply patches through the coordinator ──
	// Patches are submitted as SourceAKF proposals (knowledge-driven config
	// drift) and evaluated by the coordinator's policy; approved ones are
	// applied via KnowledgePatchExecutor.
	coord := coordinator.NewEvolutionCoordinator(coordinator.DefaultPolicy(), patchReg)
	for _, p := range patches {
		coord.Submit(coordinator.PatchProposal{
			Patch:    p,
			Source:   coordinator.SourceAKF,
			Reason:   "knowledge evolution: config drift detected",
			Priority: 6,
		})
	}
	fmt.Printf("5. Submitted %d patch proposals\n", len(patches))

	coord.Evaluate(ctx)
	history := coord.PatchHistory()
	fmt.Printf("6. Applied %d patches:\n", len(history))
	for _, r := range history {
		status := "OK"
		if r.Error != nil {
			status = fmt.Sprintf("ERROR: %v", r.Error)
		}
		fmt.Printf("   • %s → %s\n", r.Proposal.Patch.Type, status)
	}

	fmt.Println()
	fmt.Println("═══ Demo Complete ═══")
}
