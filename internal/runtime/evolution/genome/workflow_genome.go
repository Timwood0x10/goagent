//nolint:gosec // GA mutation intentionally uses math/rand (performance, not crypto).
package genome

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"fmt"
	"math/rand"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

var wfLog = logger.Module("genome.workflow")

// Genome name constants.
const (
	WorkflowGenomeName  = "workflow"
	KnowledgeGenomeName = "knowledge"
	// SchedulerGenomeName is RETIRED (fusion plan §B1, 2026-08-22): the
	// scheduler dimension no longer participates in genome mutation/diff.
	// Kept only as a historical identifier for legacy persisted patches.
	SchedulerGenomeName = "scheduler"
	RecoveryGenomeName  = "recovery"
	defaultAgent        = "default"
)

type wfMutationOp int

const (
	wfOpInsertNode wfMutationOp = iota
	wfOpRemoveNode
	wfOpReplaceNode
	wfOpParallelize
	wfOpSerialize
	wfOpSwap
	wfOpSplit
	wfOpMerge
	wfOpSetMetadata
)

var wfOps = []wfMutationOp{wfOpInsertNode, wfOpRemoveNode, wfOpReplaceNode, wfOpParallelize, wfOpSerialize, wfOpSwap, wfOpSplit, wfOpMerge, wfOpSetMetadata}

// WorkflowGenomeConfig controls the DAG topology evolution behaviour.
type WorkflowGenomeConfig struct {
	// AgentPool is the set of agent types available for inserting new nodes.
	AgentPool []string

	// MaxNodes caps the DAG size to prevent unbounded growth.
	MaxNodes int

	// InsertionRate controls how aggressively new nodes are inserted [0, 1].
	InsertionRate float64

	// PruneRate controls how aggressively low-value nodes are removed [0, 1].
	PruneRate float64

	// EvidenceStore provides execution evidence for fitness evaluation.
	// May be nil; fitness falls back to a constant when nil.
	EvidenceStore evidence.Store
}

// DefaultWorkflowGenomeConfig returns a sensible default configuration.
func DefaultWorkflowGenomeConfig() WorkflowGenomeConfig {
	return WorkflowGenomeConfig{
		AgentPool:     []string{defaultAgent},
		MaxNodes:      20,
		InsertionRate: 0.3,
		PruneRate:     0.2,
	}
}

// WorkflowGenome evolves the DAG execution topology.
// Mutation operators directly correspond to MutableDAG operations:
//
//	InsertNode   → AddNode + AddEdge
//	RemoveNode   → RemoveNode + RemoveEdge
//	ReplaceNode  → ReplaceNode
//	Parallelize  → parallel fan-out conversion
//	Serialize    → linear chain conversion
type WorkflowGenome struct {
	dag    *engine.MutableDAG
	config WorkflowGenomeConfig
}

// NewWorkflowGenome creates a new WorkflowGenome wrapping the given DAG.
func NewWorkflowGenome(dag *engine.MutableDAG, config WorkflowGenomeConfig) *WorkflowGenome {
	return &WorkflowGenome{
		dag:    dag,
		config: config,
	}
}

// SetDAG replaces the genome's DAG reference with a live one. This is
// called after agents are created and their DAGs are registered with the
// runtime manager, so the genome evolves the real workflow topology
// instead of the bootstrap placeholder.
func (g *WorkflowGenome) SetDAG(dag *engine.MutableDAG) {
	if dag == nil {
		return
	}
	g.dag = dag
}

// Name returns the genome identifier.
func (g *WorkflowGenome) Name() string { return WorkflowGenomeName }

// DAG returns the underlying MutableDAG. Used by the Diff Engine to compare snapshots.
func (g *WorkflowGenome) DAG() *engine.MutableDAG { return g.dag }

// Mutate generates n candidate genomes from this parent.
// Each mutation applies one random operator to the DAG topology.
func (g *WorkflowGenome) Mutate(_ context.Context, n int) ([]Genome, error) {
	if n <= 0 {
		return nil, nil
	}

	children := make([]Genome, 0, n)
	for i := 0; i < n; i++ {
		child := g.clone()
		op := wfOps[rand.Intn(len(wfOps))]
		switch op {
		case wfOpInsertNode:
			child.mutateInsertNode()
		case wfOpRemoveNode:
			child.mutateRemoveNode()
		case wfOpReplaceNode:
			child.mutateReplaceNode()
		case wfOpParallelize:
			child.mutateParallelize()
		case wfOpSerialize:
			child.mutateSerialize()
		case wfOpSwap:
			child.mutateSwapNodes()
		case wfOpSplit:
			child.mutateSplitNode()
		case wfOpMerge:
			child.mutateMergeNodes()
		case wfOpSetMetadata:
			child.mutateSetMetadata()
		}
		children = append(children, child)
	}
	return children, nil
}

// Crossover recombines this genome with another to produce a child.
func (g *WorkflowGenome) Crossover(_ context.Context, other Genome) (Genome, error) {
	otherWF, ok := other.(*WorkflowGenome)
	if !ok {
		return nil, fmt.Errorf("workflow: crossover incompatible genome type %T", other)
	}

	child := g.clone()
	otherSteps := otherWF.dag.StepIndex()

	// Uniform crossover: randomly replace nodes with the other parent's version.
	for id, step := range otherSteps {
		if rand.Float64() < 0.5 {
			if child.dag.StepIndex()[id] != nil {
				if err := child.dag.ReplaceNode(context.Background(), id, step); err != nil {
					wfLog.Warn("crossover replace failed", "node", id, "error", err)
				}
			} else if child.dag.NodeCount() < child.config.MaxNodes {
				if err := child.dag.AddNode(context.Background(), step); err != nil {
					wfLog.Warn("crossover add failed", "node", id, "error", err)
				}
			}
		}
	}
	return child, nil
}

// Fitness evaluates this genome's quality based on real workflow execution
// evidence: the measured task success rate (Value in [0, 1]) produced by the
// flight recorder. When no evidence is available yet, a neutral 0.5 is returned
// so the GA keeps exploring. The workflow's own past fitness is not read back,
// so this is not a mathematical fixed point: fitness reflects actual execution
// outcomes under the current DAG topology.
func (g *WorkflowGenome) Fitness(ctx context.Context) (float64, error) {
	score, err := avgFitnessValue(ctx, g.config.EvidenceStore, WorkflowGenomeName, 0, 100)
	if err != nil {
		return 0.5, nil
	}
	return score, nil
}

// Snapshot returns a serializable snapshot of the current DAG state.
// Used by the Diff Engine to compute changes between generations.
func (g *WorkflowGenome) Snapshot(_ context.Context) (any, error) {
	return g.dag.Snapshot(), nil
}

// ── Mutation implementations ─────────────────

func (g *WorkflowGenome) mutateInsertNode() {
	if g.dag.NodeCount() >= g.config.MaxNodes {
		return
	}
	agentType := g.config.AgentPool[rand.Intn(len(g.config.AgentPool))]
	stepID := fmt.Sprintf("wf-mut-%d", g.dag.Version()+1)

	step := &engine.Step{
		ID:        stepID,
		Name:      stepID,
		AgentType: agentType,
		Input:     "auto-evolved",
	}

	// Pick a random existing node as dependency.
	steps := g.dag.Steps()
	if len(steps) > 0 {
		dep := steps[rand.Intn(len(steps))]
		step.DependsOn = []string{dep.ID}
	}

	if err := g.dag.AddNode(context.Background(), step); err != nil {
		wfLog.Warn("insert node mutation failed", "node", stepID, "error", err)
	}
}

func (g *WorkflowGenome) mutateRemoveNode() {
	steps := g.dag.Steps()
	if len(steps) <= 1 {
		return // keep at least one node
	}

	// Find nodes that no other step depends on (true leaf nodes).
	referenced := make(map[string]bool)
	for _, s := range steps {
		for _, dep := range s.DependsOn {
			referenced[dep] = true
		}
	}

	for _, step := range steps {
		if !referenced[step.ID] {
			if err := g.dag.RemoveNode(context.Background(), step.ID); err != nil {
				wfLog.Warn("remove leaf node failed", "node", step.ID, "error", err)
			}
			return
		}
	}

	// Fallback: remove random node.
	target := steps[rand.Intn(len(steps))]
	if err := g.dag.RemoveNode(context.Background(), target.ID); err != nil {
		wfLog.Warn("remove node fallback failed", "node", target.ID, "error", err)
	}
}

func (g *WorkflowGenome) mutateReplaceNode() {
	steps := g.dag.Steps()
	if len(steps) == 0 {
		return
	}
	oldStep := steps[rand.Intn(len(steps))]
	agentType := g.config.AgentPool[rand.Intn(len(g.config.AgentPool))]

	// C4: preserve Metadata through replace – previously the new step was
	// built without Metadata, which meant a metadata-only gene mutation
	// produced ZERO patches (the differ saw only topology changes) and the
	// evolution could never select for a metadata variant.
	var mdCopy map[string]string
	if oldStep.Metadata != nil {
		mdCopy = make(map[string]string, len(oldStep.Metadata))
		for k, v := range oldStep.Metadata {
			mdCopy[k] = v
		}
	}

	newStep := &engine.Step{
		ID:        oldStep.ID,
		Name:      oldStep.Name + "-v2",
		AgentType: agentType,
		Input:     oldStep.Input,
		DependsOn: oldStep.DependsOn,
		Metadata:  mdCopy,
	}
	if err := g.dag.ReplaceNode(context.Background(), oldStep.ID, newStep); err != nil {
		wfLog.Warn("replace node mutation failed", "node", oldStep.ID, "error", err)
	}
}

// mutateSetMetadata mutates a random node's Metadata in place (C4 作动面).
// It twiddles a budget/prior/enabled-style attribute to produce a metadata-only
// diff that WorkflowDiffer now surfaces as a PatchSetNodeMetadata. Without this
// operator, evolution could never explore the metadata dimension — the genome
// had no way to produce a child that differs ONLY by node attributes.
func (g *WorkflowGenome) mutateSetMetadata() {
	steps := g.dag.Steps()
	if len(steps) == 0 {
		return
	}
	step := steps[rand.Intn(len(steps))]

	md := make(map[string]string)
	if step.Metadata != nil {
		for k, v := range step.Metadata {
			md[k] = v
		}
	}

	// Flip one well-known attribute; default to "budget" for a fresh node. The
	// value must be non-empty and changeable so the diff is not a no-op.
	const budgetKey = "budget"
	switch md[budgetKey] {
	case "":
		md[budgetKey] = "10"
	case "10":
		md[budgetKey] = "20"
	default:
		md[budgetKey] = "10"
	}

	if err := g.dag.SetNodeMetadata(step.ID, md); err != nil {
		wfLog.Warn("set-node-metadata mutation failed", "node", step.ID, "error", err)
	}
}

func (g *WorkflowGenome) mutateParallelize() {
	// Pick 3 consecutive nodes A → B → C and insert a parallel B2 node.
	steps := g.dag.Steps()
	if len(steps) < 3 {
		return
	}

	// Pick a random start index.
	start := rand.Intn(len(steps) - 2)
	a, b, c := steps[start], steps[start+1], steps[start+2]

	if g.dag.NodeCount()+1 > g.config.MaxNodes {
		return
	}

	b2 := &engine.Step{
		ID:        b.ID + "-parallel",
		Name:      b.Name + "-parallel",
		AgentType: b.AgentType,
		Input:     b.Input,
		DependsOn: []string{a.ID},
	}
	if err := g.dag.AddNode(context.Background(), b2); err != nil {
		wfLog.Warn("parallelize add node failed", "node", b2.ID, "error", err)
		return
	}
	if g.dag.StepIndex()[c.ID] != nil {
		// Register the b2→c edge through the DAG so the parallel node is not
		// a dead end in m.dag.Edges (edge map is the authoritative topology).
		if err := g.dag.AddEdge(context.Background(), b2.ID, c.ID); err != nil {
			wfLog.Warn("parallelize add edge failed", "from", b2.ID, "to", c.ID, "error", err)
			// Roll back the freshly added node so a failed edge cannot leave
			// b2 as an unreachable island in the topology.
			if rmErr := g.dag.RemoveNode(context.Background(), b2.ID); rmErr != nil {
				wfLog.Warn("parallelize rollback add node failed", "node", b2.ID, "error", rmErr)
			}
		}
	}
}

func (g *WorkflowGenome) mutateSerialize() {
	// Convert a parallel fan-out into a linear chain.
	steps := g.dag.Steps()
	for _, step := range steps {
		deps := g.dag.ReadDeps(step.ID)
		if len(deps) >= 2 {
			// Remove all but the first dependency through the DAG so the
			// serialized chain is reflected in m.dag.Edges.
			for _, dep := range deps[1:] {
				if err := g.dag.RemoveEdge(context.Background(), dep, step.ID); err != nil {
					wfLog.Warn("serialize remove edge failed", "from", dep, "to", step.ID, "error", err)
					return
				}
			}
			return
		}
	}
}

func (g *WorkflowGenome) mutateSwapNodes() {
	// Swap the dependencies of two random nodes in the DAG.
	steps := g.dag.Steps()
	if len(steps) < 2 {
		return
	}
	i, j := rand.Intn(len(steps)), rand.Intn(len(steps))
	if i == j {
		j = (j + 1) % len(steps)
	}
	// Swap the dependency lists through the DAG (remove old in-edges, then add
	// the swapped ones) so m.dag.Edges stays authoritative. A cycle-detection
	// failure rolls back every operation and leaves the DAG untouched.
	depsI := g.dag.ReadDeps(steps[i].ID)
	depsJ := g.dag.ReadDeps(steps[j].ID)
	var ops []edgeOp
	for _, dep := range depsI {
		// A RemoveEdge failure means the edge was already absent (stale dep);
		// skipping avoids recording a rollback op that would re-create it.
		if err := g.dag.RemoveEdge(context.Background(), dep, steps[i].ID); err != nil {
			wfLog.Warn("swap remove edge failed", "from", dep, "to", steps[i].ID, "error", err)
			continue
		}
		ops = append(ops, edgeOp{from: dep, to: steps[i].ID, add: false})
	}
	for _, dep := range depsJ {
		if err := g.dag.RemoveEdge(context.Background(), dep, steps[j].ID); err != nil {
			wfLog.Warn("swap remove edge failed", "from", dep, "to", steps[j].ID, "error", err)
			continue
		}
		ops = append(ops, edgeOp{from: dep, to: steps[j].ID, add: false})
	}
	for _, dep := range depsJ {
		if err := g.dag.AddEdge(context.Background(), dep, steps[i].ID); err != nil {
			rollbackEdges(g.dag, ops)
			return
		}
		ops = append(ops, edgeOp{from: dep, to: steps[i].ID, add: true})
	}
	for _, dep := range depsI {
		if err := g.dag.AddEdge(context.Background(), dep, steps[j].ID); err != nil {
			rollbackEdges(g.dag, ops)
			return
		}
		ops = append(ops, edgeOp{from: dep, to: steps[j].ID, add: true})
	}
}

func (g *WorkflowGenome) mutateSplitNode() {
	// Split a random node into two sequential nodes.
	steps := g.dag.Steps()
	if len(steps) == 0 || g.dag.NodeCount()+1 > g.config.MaxNodes {
		return
	}
	target := steps[rand.Intn(len(steps))]
	splitID := target.ID + "-split"
	splitStep := &engine.Step{
		ID:        splitID,
		Name:      splitID,
		AgentType: target.AgentType,
		Input:     target.Input,
		DependsOn: []string{target.ID},
	}
	if err := g.dag.AddNode(context.Background(), splitStep); err != nil {
		wfLog.Warn("split add node failed", "node", splitID, "error", err)
		return
	}
	// Re-route downstream nodes through the DAG so the split node becomes
	// referenced in m.dag.Edges instead of a dead end.
	for _, s := range steps {
		if s.ID == splitID {
			continue // skip the freshly added split node itself
		}
		if !contains(g.dag.ReadDeps(s.ID), target.ID) {
			continue
		}
		if err := g.dag.RemoveEdge(context.Background(), target.ID, s.ID); err != nil {
			continue
		}
		if err := g.dag.AddEdge(context.Background(), splitID, s.ID); err != nil {
			_ = g.dag.AddEdge(context.Background(), target.ID, s.ID) // restore original edge
		}
	}
}

func (g *WorkflowGenome) mutateMergeNodes() {
	// Merge two consecutive nodes into one.
	steps := g.dag.Steps()
	if len(steps) < 2 {
		return
	}
	// Find two nodes where one depends on the other.
	for i := 0; i < len(steps); i++ {
		for j := 0; j < len(steps); j++ {
			if i == j {
				continue
			}
			if contains(steps[j].DependsOn, steps[i].ID) {
				// Merge j into i: remove j, update i's deps. j's deps may
				// include i's own ID (j depends on i), so strip both i's ID and
				// j's ID (the node being removed) to avoid a self-loop.
				merged := mergeDeps(steps[i].DependsOn, steps[j].DependsOn)
				merged = removeID(merged, steps[i].ID)
				merged = removeID(merged, steps[j].ID)
				// Remove j from the DAG (synced through the edge map), then add
				// the merged deps that i did not previously have so the merge
				// is visible in m.dag.Edges. If j cannot be removed (e.g. it
				// still has dependents), abort the merge before mutating i:
				// continuing would leave a ghost node whose DependsOn no longer
				// matches the edge map. Steps() returns internal pointers, so
				// i's Input must only be updated after j is actually removed.
				if err := g.dag.RemoveNode(context.Background(), steps[j].ID); err != nil {
					wfLog.Warn("merge remove node failed", "node", steps[j].ID, "error", err)
					return
				}
				steps[i].Input = steps[i].Input + " | " + steps[j].Input
				current := g.dag.ReadDeps(steps[i].ID)
				for _, dep := range merged {
					if !contains(current, dep) {
						if err := g.dag.AddEdge(context.Background(), dep, steps[i].ID); err != nil {
							wfLog.Warn("merge add edge failed", "from", dep, "to", steps[i].ID, "error", err)
						}
					}
				}
				return
			}
		}
	}
}

// removeID returns a copy of deps with id removed (used to avoid self-loops
// when merging a node's dependencies that may reference the node itself).
func removeID(deps []string, id string) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if d != id {
			out = append(out, d)
		}
	}
	return out
}

// edgeOp records one DAG edge mutation for rollback (used by mutateSwapNodes
// so a cycle-detection failure restores the DAG to its original topology).
type edgeOp struct {
	from string
	to   string
	add  bool // true: edge was added; false: edge was removed
}

// rollbackEdges reverts a sequence of edge ops in reverse order.
//
// Args:
//   - dag: the MutableDAG to restore.
//   - ops: the recorded operations (applied in order).
func rollbackEdges(dag *engine.MutableDAG, ops []edgeOp) {
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		if op.add {
			_ = dag.RemoveEdge(context.Background(), op.from, op.to)
		} else {
			_ = dag.AddEdge(context.Background(), op.from, op.to)
		}
	}
}

// contains checks if a string is in a slice.
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// mergeDeps combines two dependency lists, removing duplicates.
func mergeDeps(a, b []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(a)+len(b))
	for _, d := range a {
		if !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	for _, d := range b {
		if !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	return result
}

// clone creates a deep copy of the WorkflowGenome.
// Steps are sorted topologically before passing to NewMutableDAG
// because Steps() returns in non-deterministic map order, which would
// cause NewMutableDAG to reject the steps if dependents appear before deps.
func (g *WorkflowGenome) clone() *WorkflowGenome {
	steps := g.dag.Steps()
	cloneDag, err := engine.NewMutableDAG(sortByDeps(steps))
	if err != nil {
		// Last resort: rebuild from step positions to get deterministic order.
		ordered := make([]*engine.Step, 0, len(steps))
		for _, step := range steps {
			if len(step.DependsOn) == 0 {
				ordered = append(ordered, step)
			}
		}
		for _, step := range steps {
			if len(step.DependsOn) > 0 {
				ordered = append(ordered, step)
			}
		}
		cloneDag, err = engine.NewMutableDAG(ordered)
		if err != nil {
			// Absolute fallback: share parent (mutation may be no-op).
			cloneDag = g.dag
		}
	}
	return &WorkflowGenome{
		dag:    cloneDag,
		config: g.config,
	}
}

// sortByDeps returns steps in topological order (dependencies before dependents).
func sortByDeps(steps []*engine.Step) []*engine.Step {
	// Build in-degree map.
	inDegree := make(map[string]int, len(steps))
	stepMap := make(map[string]*engine.Step, len(steps))
	for _, s := range steps {
		inDegree[s.ID] = len(s.DependsOn)
		stepMap[s.ID] = s
	}

	// Find roots (no dependencies).
	var queue []string
	for _, s := range steps {
		if len(s.DependsOn) == 0 {
			queue = append(queue, s.ID)
		}
	}

	// Kahn's algorithm.
	result := make([]*engine.Step, 0, len(steps))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, stepMap[id])

		// Decrease in-degree of nodes depending on this one.
		for _, s := range steps {
			if contains(s.DependsOn, id) {
				inDegree[s.ID]--
				if inDegree[s.ID] == 0 {
					queue = append(queue, s.ID)
				}
			}
		}
	}

	return result
}
