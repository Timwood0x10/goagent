package sdk

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Graph is dynamic orchestration as a first-class citizen of the SDK
// (docs/design/sdk-graph-v030.md). Nodes execute, edges carry
// optional conditions, and an optional router overrides the next hop at
// runtime — a minimal revival of the retired workflow-graph essentials
// (NodeRouter + conditional edges + runtime mutation), driven through the
// SAME kernel path as Submit for LLM nodes.
//
// Zero value is not usable; construct with NewGraph. All mutating methods
// are safe for concurrent use: RunGraph takes ONE fixed snapshot at entry and
// executes against it, so a concurrent AddNode/RemoveNode never tears a round
// (no data race) — but structure changes do NOT take effect on the in-flight
// run; they apply from the next RunGraph call. Evolution-style runtime
// patching is therefore a future milestone, not a current behavior.
type Graph struct {
	mu    sync.RWMutex
	id    string
	nodes map[string]graphNode
	order []string // insertion order for deterministic rounds
	edges []graphEdge
	// router, when set, is consulted after every node completion; a non-empty
	// return forces that node as the sole next execution (the loop/jump
	// mechanism), overriding static edges for that step.
	router func(ctx context.Context, currentNodeID string, state map[string]any) string
	// MaxIterations bounds how many times ONE node may execute (router loops).
	// <= 0 means the default (defaultGraphMaxIterations).
	MaxIterations int
	// MaxRoundConcurrency caps how many ready nodes launch in parallel within
	// one round (errgroup limit; 0 = unbounded). It carries the concurrency-
	// throttling value of the retired workflow schedulers:
	// ordering policies died with workflow.Runner because a fully-parallel
	// ready batch has no "who runs first" decision left to make, but capping
	// simultaneous LLM calls remains operationally meaningful.
	MaxRoundConcurrency int
	// Timeout caps the total wall-clock duration of RunGraph (<= 0 = no
	// limit). When set, RunGraph derives a child context with this deadline;
	// expiry returns context.DeadlineExceeded and cancels in-flight nodes.
	Timeout time.Duration
	// buildErr defers capacity/duplicate violations from Add* to RunGraph so
	// the builder chain stays fluent (*Graph receivers cannot return errors).
	buildErr error
}

// graphNode is one executable vertex. Exactly one of the three kinds is set.
type graphNode struct {
	// agent is the *Agent node's configured agent (instruction/tools intact).
	// The pointer is retained — NOT just the name — so an agent created via
	// NewAgent and added with AddNode (never through RegisterAgent) still runs
	// with its own configuration instead of a bare capability-named stand-in.
	agent     *Agent
	agentName string                                                // capability the agent node executes as (== agent.name)
	fn        func(ctx context.Context, state map[string]any) error // pure function node
	sub       *Graph                                                // nested subgraph node
}

// graphEdge is a directed edge whose condition decides whether the target
// becomes reachable once the source settles. nil cond means unconditional.
type graphEdge struct {
	from, to string
	cond     func(state map[string]any) bool
}

// Limits hard-coded by design; Add* records a deferred error on violation.
const (
	maxGraphNodes = 1024
	maxGraphEdges = 4096
	// defaultGraphMaxIterations bounds router-driven re-execution of a single
	// node (infinite-loop guard).
	defaultGraphMaxIterations = 100
)

// NewGraph creates an empty named graph.
func NewGraph(id string) *Graph {
	return &Graph{
		id:    id,
		nodes: make(map[string]graphNode),
	}
}

// AddNode adds an executable node. exec must be one of:
//   - *Agent          → executed through the kernel scheduling path (the same
//     fabric quantum engine Submit uses); its registered capability is the
//     agent name.
//   - func(ctx, state) error → a pure compute node, executed inline by the
//     orchestration engine (no LLM involvement, nothing to schedule).
//   - *Graph          → a nested subgraph, executed recursively against the
//     SAME shared state map.
//
// Unknown kinds are rejected with a deferred error surfaced by RunGraph.
func (g *Graph) AddNode(id string, exec any) *Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buildErr != nil {
		return g
	}
	if id == "" {
		g.buildErr = errors.New("sdk: AddNode requires a non-empty id")
		return g
	}
	if _, dup := g.nodes[id]; dup {
		g.buildErr = errors.New("sdk: duplicate node id " + id)
		return g
	}
	if len(g.nodes) >= maxGraphNodes {
		g.buildErr = errors.New("sdk: graph node cap exceeded (1024)")
		return g
	}
	n := graphNode{}
	switch v := exec.(type) {
	case *Agent:
		if v == nil {
			g.buildErr = errors.New("sdk: AddNode(" + id + ") received a nil *Agent")
			return g
		}
		n.agent = v
		n.agentName = v.name
	case func(context.Context, map[string]any) error:
		if v == nil {
			g.buildErr = errors.New("sdk: AddNode(" + id + ") received a nil function")
			return g
		}
		n.fn = v
	case *Graph:
		if v == nil {
			g.buildErr = errors.New("sdk: AddNode(" + id + ") received a nil *Graph")
			return g
		}
		n.sub = v
	default:
		g.buildErr = errors.New("sdk: AddNode(" + id + ") kind unsupported: want *Agent, func(ctx, state) error, or *Graph")
		return g
	}
	g.nodes[id] = n
	g.order = append(g.order, id)
	return g
}

// AddEdge adds a directed edge; cond (optional) is evaluated once the source
// settles — false marks the edge dead, and a target whose incoming edges are
// ALL dead is skipped (cascading to its own outgoing edges). Endpoints may be
// added after the edge (forward reference); missing endpoints are validated
// when the graph runs.
//
// Join semantics (multi-incoming-edge nodes): JoinAll — a node becomes ready
// only when EVERY incoming source has settled (done/failed/skipped) AND at
// least one incoming edge is alive (unconditional, or its condition held);
// conditions are evaluated as OUTGOING edges after the source settles. This
// mirrors the retired workflow JoinKind=JoinAll default; the router can
// override readiness for a forced single next hop.
func (g *Graph) AddEdge(from, to string, cond func(state map[string]any) bool) *Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buildErr != nil {
		return g
	}
	if from == "" || to == "" {
		g.buildErr = errors.New("sdk: AddEdge requires non-empty endpoints")
		return g
	}
	if len(g.edges) >= maxGraphEdges {
		g.buildErr = errors.New("sdk: graph edge cap exceeded (4096)")
		return g
	}
	g.edges = append(g.edges, graphEdge{from: from, to: to, cond: cond})
	return g
}

// RemoveNode deletes a node and every edge touching it. Safe to call while a
// graph runs (RWMutex, no data race); the change takes effect from the NEXT
// RunGraph call, not the in-flight run (one fixed snapshot at entry).
func (g *Graph) RemoveNode(id string) *Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.nodes[id]; !ok {
		return g
	}
	delete(g.nodes, id)
	for i, o := range g.order {
		if o == id {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
	kept := g.edges[:0]
	for _, e := range g.edges {
		if e.from != id && e.to != id {
			kept = append(kept, e)
		}
	}
	g.edges = kept
	return g
}

// RemoveEdge deletes the first edge matching from→to (conditions are not
// compared; the builder treats edges as unordered multiset entries).
func (g *Graph) RemoveEdge(from, to string) *Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, e := range g.edges {
		if e.from == from && e.to == to {
			g.edges = append(g.edges[:i], g.edges[i+1:]...)
			return g
		}
	}
	return g
}

// SetRouter installs the dynamic-routing callback (the retired NodeRouter
// essence): after a node completes, the router receives (ctx, completedID,
// state); a non-empty return forces that node as the SOLE next execution —
// enabling jumps, fallbacks, and bounded loops (per-node re-runs are capped
// by MaxIterations). Static edges are ignored for that one step.
func (g *Graph) SetRouter(fn func(ctx context.Context, currentNodeID string, state map[string]any) string) *Graph {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.router = fn
	return g
}

// graphSnapshot is an immutable view the engine works against so concurrent
// mutation never tears a round.
type graphSnapshot struct {
	maxConcurrency int
	nodes          map[string]graphNode
	order          []string
	out            map[string][]string // from → to list, insertion order
	in             map[string][]string // to → from list
	router         func(ctx context.Context, currentNodeID string, state map[string]any) string
	conds          map[[2]string]func(map[string]any) bool
	buildErr       error
	timeout        time.Duration
	// maxIterations is snapshotted under RLock to prevent the data
	// race when RunGraph reads it without holding the lock.
	maxIterations int
}

// snapshot copies the current structure under RLock.
func (g *Graph) snapshot() graphSnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	snap := graphSnapshot{
		maxConcurrency: g.MaxRoundConcurrency,
		nodes:          make(map[string]graphNode, len(g.nodes)),
		order:          append([]string(nil), g.order...),
		out:            make(map[string][]string),
		in:             make(map[string][]string),
		router:         g.router,
		conds:          make(map[[2]string]func(map[string]any) bool),
		buildErr:       g.buildErr,
		timeout:        g.Timeout,
		maxIterations:  g.MaxIterations,
	}
	for id, n := range g.nodes {
		snap.nodes[id] = n
	}
	for _, e := range g.edges {
		snap.out[e.from] = append(snap.out[e.from], e.to)
		snap.in[e.to] = append(snap.in[e.to], e.from)
		key := [2]string{e.from, e.to}
		snap.conds[key] = e.cond
	}
	return snap
}
