package sdk

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// RunGraph executes a graph until no node remains runnable (or ctx is
// cancelled / a hard cap trips), returning every node result plus the final
// shared state.
//
// Execution model (docs/design/sdk-graph-v030.md):
//   - Rounds are barriers: every currently-runnable node launches in parallel,
//     the round ends when they all settle.
//   - LLM (*Agent) nodes go through the kernel scheduling path — the same
//     fabric quantum engine Submit uses (single execution path, no second
//     engine).
//   - Function and subgraph nodes execute inline: they involve no LLM call,
//     so there is nothing to schedule; routing them through the fabric would
//     add latency without adding durability (they hold no cross-restart
//     intent). This mirrors the design note that pure-compute stages need not
//     occupy a scheduler slot.
//   - After a node completes, its outgoing condition edges are evaluated
//     against the shared state; false marks an edge dead, and a node whose
//     incoming edges are ALL dead is skipped (cascading).
//   - The router (if installed) is consulted after each completion; a
//     non-empty return forces that node as the SOLE next execution — the jump
//     and bounded-loop mechanism. Re-execution is capped per node by
//     MaxIterations (default 100).
//
// Node failure ends its branch (outgoing edges die) but does not cancel
// sibling branches; the first failure is returned alongside partial results.
func (r *Runtime) RunGraph(ctx context.Context, g *Graph) (*GraphResult, error) {
	if g == nil {
		return nil, errors.New("sdk: RunGraph graph is nil")
	}
	snap := g.snapshot()
	if snap.buildErr != nil {
		return nil, snap.buildErr
	}
	if err := validateGraph(snap); err != nil {
		return nil, err
	}

	// Register every *Agent node's executor ONCE, up front, single-threaded.
	// This uses the retained *Agent pointer so an agent added via AddNode
	// (never through RegisterAgent) keeps its own instruction/tools instead of
	// falling back to a bare capability-named stand-in. Doing it here — before
	// any round goroutine starts — also means the parallel rounds never write
	// r.sdkExecutors concurrently (the scheduler reads it lock-free).
	r.registerGraphAgents(snap)

	st := newGraphRun()
	maxIter := snap.maxIterations
	if maxIter <= 0 {
		maxIter = defaultGraphMaxIterations
	}

	// Apply graph-level timeout: derive a child context so
	// in-flight nodes are cancelled when the deadline expires.
	runCtx := ctx
	if snap.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, snap.timeout)
		defer cancel()
	}

	firstErr := r.runGraphRounds(runCtx, snap, st, maxIter)

	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]any, len(st.state))
	for k, v := range st.state {
		out[k] = v
	}
	return &GraphResult{NodeResults: st.results, State: out}, firstErr
}

// GraphResult carries per-node outputs and the final shared state.
type GraphResult struct {
	// NodeResults maps node id → its execution result (LLM nodes carry the
	// agent's real Output; function nodes a success placeholder).
	NodeResults map[string]*Result
	// State is the shared state map after the final round.
	State map[string]any
}

// graphRun is one execution's mutable bookkeeping, guarded by mu.
type graphRun struct {
	mu       sync.Mutex
	state    map[string]any
	results  map[string]*Result
	done     map[string]bool
	skipped  map[string]bool
	failed   map[string]bool
	iter     map[string]int
	edgeDead map[[2]string]bool
	lastDone string
}

// validateGraph checks endpoints exist and the graph is not trivially empty.
func validateGraph(snap graphSnapshot) error {
	if len(snap.nodes) == 0 {
		return errors.New("sdk: RunGraph on an empty graph")
	}
	for from, tos := range snap.out {
		if _, ok := snap.nodes[from]; !ok {
			return fmt.Errorf("sdk: edge references unknown source %q", from)
		}
		for _, to := range tos {
			if _, ok := snap.nodes[to]; !ok {
				return fmt.Errorf("sdk: edge references unknown target %q", to)
			}
		}
	}
	return nil
}

// runGraphRounds drives barrier rounds until nothing is runnable.
func (r *Runtime) runGraphRounds(ctx context.Context, snap graphSnapshot, st *graphRun, maxIter int) error {
	idle := 0 // consecutive rounds with zero executions → termination guard
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		st.settleSkips(snap)
		ready := st.readySet(snap, maxIter)

		// Router override: consumes the last completion's decision. A forced
		// pick may re-execute a done node (bounded by maxIter) — the only way
		// a cycle actually loops.
		//
		// The router reads st.state WITHOUT holding st.mu — safe only because
		// this call happens strictly between barrier rounds (no node goroutine
		// is running here: runRound's errgroup.Wait has returned). Do not move
		// this call inside a round.
		if snap.router != nil && st.lastDone != "" {
			next := snap.router(ctx, st.lastDone, st.state)
			st.lastDone = ""
			if next != "" && st.canRun(next, snap, maxIter) {
				ready = []string{next}
			}
		}

		if len(ready) == 0 {
			idle++
			// idle >= 2, not 1: a single empty round is expected after the last
			// batch settles (readySet is computed BEFORE outgoing edges open,
			// so one round can legitimately produce no work). Two consecutive
			// empty rounds mean the graph truly has no runnable node left.
			if st.pendingCount(snap) == 0 || idle >= 2 {
				return nil
			}
			continue
		}
		idle = 0

		completions, firstErr := r.runRound(ctx, snap, st, ready)
		// EVERY completion's outgoing conditions must be evaluated — a round
		// may settle several nodes whose branches all need to open. Only the
		// FIRST completion seeds the router decision (v1 limitation).
		for i, id := range completions {
			st.evaluateOutgoing(snap, id)
			if i == 0 {
				st.lastDone = id
			}
		}
		if firstErr != nil {
			return firstErr
		}
	}
}

// runRound executes every ready node concurrently and waits for the barrier.
func (r *Runtime) runRound(ctx context.Context, snap graphSnapshot, st *graphRun, ready []string) (completions []string, firstErr error) {
	group, gctx := errgroup.WithContext(ctx)
	if snap.maxConcurrency > 0 {
		group.SetLimit(snap.maxConcurrency)
	}
	var mu sync.Mutex // serializes completion-order appends
	for _, id := range ready {
		id := id
		group.Go(func() error {
			if err := r.execGraphNode(gctx, snap, st, id); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return nil // siblings keep running; error reported via firstErr
			}
			mu.Lock()
			completions = append(completions, id)
			mu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	return completions, firstErr
}

// execGraphNode runs one node and records its outcome.
func (r *Runtime) execGraphNode(ctx context.Context, snap graphSnapshot, st *graphRun, id string) error {
	n := snap.nodes[id]
	st.mu.Lock()
	st.iter[id]++
	st.mu.Unlock()

	switch {
	case n.agentName != "":
		st.mu.Lock()
		input := st.agentInput(snap, id)
		st.mu.Unlock()
		res, err := r.Submit(ctx, Task{Capability: n.agentName, Input: input})
		st.mu.Lock()
		defer st.mu.Unlock()
		if err != nil {
			st.failed[id] = true
			return fmt.Errorf("graph node %q: %w", id, err)
		}
		st.done[id] = true
		st.results[id] = res
		if res != nil {
			st.state[id] = res.Output
		}
		return nil

	case n.fn != nil:
		// Copy the state snapshot under lock so the user function never
		// holds st.mu (it might call back into the graph or block on I/O).
		st.mu.Lock()
		stateCopy := copyState(st.state)
		st.mu.Unlock()
		err := func() (runErr error) {
			defer func() {
				if p := recover(); p != nil {
					runErr = fmt.Errorf("graph node %q panicked: %v", id, p)
				}
			}()
			return n.fn(ctx, stateCopy)
		}()
		st.mu.Lock()
		defer st.mu.Unlock()
		if err != nil {
			st.failed[id] = true
			return fmt.Errorf("graph node %q: %w", id, err)
		}
		// Merge any keys the function wrote into the shared state.
		for k, v := range stateCopy {
			st.state[k] = v
		}
		st.done[id] = true
		st.results[id] = &Result{Output: "ok"}
		return nil

	case n.sub != nil:
		subSnap := n.sub.snapshot()
		// Copy the parent state under the parent lock so the subgraph never
		// writes the parent's map while sibling nodes in the SAME round run
		// under the parent lock — one map, one lock. The subgraph executes
		// against its own copy (protected by its own child.mu) and merges
		// back once it finishes, mirroring the function-node pattern.
		st.mu.Lock()
		childState := copyState(st.state)
		st.mu.Unlock()
		child := &graphRun{
			state:    childState,
			done:     make(map[string]bool),
			skipped:  make(map[string]bool),
			failed:   make(map[string]bool),
			results:  make(map[string]*Result),
			iter:     make(map[string]int),
			edgeDead: make(map[[2]string]bool),
		}
		err := r.runGraphRounds(ctx, subSnap, child, orDefault(subSnap.maxIterations))
		st.mu.Lock()
		defer st.mu.Unlock()
		if err != nil {
			st.failed[id] = true
			return fmt.Errorf("graph node %q (subgraph %s): %w", id, n.sub.id, err)
		}
		// Merge the subgraph's state back into the parent (snapshot at start,
		// merge on completion — same semantics as function nodes).
		for k, v := range childState {
			st.state[k] = v
		}
		st.done[id] = true
		st.results[id] = &Result{Output: "ok"}
		return nil
	}
	return fmt.Errorf("graph node %q has no executable kind", id)
}

// registerGraphAgents installs an executor for every *Agent node before the
// run starts, single-threaded. It uses the retained *Agent pointer so a node
// added via AddNode (never through RegisterAgent) resolves the INTENDED agent
// (instruction/tools intact) instead of Submit auto-creating a bare
// capability-named stand-in. Running once up front also keeps r.sdkExecutors
// free of concurrent writes during the parallel rounds (the scheduler reads it
// without the agent lock).
func (r *Runtime) registerGraphAgents(snap graphSnapshot) {
	r.ensureScheduler()
	r.agentMu.Lock()
	defer r.agentMu.Unlock()
	for _, n := range snap.nodes {
		if n.agent == nil {
			continue
		}
		// check and register through the scheduler's own registry
		// (execMu-guarded) — no direct map write.
		if _, ok := r.sched.LookupExecutor(n.agentName); ok {
			continue // already registered (e.g. via RegisterAgent) — keep it
		}
		r.sched.RegisterExecutor(n.agentName, &sdkAgentExecutor{agent: n.agent})
	}
}

// agentInput resolves the input for an agent node. Pipeline data flow: a node
// with a single incoming edge consumes its upstream source's OUTPUT (the
// source node's state entry, which agent nodes write as state[fromID]), so
// agent→agent chains pass results along instead of re-reading the stale
// global state["input"]. Roots (no incoming edges) and multi-incoming nodes
// (JoinAll — no single upstream output) fall back to state["input"]. Caller
// must hold st.mu.
func (st *graphRun) agentInput(snap graphSnapshot, id string) string {
	ins := snap.in[id]
	if len(ins) == 1 {
		if v, ok := st.state[ins[0]].(string); ok {
			return v
		}
	}
	if v, ok := st.state["input"].(string); ok {
		return v
	}
	return ""
}

// evaluateOutgoing runs the freshly settled node's edge conditions against
// the shared state; failed conds (or a failed/skipped source) kill the edge.
func (st *graphRun) evaluateOutgoing(snap graphSnapshot, id string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, to := range snap.out[id] {
		key := [2]string{id, to}
		if cond := snap.conds[key]; cond != nil && !cond(st.state) {
			st.edgeDead[key] = true
		}
	}
	if st.failed[id] || st.skipped[id] {
		for _, to := range snap.out[id] {
			st.edgeDead[[2]string{id, to}] = true
		}
	}
}

// settleSkips cascades skipping: a node with at least one incoming edge whose
// edges are ALL dead can never fire.
func (st *graphRun) settleSkips(snap graphSnapshot) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, id := range snap.order {
		if st.done[id] || st.skipped[id] || st.failed[id] {
			continue
		}
		ins := snap.in[id]
		if len(ins) == 0 {
			continue // roots are never skipped
		}
		allDead := true
		for _, from := range ins {
			if !st.edgeDead[[2]string{from, id}] {
				allDead = false
				break
			}
		}
		if allDead {
			st.skipped[id] = true
		}
	}
}

// readySet lists nodes runnable this round: unsettled, under the iteration
// cap, all incoming sources settled, and at least one live incoming edge.
// This is the JoinAll readiness rule: EVERY incoming source must have settled
// (done/failed/skipped) before the node may run; a single unsettled source
// blocks the node regardless of how many other edges are live (JoinAny is
// deliberately not offered — the router covers any-one-of-N semantics).
func (st *graphRun) readySet(snap graphSnapshot, maxIter int) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var ready []string
	for _, id := range snap.order {
		if st.done[id] || st.skipped[id] || st.failed[id] || st.iter[id] >= maxIter {
			continue
		}
		live, blocked := false, false
		for _, from := range snap.in[id] {
			srcSettled := st.done[from] || st.failed[from] || st.skipped[from]
			if !srcSettled {
				blocked = true
				break
			}
			if !st.edgeDead[[2]string{from, id}] {
				live = true
			}
		}
		if blocked {
			continue
		}
		hasIncoming := len(snap.in[id]) > 0
		if hasIncoming && !live {
			continue // all-dead handled by settleSkips next pass
		}
		ready = append(ready, id)
	}
	return ready
}

// canRun reports whether the router may force-run id this round.
func (st *graphRun) canRun(id string, snap graphSnapshot, maxIter int) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := snap.nodes[id]; !ok {
		return false
	}
	if st.skipped[id] || st.failed[id] || st.iter[id] >= maxIter {
		return false
	}
	return true
}

// pendingCount counts nodes that have not reached a terminal state.
func (st *graphRun) pendingCount(snap graphSnapshot) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, id := range snap.order {
		if !st.done[id] && !st.skipped[id] && !st.failed[id] {
			n++
		}
	}
	return n
}

// orDefault resolves an iteration bound that is unset (<= 0) to the default.
func orDefault(v int) int {
	if v <= 0 {
		return defaultGraphMaxIterations
	}
	return v
}

// newGraphRun allocates a fully-initialised graphRun so every map field is
// non-nil (a zero-value graphRun would panic on first map write).
func newGraphRun() *graphRun {
	return &graphRun{
		state:    make(map[string]any),
		done:     make(map[string]bool),
		skipped:  make(map[string]bool),
		failed:   make(map[string]bool),
		results:  make(map[string]*Result),
		iter:     make(map[string]int),
		edgeDead: make(map[[2]string]bool),
	}
}

// copyState returns a shallow copy of m so a node function can read/write
// without holding the graphRun mutex.
func copyState(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
