package retriever

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// Query is a retrieval request.
type Query struct {
	// Text is the natural language query (e.g. "Why did we choose Redis?").
	Text string `json:"text"`

	// Types restricts retrieval to specific object types (empty = all types).
	Types []knowledge.ObjectType `json:"types,omitempty"`

	// MaxResults caps the number of nodes in the result graph (0 = default 50).
	MaxResults int `json:"max_results,omitempty"`

	// MaxTokens caps the total LLM context tokens (0 = default 4000).
	MaxTokens int `json:"max_tokens,omitempty"`

	// TokenBudgetForGraph is the portion of MaxTokens allocated to graph
	// context (0 = default 60% of MaxTokens).
	TokenBudgetForGraph int `json:"token_budget_for_graph,omitempty"`

	// Formats specifies which compiler output formats to generate.
	// Defaults to [Prompt] when empty.
	Formats []compiler.Format `json:"formats,omitempty"`
}

// Result is the output of a retrieval operation.
type Result struct {
	// Context contains the compiled output in each requested format.
	Context *compiler.CompiledContext `json:"context"`

	// Graph is the full WorkingGraph (useful for inspection or re-compilation).
	Graph *knowledge.WorkingGraph `json:"graph,omitempty"`

	// Query is the original query for traceability.
	Query string `json:"query"`
}

// Retriever implements the AKG retrieval flow: Intent → Graph → Expand → Prune → Compile.
// It wraps the KnowledgeRuntime and Compiler into a single query interface.
type Retriever struct {
	runtime  *runtime.KnowledgeRuntime
	compiler compiler.Compiler
}

// New creates a Retriever backed by the given KnowledgeRuntime and Compiler.
func New(rt *runtime.KnowledgeRuntime, comp compiler.Compiler) *Retriever {
	return &Retriever{
		runtime:  rt,
		compiler: comp,
	}
}

// Retrieve executes the full AKF retrieval pipeline for a query.
//
// Steps:
//  1. Build an Intent from the query and scope constraints.
//  2. Run KnowledgeRuntime.Execute (Plan → Load → Pipeline → Link → Reduce).
//  3. Compile the resulting WorkingGraph into the requested output formats.
//  4. Return the compiled context + graph.
func (r *Retriever) Retrieve(ctx context.Context, query Query) (*Result, error) {
	if query.Text == "" {
		return nil, errors.New("retriever: query text is required")
	}

	if r.runtime == nil {
		return nil, errors.New("retriever: runtime is nil")
	}
	if r.compiler == nil {
		return nil, errors.New("retriever: compiler is nil")
	}

	// Build budget from query parameters.
	maxResults := query.MaxResults
	if maxResults <= 0 {
		maxResults = 50
	}
	maxTokens := query.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	forGraph := query.TokenBudgetForGraph
	if forGraph <= 0 {
		forGraph = maxTokens * 60 / 100 // 60% for graph
	}
	budget := knowledge.TokenBudget{
		MaxTokens: maxTokens,
		ForGraph:  forGraph,
		Reserved:  maxTokens - forGraph,
	}

	// Run KnowledgeRuntime: Plan → Load → Pipeline → Link → Reduce.
	// The runtime does not accept a scope/intent, so Query.Types is enforced
	// as a post-reduce filter below (see filterByTypes).
	graph, err := r.runtime.Execute(ctx, query.Text, budget, nil)
	if err != nil {
		return nil, fmt.Errorf("retriever: execute: %w", err)
	}

	// Apply the Query.Types filter on the reduced graph. Without this, the
	// declared Types field would be silently ignored.
	graph = filterByTypes(graph, query.Types)

	// Compile the graph into the requested formats.
	formats := query.Formats
	if len(formats) == 0 {
		formats = []compiler.Format{compiler.FormatPrompt}
	}
	cfg := compiler.CompileConfig{
		Formats:   formats,
		MaxTokens: maxTokens,
		MaxNodes:  maxResults,
	}
	compiled, err := r.compiler.Compile(ctx, graph, cfg)
	if err != nil {
		return nil, fmt.Errorf("retriever: compile: %w", err)
	}

	return &Result{
		Context: compiled,
		Graph:   graph,
		Query:   query.Text,
	}, nil
}

// filterByTypes returns a new WorkingGraph containing only nodes whose Type is
// in types, plus edges whose endpoints both survive. A nil graph or an empty
// types slice (meaning "all types") returns the graph unchanged.
func filterByTypes(graph *knowledge.WorkingGraph, types []knowledge.ObjectType) *knowledge.WorkingGraph {
	if graph == nil || len(types) == 0 {
		return graph
	}

	allowed := make(map[knowledge.ObjectType]struct{}, len(types))
	for _, t := range types {
		allowed[t] = struct{}{}
	}

	nodes := make(map[string]*knowledge.KnowledgeObject, len(graph.Nodes))
	for id, obj := range graph.Nodes {
		if _, ok := allowed[obj.Type]; ok {
			nodes[id] = obj
		}
	}

	edges := make([]knowledge.Relation, 0, len(graph.Edges))
	for _, e := range graph.Edges {
		if _, ok := nodes[e.From]; !ok {
			continue
		}
		if _, ok := nodes[e.To]; !ok {
			continue
		}
		edges = append(edges, e)
	}

	return &knowledge.WorkingGraph{Nodes: nodes, Edges: edges}
}
