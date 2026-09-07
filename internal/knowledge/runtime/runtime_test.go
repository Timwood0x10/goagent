package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
)

func TestDefaultLinker(t *testing.T) {
	linker := &DefaultLinker{}
	objects := []*knowledge.KnowledgeObject{
		{ID: "a", Tags: []string{"redis", "cache"}},
		{ID: "b", Tags: []string{"redis", "db"}},
		{ID: "c", Tags: []string{"cache", "memcached"}},
	}

	edges, err := linker.Link(context.Background(), objects)
	if err != nil {
		t.Fatalf("Link error: %v", err)
	}

	if len(edges) == 0 {
		t.Error("expected at least one edge from tag-based linking")
	}

	// Verify edges connect valid nodes.
	for _, e := range edges {
		if e.From != "a" && e.From != "b" && e.From != "c" {
			t.Errorf("unexpected from node: %s", e.From)
		}
		if e.Name != knowledge.RelBelongsTo {
			t.Errorf("expected %s relation, got %s", knowledge.RelBelongsTo, e.Name)
		}
	}
}

func TestDefaultLinkerEmptyObjects(t *testing.T) {
	linker := &DefaultLinker{}
	edges, err := linker.Link(context.Background(), nil)
	if err != nil {
		t.Fatalf("Link error: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 edges for nil input, got %d", len(edges))
	}
}

func TestDefaultLinkerLargeGroupUsesStarTopology(t *testing.T) {
	linker := &DefaultLinker{}

	// A single shared tag with more than maxAllPairs members. Under the old
	// all-pairs-with-cap-200 logic this orphaned members beyond index 200 and
	// wasted work; the star topology must link every member via the
	// representative in O(n) edges.
	const n = maxAllPairs + 50
	objects := make([]*knowledge.KnowledgeObject, 0, n)
	for i := range n {
		objects = append(objects, &knowledge.KnowledgeObject{
			ID:   fmt.Sprintf("obj-%d", i),
			Tags: []string{"shared"},
		})
	}

	edges, err := linker.Link(context.Background(), objects)
	if err != nil {
		t.Fatalf("Link error: %v", err)
	}

	// Star topology emits exactly n-1 edges (each member → representative),
	// far below the O(n²) all-pairs count.
	if len(edges) != n-1 {
		t.Fatalf("expected %d star edges, got %d", n-1, len(edges))
	}

	// Every edge must involve the representative (objects[0]) so no member is
	// orphaned from the cluster.
	rep := objects[0].ID
	linked := map[string]bool{rep: true}
	for _, e := range edges {
		if e.From != rep && e.To != rep {
			t.Errorf("edge %s→%s does not involve representative %s", e.From, e.To, rep)
		}
		linked[e.From] = true
		linked[e.To] = true
	}
	if len(linked) != n {
		t.Errorf("expected all %d members linked, got %d", n, len(linked))
	}
}

func TestDefaultReducer(t *testing.T) {
	reducer := &DefaultReducer{}
	graph := &knowledge.WorkingGraph{
		Nodes: map[string]*knowledge.KnowledgeObject{
			"high":   {ID: "high", Summary: "high confidence node", Confidence: 0.9},
			"medium": {ID: "medium", Summary: "medium confidence", Confidence: 0.5},
			"low":    {ID: "low", Summary: "low confidence node", Confidence: 0.1},
		},
		Edges: []knowledge.Relation{
			{From: "high", To: "low", Name: "related"},
		},
	}

	budget := knowledge.TokenBudget{ForGraph: 100} // ~2 nodes max
	reduced, err := reducer.Reduce(context.Background(), graph, budget)
	if err != nil {
		t.Fatalf("Reduce error: %v", err)
	}

	if len(reduced.Nodes) > 2 {
		t.Errorf("expected at most 2 nodes after pruning, got %d", len(reduced.Nodes))
	}

	// High confidence node should be kept.
	if _, ok := reduced.Nodes["high"]; !ok {
		t.Error("expected high-confidence node to be kept")
	}
}

func TestDefaultReducerZeroBudgetKeepsAll(t *testing.T) {
	reducer := &DefaultReducer{}
	graph := &knowledge.WorkingGraph{
		Nodes: map[string]*knowledge.KnowledgeObject{
			"a": {ID: "a", Summary: "node a", Confidence: 0.8},
			"b": {ID: "b", Summary: "node b", Confidence: 0.6},
			"c": {ID: "c", Summary: "node c", Confidence: 0.4},
		},
	}

	// Budget unset (ForGraph == 0): the reducer must not collapse the graph
	// to a single node (regression). All nodes must be retained.
	reduced, err := reducer.Reduce(context.Background(), graph, knowledge.TokenBudget{})
	if err != nil {
		t.Fatalf("Reduce error: %v", err)
	}
	if len(reduced.Nodes) != 3 {
		t.Errorf("expected all 3 nodes retained when budget is unset, got %d", len(reduced.Nodes))
	}
}

func TestDefaultReducerWithinBudget(t *testing.T) {
	reducer := &DefaultReducer{}
	graph := &knowledge.WorkingGraph{
		Nodes: map[string]*knowledge.KnowledgeObject{
			"a": {ID: "a", Summary: "node a", Confidence: 0.8},
		},
	}

	budget := knowledge.TokenBudget{ForGraph: 10000}
	reduced, err := reducer.Reduce(context.Background(), graph, budget)
	if err != nil {
		t.Fatalf("Reduce error: %v", err)
	}

	if len(reduced.Nodes) != 1 {
		t.Errorf("expected 1 node (within budget), got %d", len(reduced.Nodes))
	}
}

func TestDefaultReducerEmptyGraph(t *testing.T) {
	reducer := &DefaultReducer{}
	result, err := reducer.Reduce(context.Background(), nil, knowledge.TokenBudget{})
	if err != nil {
		t.Fatalf("Reduce error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for nil graph")
	}
}

func TestKnowledgeRuntimeEmptyGoal(t *testing.T) {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&testGraphProvider{name: "test"})
	sd := planner.NewSourceDiscovery(reg, &testQueryPlanner{})
	p := planner.NewKnowledgePlanner()

	rt := New(p, sd, reg, nil, nil, nil)
	_, err := rt.Execute(context.Background(), "", knowledge.TokenBudget{}, nil)
	if err == nil {
		t.Error("expected error for empty goal")
	}
}

func TestKnowledgeRuntimeFullPipeline(t *testing.T) {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&testGraphProvider{
		name: "memory",
		objects: []*knowledge.KnowledgeObject{
			{ID: "d1", Type: knowledge.ObjectDecision, Summary: "Chose Redis", Confidence: 0.9, Tags: []string{"redis"}},
			{ID: "d2", Type: knowledge.ObjectDecision, Summary: "Chose Postgres", Confidence: 0.8, Tags: []string{"postgres"}},
		},
	})

	sd := planner.NewSourceDiscovery(reg, &testQueryPlanner{})
	p := planner.NewKnowledgePlanner()

	rt := New(p, sd, reg, nil, []Linker{&DefaultLinker{}}, []Reducer{&DefaultReducer{}})
	graph, err := rt.Execute(context.Background(), "Why Redis?", knowledge.TokenBudget{MaxTokens: 2000, ForGraph: 1000}, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if graph == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(graph.Nodes) == 0 {
		t.Error("expected at least one node")
	}
}

// TestKnowledgeRuntimeLazyLoading locks in the lazy-loading clamp: when
// LazyLoading is set, budget.ForGraph is capped at maxLazyForGraph before
// Reduce, so the returned graph is smaller. When LazyLoading is false (or the
// budget is already at/below the cap), the budget passes through unchanged and
// the graph size matches the raw budget.
func TestKnowledgeRuntimeLazyLoading(t *testing.T) {
	// 60 objects: DefaultReducer's ~50 tokens/node estimate yields 40 nodes at
	// maxLazyForGraph (2000) and 20 nodes at a raw budget of 1000, so both the
	// clamped and the un-clamped paths produce distinct, predictable sizes.
	const nObjects = 60
	objects := make([]*knowledge.KnowledgeObject, 0, nObjects)
	for i := range nObjects {
		objects = append(objects, &knowledge.KnowledgeObject{
			ID:         fmt.Sprintf("obj-%d", i),
			Type:       knowledge.ObjectDecision,
			Summary:    fmt.Sprintf("decision node %d", i),
			Confidence: 0.9,
			Tags:       []string{"domain:cache"},
		})
	}

	run := func(t *testing.T, lazy bool, forGraph int) int {
		t.Helper()
		reg := provider.NewProviderRegistry()
		if err := reg.Register(&testGraphProvider{name: "memory", objects: objects}); err != nil {
			t.Fatalf("register provider: %v", err)
		}
		rt := New(
			planner.NewKnowledgePlanner(),
			planner.NewSourceDiscovery(reg, &testQueryPlanner{}),
			reg,
			nil,
			[]Linker{&DefaultLinker{}},
			[]Reducer{&DefaultReducer{}},
		)
		graph, err := rt.Execute(context.Background(), "why redis",
			knowledge.TokenBudget{ForGraph: forGraph},
			&Config{MaxConcurrentProviders: 5, LazyLoading: lazy})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		return len(graph.Nodes)
	}

	tests := []struct {
		name     string
		lazy     bool
		forGraph int
		want     int
	}{
		{"non_lazy_keeps_all_nodes", false, 10000, nObjects},
		{"lazy_clamps_large_budget", true, 10000, maxLazyForGraph / 50},
		{"lazy_budget_below_cap_unchanged", true, 1000, 20},
		{"non_lazy_small_budget_matches_lazy", false, 1000, 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(t, tc.lazy, tc.forGraph); got != tc.want {
				t.Errorf("expected %d graph nodes, got %d", tc.want, got)
			}
		})
	}
}

// testQueryPlanner for runtime tests.
type testQueryPlanner struct{}

func (q *testQueryPlanner) PlanQuery(_ context.Context, req planner.KnowledgeRequirement, _, _ string) (*planner.QueryPlan, error) {
	return &planner.QueryPlan{Query: "test", QueryType: planner.QuerySQL, MaxResults: req.MaxResults}, nil
}

// testGraphProvider for runtime tests.
type testGraphProvider struct {
	name    string
	objects []*knowledge.KnowledgeObject
}

func (p *testGraphProvider) Name() string                           { return p.name }
func (p *testGraphProvider) IntentMatch(_ knowledge.Intent) float64 { return 0.9 }
func (p *testGraphProvider) Stream(_ context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	ch := make(chan *knowledge.KnowledgeObject, len(p.objects))
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, obj := range p.objects {
			ch <- obj
		}
	}()
	return ch, errCh
}
