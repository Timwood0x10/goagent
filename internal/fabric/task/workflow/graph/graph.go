// package graph - provides dynamic agent orchestration with pluggable scheduling.

package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_ratelimit"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// Edge represents a connection between two nodes with optional condition.
type Edge struct {
	from string
	to   string
	cond Condition
}

// Condition defines a predicate function for edge traversal.
type Condition func(state *State) bool

// IfFunc creates a condition from a function.
func IfFunc(fn func(state *State) bool) Condition {
	return fn
}

// NodeRouter is a callback for dynamic routing decisions during graph execution.
// After a node completes, the router is called with the just-executed node ID
// and current state. If it returns a non-empty node ID, that node is enqueued
// for execution next (bypassing the DAG's static edge traversal).
// Return "" to let the DAG decide the next node via in-degree BFS as usual.
type NodeRouter func(ctx context.Context, currentNodeID string, state *State) string

// Graph represents a DAG of nodes with conditional edges.
//
// Graph is safe for concurrent reads via Execute, but concurrent
// mutation (Node, Edge, Start, RemoveEdge, RemoveNode, Clear, etc.)
// from multiple goroutines requires external synchronization.
type Graph struct {
	mu        sync.RWMutex
	id        string
	nodes     map[string]Node
	edges     map[string][]*Edge
	start     string
	scheduler Scheduler
	tracer    observability.Tracer   // observability tracer for execution tracking
	limiter   ares_ratelimit.Limiter // rate limiter for execution throttling
	router    NodeRouter             // optional dynamic routing callback
}

// NewGraph creates a new graph with the given ID.
//
// Args:
// id - unique graph identifier, must not be empty.
// Returns new graph instance or error if id is empty.
func NewGraph(id string) (*Graph, error) {
	if id == "" {
		return nil, errors.New("graph ID cannot be empty")
	}
	return &Graph{
		id:        id,
		nodes:     make(map[string]Node),
		edges:     make(map[string][]*Edge),
		scheduler: NewDefaultScheduler(),
		tracer:    observability.NewNoopTracer(), // default to no-op tracer
		limiter:   nil,                           // default to no rate limiting
	}, nil
}

// NewGraphWithTracer removed (dead code — use NewGraph + SetScheduler).

// NewGraphWithLimiter creates a new graph with a custom rate limiter.
//
// Args:
// id - unique graph identifier, must not be empty.
// limiter - rate limiter for execution throttling.
// Returns new graph instance or error.
func NewGraphWithLimiter(id string, limiter ares_ratelimit.Limiter) (*Graph, error) {
	if id == "" {
		return nil, errors.New("graph ID cannot be empty")
	}
	return &Graph{
		id:        id,
		nodes:     make(map[string]Node),
		edges:     make(map[string][]*Edge),
		scheduler: NewDefaultScheduler(),
		tracer:    observability.NewNoopTracer(),
		limiter:   limiter,
	}, nil
}

// Node adds a node to the graph.
//
// Args:
// id - unique node identifier, must not be empty.
// node - node instance, must not be nil.
// Returns graph for chaining or error.
func (g *Graph) Node(id string, node Node) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if id == "" {
		return nil, errors.New("node ID cannot be empty")
	}
	if node == nil {
		return nil, errors.New("node cannot be nil")
	}
	if _, exists := g.nodes[id]; exists {
		return nil, fmt.Errorf("duplicate node ID %q", id)
	}
	g.nodes[id] = node
	return g, nil
}

// Edge adds an edge from one node to another with optional condition.
//
// Args:
// from - source node ID, must not be empty and must exist in the graph.
// to - target node ID, must not be empty and must exist in the graph.
// cond - optional edge traversal condition.
// Returns graph for chaining or error.
func (g *Graph) Edge(from, to string, cond ...Condition) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if from == "" {
		return nil, errors.New("from node ID cannot be empty")
	}
	if to == "" {
		return nil, errors.New("to node ID cannot be empty")
	}
	if _, ok := g.nodes[from]; !ok {
		return nil, fmt.Errorf("from node %q not found: node must be added via Node() before Edge()", from)
	}
	if _, ok := g.nodes[to]; !ok {
		return nil, fmt.Errorf("to node %q not found: node must be added via Node() before Edge()", to)
	}

	edge := &Edge{from: from, to: to}
	if len(cond) > 0 {
		edge.cond = cond[0]
	}

	// Suppress only exact duplicate *unconditional* edges (same from→to, no
	// condition). Previously any second conditional edge to the same target was
	// also dropped on the assumption that function equality cannot be checked —
	// but two distinct conditions on the same from→to are legitimate branches
	// (compileGraphEdges emits BranchMany for each). Dropping them silently
	// broke multi-branch graphs. Conditional edges are always appended; callers
	// responsible for not re-adding the exact same closure in a loop.
	for _, existing := range g.edges[from] {
		if existing.to == to && edge.cond == nil && existing.cond == nil {
			return g, nil // silently allow duplicate no-cond edges
		}
	}

	g.edges[from] = append(g.edges[from], edge)
	return g, nil
}

// Start sets the starting node for the graph.
//
// Args:
// id - starting node ID, must not be empty.
// Returns graph for chaining or error.
func (g *Graph) Start(id string) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if id == "" {
		return nil, errors.New("start node ID cannot be empty")
	}
	g.start = id
	return g, nil
}

// Clear removed (dead code — only tests).

// RemoveEdge removes an edge from one node to another.
// Retained: used by patcher.go applyRemoveEdge.
func (g *Graph) RemoveEdge(from, to string) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if from == "" {
		return nil, errors.New("from node ID cannot be empty")
	}
	if to == "" {
		return nil, errors.New("to node ID cannot be empty")
	}

	if edges, ok := g.edges[from]; ok {
		newEdges := make([]*Edge, 0, len(edges))
		for _, edge := range edges {
			if edge.to != to {
				newEdges = append(newEdges, edge)
			}
		}
		g.edges[from] = newEdges
	}

	return g, nil
}

// RemoveNode removes a node and all its associated edges from the graph.
// Retained: used by patcher.go applyRemoveNode.
func (g *Graph) RemoveNode(id string) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if id == "" {
		return nil, errors.New("node ID cannot be empty")
	}

	delete(g.nodes, id)

	// Remove all edges pointing to the removed node.
	for from, edges := range g.edges {
		newEdges := make([]*Edge, 0, len(edges))
		for _, edge := range edges {
			if edge.to != id {
				newEdges = append(newEdges, edge)
			}
		}
		g.edges[from] = newEdges
	}

	// Remove edges originating from the removed node.
	delete(g.edges, id)

	// Clear start if it points to the removed node.
	if g.start == id {
		g.start = ""
	}

	return g, nil
}

// SetScheduler sets a custom scheduler for the graph.
//
// Args:
// scheduler - custom scheduler instance, must not be nil.
// Returns graph for chaining or error.
func (g *Graph) SetScheduler(scheduler Scheduler) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if scheduler == nil {
		return nil, errors.New("scheduler cannot be nil")
	}
	g.scheduler = scheduler
	return g, nil
}

// SetTracer, SetPluginBus, SetExecutionCollector, SetLimiter, SetCheckpointStore
// removed (dead code).
// SetScheduler retained (used by patcher.go).

// SetRouter sets a dynamic routing callback that is invoked after each
// successfully completed node. Retained: used by SDK tests.
func (g *Graph) SetRouter(router NodeRouter) (*Graph, error) {
	if g == nil {
		return nil, errors.New("graph is nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.router = router
	return g, nil
}

// ID returns the graph ID.
func (g *Graph) ID() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.id
}

// NodeIDs returns all node IDs in the graph. The order is non-deterministic.
func (g *Graph) NodeIDs() []string {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	ids := make([]string, 0, len(g.nodes))
	for id := range g.nodes {
		ids = append(ids, id)
	}
	return ids
}

// EdgeInfo carries the serializable parts of an edge for IR compilation.
type EdgeInfo struct {
	From    string
	To      string
	HasCond bool
}

// RuntimeEdge contains one executable edge and its optional predicate.
type RuntimeEdge struct {
	From      string
	To        string
	Condition Condition
}

// Edges returns all edges in the graph as serializable EdgeInfo values.
func (g *Graph) Edges() []EdgeInfo {
	runtimeEdges := g.RuntimeEdges()
	edges := make([]EdgeInfo, 0, len(runtimeEdges))
	for _, edge := range runtimeEdges {
		edges = append(edges, EdgeInfo{
			From:    edge.From,
			To:      edge.To,
			HasCond: edge.Condition != nil,
		})
	}
	return edges
}

// RuntimeEdges returns executable edge bindings for unified compilation.
func (g *Graph) RuntimeEdges() []RuntimeEdge {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	edges := make([]RuntimeEdge, 0, len(g.edges))
	for from, targets := range g.edges {
		for _, edge := range targets {
			edges = append(edges, RuntimeEdge{
				From:      from,
				To:        edge.to,
				Condition: edge.cond,
			})
		}
	}
	return edges
}

// RuntimeRouter returns the optional graph routing callback.
func (g *Graph) RuntimeRouter() NodeRouter {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.router
}

// RuntimeScheduler returns the configured ready-node selector.
func (g *Graph) RuntimeScheduler() Scheduler {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.scheduler
}

// RuntimePluginBus, RuntimeCollector, RuntimeCheckpointStore removed (dead code).
// RuntimeRouter retained for graph execution.

// RuntimeNodes returns executable node bindings for unified compilation.
func (g *Graph) RuntimeNodes() map[string]Node {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	nodes := make(map[string]Node, len(g.nodes))
	for id, node := range g.nodes {
		nodes[id] = node
	}
	return nodes
}

// StartNode returns the configured start node ID, or "" if not set.
func (g *Graph) StartNode() string {
	if g == nil {
		return ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.start
}

// Result represents the result of graph execution.
type Result struct {
	GraphID  string
	State    *State
	Duration time.Duration
	Error    error
}
