package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

var evolutionCmd = &cobra.Command{
	Use:   "evolution",
	Short: "Runtime evolution system commands",
	Long:  `Manage the runtime evolution system: inspect genomes, run evolution cycles, view coordinator decisions.`,
}

var evolutionRunCmd = &cobra.Command{
	Use:   "run [flags]",
	Short: "Run one evolution cycle on all registered genomes",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEvolutionCycle()
	},
}

var evolutionStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show evolution system status (genomes, differs, coordinator)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showEvolutionStatus()
	},
}

func init() {
	rootCmd.AddCommand(evolutionCmd)
	evolutionCmd.AddCommand(evolutionRunCmd)
	evolutionCmd.AddCommand(evolutionStatusCmd)
}

func getNewEvolution() (*ares_bootstrap.NewEvolutionComponents, error) {
	// Components are cached so the expensive ProvideNewEvolution runs once.
	if cachedComponents == nil {
		// Create a minimal MutableDAG so workflow/scheduler/recovery genomes
		// and their executors are properly registered (not nil).
		dag, err := engine.NewMutableDAG(nil)
		if err != nil {
			return nil, fmt.Errorf("create mutable dag: %w", err)
		}
		comp, err := ares_bootstrap.ProvideNewEvolution(dag, nil, memory.NewMinimalMemoryManager(), nil)
		if err != nil {
			return nil, fmt.Errorf("bootstrap evolution: %w", err)
		}
		cachedComponents = comp
	}
	return cachedComponents, nil
}

var cachedComponents *ares_bootstrap.NewEvolutionComponents

func runEvolutionCycle() error {
	ctx := context.Background()
	ev, err := getNewEvolution()
	if err != nil {
		return err
	}

	fmt.Println("═══ Evolution Cycle ═══")
	fmt.Println()

	// 1. Take snapshots.
	snapshots := make(map[string]diff.SnapshotPair)
	for _, name := range ev.GenomeReg.List() {
		gm, err := ev.GenomeReg.Get(name)
		if err != nil {
			return fmt.Errorf("get genome %s: %w", name, err)
		}
		oldSnap, err := gm.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", name, err)
		}
		snapshots[name] = diff.SnapshotPair{Old: oldSnap}
	}

	// 2. Mutate each genome.
	type result struct {
		name    string
		patches []patch.RuntimePatch
	}
	var results []result

	for _, name := range ev.GenomeReg.List() {
		gm, err := ev.GenomeReg.Get(name)
		if err != nil {
			continue
		}

		children, err := gm.Mutate(ctx, evFlags.mutationCount)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: mutate failed: %v\n", name, err)
			continue
		}

		// Pick best by fitness.
		var bestGenome interface{ Name() string }
		var bestFit float64
		for _, child := range children {
			f, ok := child.(genome.FitnessGenome)
			if !ok {
				continue
			}
			fit, fErr := f.Fitness(ctx)
			if fErr != nil {
				continue
			}
			if fit > bestFit {
				bestFit = fit
				bestGenome = child
			}
		}
		if bestGenome == nil {
			continue
		}

		// Snapshot the best child and diff.
		if child, ok := bestGenome.(interface {
			Snapshot(context.Context) (any, error)
		}); ok {
			newSnap, sErr := child.Snapshot(ctx)
			if sErr != nil {
				continue
			}
			pair := snapshots[name]
			pair.New = newSnap
			patches, dErr := ev.DiffReg.DiffAll(ctx, map[string]diff.SnapshotPair{name: pair})
			if dErr != nil {
				continue
			}
			if len(patches) > 0 {
				results = append(results, result{name: name, patches: patches})
			}
		}
	}

	fmt.Printf("Genomes evaluated: %d\n", len(ev.GenomeReg.List()))
	fmt.Printf("Genomes changed:  %d\n", len(results))
	var total int
	for _, r := range results {
		fmt.Printf("  %s: %d patches\n", r.name, len(r.patches))
		for _, p := range r.patches {
			fmt.Printf("    %s on %s\n", p.Type, p.Target)
			total++
		}
	}

	// 3. Submit patches to coordinator.
	for _, r := range results {
		for _, p := range r.patches {
			ev.Coordinator.Submit(coordinator.PatchProposal{
				Patch:     p,
				Source:    coordinator.SourceGA,
				Reason:    fmt.Sprintf("evolution cycle: %s improved", r.name),
				Priority:  5,
				Timestamp: time.Now(),
			})
		}
	}
	fmt.Printf("\nSubmitted %d patches to coordinator\n", total)

	// 4. Evaluate and apply.
	ev.Coordinator.Evaluate(ctx)
	history := ev.Coordinator.PatchHistory()
	var okCount, failCount int
	for _, r := range history {
		if r.Error != nil {
			failCount++
		} else {
			okCount++
		}
	}
	fmt.Printf("Applied: %d OK, %d Failed\n", okCount, failCount)

	return nil
}

func showEvolutionStatus() error {
	ev, err := getNewEvolution()
	if err != nil {
		return err
	}

	fmt.Println("═══ Evolution System Status ═══")
	fmt.Println()

	// Genomes.
	fmt.Printf("Genomes (%d):\n", len(ev.GenomeReg.List()))
	for _, name := range ev.GenomeReg.List() {
		fmt.Printf("  - %s\n", name)
	}
	fmt.Println()

	// Differs.
	fmt.Printf("Differs (%d):\n", len(ev.DiffReg.List()))
	for _, name := range ev.DiffReg.List() {
		fmt.Printf("  - %s\n", name)
	}
	fmt.Println()

	// Coordinator.
	decisions := ev.Coordinator.DecisionHistory()
	history := ev.Coordinator.PatchHistory()
	fmt.Printf("Coordinator:\n")
	fmt.Printf("  Pending proposals: %d\n", ev.Coordinator.PendingCount())
	fmt.Printf("  Decisions made:    %d\n", len(decisions))
	fmt.Printf("  Patches applied:   %d\n", len(history))

	// Evidence store.
	evs, _ := ev.EvidenceStore.Query(context.Background(), evidence.Filter{Limit: 1000})
	fmt.Printf("\nEvidence store entries: %d\n", len(evs))

	// C5.1: lifecycle + compile attribution chain.
	if ev.Lifecycle != nil {
		snap := ev.Lifecycle.Snapshot()
		fmt.Println()
		fmt.Println("── Attribution Chain ──")
		fmt.Printf("  Generation:     %d\n", snap.Generation)
		fmt.Printf("  State:          %s\n", snap.State)
		fmt.Printf("  Active ID:      %s\n", snap.ActiveID)
		fmt.Printf("  Window score:   %.4f (%d samples)\n", snap.WindowScore, snap.WindowCount)
		fmt.Printf("  Rollback armed:  %v\n", snap.RollbackArmed)
		if len(snap.Gates) > 0 {
			fmt.Printf("  Gates:          %v\n", snap.Gates)
		}
		if snap.ShadowGateSkipReason != "" {
			fmt.Printf("  Shadow skip:    %s\n", snap.ShadowGateSkipReason)
		}
		if snap.LastDecision != "" {
			fmt.Printf("  Last decision:  %s\n", snap.LastDecision)
		}
		// C5.2: compile provenance — the triplet (generation, gates,
		// compile_id) answers "which generation, which gate, which
		// compile" in a single command.
		if snap.CompileID != "" {
			fmt.Printf("  Compile ID:     %s\n", snap.CompileID)
			fmt.Printf("  DAG version:    %d\n", snap.DAGVersion)
		}
		fmt.Printf("  Compile count:  %d\n", snap.CompileCount)
	}

	return nil
}

// ── Flags ───────────────────────────────────

var evFlags struct {
	mutationCount int
}

func init() {
	evolutionRunCmd.Flags().IntVarP(&evFlags.mutationCount, "mutations", "m", 3, "number of mutations per genome")
}
