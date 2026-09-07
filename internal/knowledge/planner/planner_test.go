package planner

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/knowledge/provider/memory"
)

// testQueryPlanner returns predictable query plans.
type testQueryPlanner struct{}

func (q *testQueryPlanner) PlanQuery(_ context.Context, req KnowledgeRequirement, providerName string, providerType string) (*QueryPlan, error) {
	return &QueryPlan{
		Query:      "SELECT * FROM " + providerType + " WHERE need='" + string(req.Need) + "'",
		QueryType:  QuerySQL,
		MaxResults: req.MaxResults,
	}, nil
}

func TestKnowledgePlannerPlan(t *testing.T) {
	planner := NewKnowledgePlanner()
	budget := knowledge.TokenBudget{MaxTokens: 2000, Reserved: 1000, ForGraph: 1000}

	plan, err := planner.Plan(context.Background(), "Why Redis?", budget)
	if err != nil {
		t.Fatalf("Plan error: %v", err)
	}

	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if plan.TokenBudget.ForGraph != 1000 {
		t.Errorf("expected ForGraph=1000, got %d", plan.TokenBudget.ForGraph)
	}
	if len(plan.Requirements) == 0 {
		t.Error("expected at least one requirement")
	}
}

func TestKnowledgePlannerEmptyGoal(t *testing.T) {
	planner := NewKnowledgePlanner()
	_, err := planner.Plan(context.Background(), "", knowledge.TokenBudget{})
	if err == nil {
		t.Error("expected error for empty goal")
	}
}

func TestKnowledgePlannerRequirements(t *testing.T) {
	planner := NewKnowledgePlanner()
	plan, _ := planner.Plan(context.Background(), "architecture design for the new stack", knowledge.TokenBudget{})

	needs := make(map[NeedType]bool)
	for _, req := range plan.Requirements {
		needs[req.Need] = true
	}

	if !needs[NeedDecision] {
		t.Error("expected NeedDecision requirement")
	}
	if !needs[NeedHistory] {
		t.Error("expected NeedHistory requirement")
	}
	if !needs[NeedArchitecture] {
		t.Error("expected NeedArchitecture requirement")
	}
}

func TestSourceDiscoveryNoProviders(t *testing.T) {
	reg := provider.NewProviderRegistry()
	qp := &testQueryPlanner{}
	sd := NewSourceDiscovery(reg, qp)

	sources, err := sd.Discover(context.Background(), []KnowledgeRequirement{
		{Need: NeedDecision, MaxResults: 10},
	}, knowledge.TokenBudget{})

	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("expected 0 sources with no providers, got %d", len(sources))
	}
}

func TestSourceDiscoveryWithProvider(t *testing.T) {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&testGraphProvider{
		name:  "memory",
		score: 0.8,
	})

	qp := &testQueryPlanner{}
	sd := NewSourceDiscovery(reg, qp)

	sources, err := sd.Discover(context.Background(), []KnowledgeRequirement{
		{Need: NeedDecision, Description: "test decision", MaxResults: 10},
	}, knowledge.TokenBudget{})

	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("expected at least 1 source")
	}
	if sources[0].ProviderName != "memory" {
		t.Errorf("expected 'memory' provider, got '%s'", sources[0].ProviderName)
	}
	if sources[0].Query == nil {
		t.Fatal("expected non-nil query plan")
	}
	if sources[0].Query.QueryType != QuerySQL {
		t.Errorf("expected QuerySQL, got '%s'", sources[0].Query.QueryType)
	}
}

// fakeTaskSearcher is a no-op TaskSearcher for MemoryProvider in tests.
// Discover only calls IntentMatch/PlanQuery, never Stream, so it is sufficient.
type fakeTaskSearcher struct{}

func (f *fakeTaskSearcher) SearchSimilarTasks(_ context.Context, _ string, _ int) ([]memory.SearchResult, error) {
	return nil, nil
}

// TestSourceDiscoveryExcludesMemoryForCode verifies the root-cause fix:
// the intent now carries Scope.Types derived from the requirement Need, so
// MemoryProvider's type-aware score (0.3 for code/architecture) falls below
// providerSelectThreshold (0.35) and the provider is excluded from code
// queries, while remaining selected for decision queries (score 0.8).
func TestSourceDiscoveryExcludesMemoryForCode(t *testing.T) {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(memory.New("memory", &fakeTaskSearcher{}))

	qp := &testQueryPlanner{}
	sd := NewSourceDiscovery(reg, qp)

	codeSources, err := sd.Discover(context.Background(), []KnowledgeRequirement{
		{Need: NeedCode, Description: "implement the cache layer", MaxResults: 10},
	}, knowledge.TokenBudget{})
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	for _, s := range codeSources {
		if s.ProviderName == "memory" {
			t.Errorf("MemoryProvider must NOT be selected for a code need (threshold %.2f excludes its 0.3 score)", providerSelectThreshold)
		}
	}

	decisionSources, err := sd.Discover(context.Background(), []KnowledgeRequirement{
		{Need: NeedDecision, Description: "decide on caching strategy", MaxResults: 10},
	}, knowledge.TokenBudget{})
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	found := false
	for _, s := range decisionSources {
		if s.ProviderName == "memory" {
			found = true
		}
	}
	if !found {
		t.Error("MemoryProvider must be selected for a decision need (score 0.8 >= threshold)")
	}
}

func TestSourceDiscoveryEmptyRequirements(t *testing.T) {
	reg := provider.NewProviderRegistry()
	qp := &testQueryPlanner{}
	sd := NewSourceDiscovery(reg, qp)

	sources, err := sd.Discover(context.Background(), nil, knowledge.TokenBudget{})
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("expected 0 sources for empty requirements, got %d", len(sources))
	}
}

func TestSourceDiscoveryPriorities(t *testing.T) {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&testGraphProvider{name: "provider-a", score: 0.9})
	_ = reg.Register(&testGraphProvider{name: "provider-b", score: 0.7})

	qp := &testQueryPlanner{}
	sd := NewSourceDiscovery(reg, qp)

	sources, _ := sd.Discover(context.Background(), []KnowledgeRequirement{
		{Need: NeedDecision, Priority: 2, MaxResults: 10},
		{Need: NeedHistory, Priority: 1, MaxResults: 10},
	}, knowledge.TokenBudget{})

	// Sources should be sorted by priority ascending.
	if len(sources) < 2 {
		t.Fatalf("expected at least 2 sources, got %d", len(sources))
	}
}

func TestQueryPlanCreation(t *testing.T) {
	qp := &testQueryPlanner{}
	plan, err := qp.PlanQuery(context.Background(),
		KnowledgeRequirement{Need: NeedArchitecture, MaxResults: 15},
		"pg_provider", "postgres",
	)
	if err != nil {
		t.Fatalf("PlanQuery error: %v", err)
	}
	if plan.QueryType != QuerySQL {
		t.Errorf("expected QuerySQL, got '%s'", plan.QueryType)
	}
	if plan.MaxResults != 15 {
		t.Errorf("expected MaxResults=15, got %d", plan.MaxResults)
	}
}

func TestNeedTypeConstants(t *testing.T) {
	types := []NeedType{NeedArchitecture, NeedDecision, NeedHistory, NeedCode, NeedIssue, NeedPerformance}
	for _, nt := range types {
		if nt == "" {
			t.Error("NeedType constant should not be empty")
		}
	}
}

func TestQueryTypeConstants(t *testing.T) {
	types := []QueryType{QuerySQL, QueryCypher, QueryVector, QueryMemory, QueryKeyword}
	for _, qt := range types {
		if qt == "" {
			t.Error("QueryType constant should not be empty")
		}
	}
}

// testGraphProvider implements provider.GraphProvider for testing.
type testGraphProvider struct {
	name  string
	score float64
}

func (p *testGraphProvider) Name() string { return p.name }

func (p *testGraphProvider) IntentMatch(_ knowledge.Intent) float64 { return p.score }

func (p *testGraphProvider) Stream(_ context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	ch := make(chan *knowledge.KnowledgeObject)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}

// typedStub is a GraphProvider that exposes a configured ProviderType, for
// verifying detectProviderType dispatches through the TypedProvider interface.
type typedStub struct {
	name string
	pt   provider.ProviderType
}

func (s *typedStub) Name() string { return s.name }
func (s *typedStub) IntentMatch(_ knowledge.Intent) float64 {
	if s.pt == "" {
		return 0
	}
	return 0.9
}
func (s *typedStub) Stream(_ context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	ch := make(chan *knowledge.KnowledgeObject)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}
func (s *typedStub) ProviderType() provider.ProviderType { return s.pt }

// untypedStub is a GraphProvider that does NOT implement TypedProvider, for
// verifying detectProviderType falls back to "" for unknown providers.
type untypedStub struct {
	name string
}

func (u *untypedStub) Name() string                           { return u.name }
func (u *untypedStub) IntentMatch(_ knowledge.Intent) float64 { return 0.5 }
func (u *untypedStub) Stream(_ context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	ch := make(chan *knowledge.KnowledgeObject)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}

// TestDetectProviderType verifies detectProviderType reads the backing type
// from each provider via the TypedProvider interface, and returns "" for
// providers that do not expose a type. Previously detectProviderType just
// returned the provider's name (unimplemented).
func TestDetectProviderType(t *testing.T) {
	tests := []struct {
		name     string
		provider provider.GraphProvider
		expected string
	}{
		{"memory", &typedStub{name: "m", pt: provider.ProviderMemory}, string(provider.ProviderMemory)},
		{"evolution", &typedStub{name: "e", pt: provider.ProviderEvolution}, string(provider.ProviderEvolution)},
		{"postgres", &typedStub{name: "pg", pt: provider.ProviderPostgres}, string(provider.ProviderPostgres)},
		{"vector", &typedStub{name: "v", pt: provider.ProviderVector}, string(provider.ProviderVector)},
		{"store", &typedStub{name: "s", pt: provider.ProviderStore}, string(provider.ProviderStore)},
		{"code_provider", &typedStub{name: "c", pt: provider.ProviderCode}, string(provider.ProviderCode)},
		{"untyped", &untypedStub{name: "custom"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectProviderType(tt.provider)
			if got != tt.expected {
				t.Errorf("detectProviderType(%s) = %q, want %q", tt.name, got, tt.expected)
			}
		})
	}
}

// TestDetectProviderType_RealMemoryProvider verifies a real constructed
// MemoryProvider is detected as "memory" through the TypedProvider interface
// (integration check that the production provider is wired, not just stubs).
func TestDetectProviderType_RealMemoryProvider(t *testing.T) {
	mp := memory.New("memory", &fakeTaskSearcher{})
	if got := detectProviderType(mp); got != "memory" {
		t.Errorf("detectProviderType(memory.MemoryProvider) = %q, want %q", got, "memory")
	}
}
