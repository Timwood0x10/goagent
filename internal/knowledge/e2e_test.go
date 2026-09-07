package knowledge_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/knowledge/retriever"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
	memorystore "github.com/Timwood0x10/ares/internal/knowledge/store/memory"
)

type e2eProvider struct {
	name    string
	objects []*knowledge.KnowledgeObject
}

func (p *e2eProvider) Name() string                           { return p.name }
func (p *e2eProvider) IntentMatch(_ knowledge.Intent) float64 { return 0.9 }
func (p *e2eProvider) Stream(_ context.Context, _ knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
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

type e2eQueryPlanner struct{}

func (q *e2eQueryPlanner) PlanQuery(_ context.Context, req planner.KnowledgeRequirement, _, _ string) (*planner.QueryPlan, error) {
	return &planner.QueryPlan{Query: req.Description, QueryType: planner.QuerySQL, MaxResults: req.MaxResults}, nil
}

// TestAKFFullE2E verifies the complete AKF pipeline:
//
//	Query → Planner → SourceDiscovery → Provider(Stream) → Pipeline → Link → Reduce → Compile
func TestAKFFullE2E(t *testing.T) {
	memProvider := &e2eProvider{
		name: "memory",
		objects: []*knowledge.KnowledgeObject{
			{
				ID: "mem:redis1", Type: knowledge.ObjectDecision, Namespace: "memory",
				Summary: "Chose Redis for caching layer", Normalized: "Redis is used as cache",
				Raw: []byte("Decision: Use Redis for caching"), Confidence: 0.9,
				Tags: []string{"redis", "cache", "decision"},
			},
			{
				ID: "mem:pg1", Type: knowledge.ObjectDecision, Namespace: "memory",
				Summary: "Chose PostgreSQL for storage", Normalized: "PostgreSQL is the primary DB",
				Raw: []byte("Decision: Use PostgreSQL for persistence"), Confidence: 0.85,
				Tags: []string{"postgres", "database", "decision"},
			},
		},
	}
	codeProvider := &e2eProvider{
		name: "code",
		objects: []*knowledge.KnowledgeObject{
			{
				ID: "code:redis_cache", Type: knowledge.ObjectCode, Namespace: "code",
				Summary: "Redis cache implementation in Go", Normalized: "cache.NewRedisCache()",
				Confidence: 0.95, Tags: []string{"redis", "golang", "cache"},
			},
		},
	}

	reg := provider.NewProviderRegistry()
	if err := reg.Register(memProvider); err != nil {
		t.Fatalf("register memory: %v", err)
	}
	if err := reg.Register(codeProvider); err != nil {
		t.Fatalf("register code: %v", err)
	}

	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 4096}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.6}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 100}},
	)

	qp := &e2eQueryPlanner{}
	sd := planner.NewSourceDiscovery(reg, qp)
	pl := planner.NewKnowledgePlanner()

	linkers := []runtime.Linker{&runtime.DefaultLinker{}}
	reducers := []runtime.Reducer{&runtime.DefaultReducer{}}
	rt := runtime.New(pl, sd, reg, pipe, linkers, reducers)

	comp := compiler.NewDefaultCompiler()
	ret := retriever.New(rt, comp)

	budget := knowledge.TokenBudget{
		MaxTokens: 5000,
		ForGraph:  3000,
		Reserved:  2000,
	}

	graph, err := rt.Execute(context.Background(), "Why Redis?", budget, &runtime.Config{
		MaxConcurrentProviders: 2,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(graph.Nodes) == 0 {
		t.Fatal("expected at least 1 node in graph")
	}
	t.Logf("Graph: %d nodes, %d edges", len(graph.Nodes), len(graph.Edges))

	foundRedis := false
	for id := range graph.Nodes {
		if strings.Contains(id, "redis") {
			foundRedis = true
		}
	}
	if !foundRedis {
		t.Error("expected at least one Redis-related node")
	}

	compiled, err := comp.Compile(context.Background(), graph, compiler.CompileConfig{
		Formats: []compiler.Format{compiler.FormatPrompt, compiler.FormatJSON},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(compiled.Formats) != 2 {
		t.Errorf("expected 2 formats, got %d", len(compiled.Formats))
	}
	promptContent, ok := compiled.Formats[compiler.FormatPrompt]
	if !ok || promptContent == "" {
		t.Error("expected non-empty Prompt content")
	}
	t.Logf("Compiled: %d tokens", compiled.Metrics.OutputTokens)

	retResult, err := ret.Retrieve(context.Background(), retriever.Query{
		Text: "Why Redis?", MaxResults: 10, MaxTokens: 4000,
		Formats: []compiler.Format{compiler.FormatPrompt},
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if retResult.Context == nil || retResult.Graph == nil {
		t.Fatal("retrieve returned nil context or graph")
	}
	if retResult.Query != "Why Redis?" {
		t.Errorf("expected query 'Why Redis?', got %q", retResult.Query)
	}
	t.Logf("Retrieve: %d nodes, %d tokens", len(retResult.Graph.Nodes), retResult.Context.Metrics.OutputTokens)
}

// TestAKFE2E_PipelineProcessing verifies Normalizer → Summarizer end-to-end.
func TestAKFE2E_PipelineProcessing(t *testing.T) {
	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 1024}},
		nil, nil,
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 30}},
	)

	obj := &knowledge.KnowledgeObject{
		ID:  "test:raw1",
		Raw: []byte("  This   is   a   VERY   long   text  \n\n  \t  indeed!  "),
	}

	result, err := pipe.Process(context.Background(), obj)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Normalized == "" {
		t.Error("Normalized should not be empty")
	}
	if result.Summary == "" {
		t.Error("Summary should not be empty")
	}
	if len(result.Summary) > 33 {
		t.Errorf("Summary too long (%d chars)", len(result.Summary))
	}
}

// TestAKFE2E_ProviderConcurrency verifies concurrent provider loading.
func TestAKFE2E_ProviderConcurrency(t *testing.T) {
	reg := provider.NewProviderRegistry()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("p%d", i)
		_ = reg.Register(&e2eProvider{
			name: name,
			objects: []*knowledge.KnowledgeObject{
				{ID: name + ":obj1", Summary: name + " object", Confidence: 0.8},
			},
		})
	}

	sd := planner.NewSourceDiscovery(reg, &e2eQueryPlanner{})
	pl := planner.NewKnowledgePlanner()
	rt := runtime.New(pl, sd, reg, nil, nil, nil)

	budget := knowledge.TokenBudget{MaxTokens: 5000, ForGraph: 3000, Reserved: 2000}
	graph, err := rt.Execute(context.Background(), "test", budget, &runtime.Config{
		MaxConcurrentProviders: 5,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(graph.Nodes) < 5 {
		t.Errorf("expected at least 5 nodes, got %d", len(graph.Nodes))
	}
}

// TestAKFE2E_CompileAllFormats verifies every compiler format works.
func TestAKFE2E_CompileAllFormats(t *testing.T) {
	graph := &knowledge.WorkingGraph{
		Nodes: map[string]*knowledge.KnowledgeObject{
			"n1": {ID: "n1", Type: knowledge.ObjectDecision, Summary: "Test", Confidence: 0.9},
		},
		Edges: []knowledge.Relation{},
	}

	comp := compiler.NewDefaultCompiler()
	allFormats := []compiler.Format{
		compiler.FormatPrompt, compiler.FormatMarkdown,
		compiler.FormatJSON, compiler.FormatXML, compiler.FormatToolSchema,
	}

	compiled, err := comp.Compile(context.Background(), graph, compiler.CompileConfig{Formats: allFormats})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, f := range allFormats {
		content, ok := compiled.Formats[f]
		if !ok || content == "" {
			t.Errorf("missing or empty format: %s", f)
		}
	}
}

// TestAKG_WriteReadLoop verifies the AKG write→read closed loop using only the
// knowledge package + memstore (no adapter DistillBridge, which may not
// exist yet in this deployment):
//
//  1. Save active KnowledgeObjects to the store (write side).
//  2. HybridSearch recalls them by goal text (read side).
//  3. Quality gate Evaluate → ComputeFinal → Promote transitions a candidate
//     to active, and ListByStatus(StatusActive) observes the promotion.
//  4. A candidate-status object must NOT leak into an active-only search.
//
// When adapter.NewDistillBridgeWithGate lands this test should gain a
// DistillBridge round-trip case.
func TestAKG_WriteReadLoop(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	// 1. Seed active objects (the write side of the loop).
	seed := []*knowledge.KnowledgeObject{
		{
			ID: "akg:auth:1", Type: knowledge.ObjectMemory, Namespace: "default",
			Summary:    "router.go fixes the auth bypass in middleware",
			Normalized: "router.go fixes auth bypass middleware",
			Status:     knowledge.StatusActive, Confidence: 0.8, CreatedAt: time.Now(),
		},
		{
			ID: "akg:cache:1", Type: knowledge.ObjectDecision, Namespace: "default",
			Summary:    "chose redis for the caching layer",
			Normalized: "redis caching layer decision",
			Status:     knowledge.StatusActive, Confidence: 0.85, CreatedAt: time.Now(),
		},
	}
	if err := ms.Save(ctx, seed...); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// 2. HybridSearch recalls the auth fact by goal text (read side).
	scored, err := ms.HybridSearch(ctx, knowledge.HybridSearchRequest{
		Query:        "auth bypass",
		Namespace:    "default",
		TopK:         10,
		FinalK:       5,
		MinScore:     0,
		Model:        "test-model",
		StatusFilter: []knowledge.ObjectStatus{knowledge.StatusActive},
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(scored) == 0 {
		t.Fatal("expected at least one recalled object, got none")
	}
	foundAuth := false
	for _, s := range scored {
		if s.Object != nil && s.Object.ID == "akg:auth:1" {
			foundAuth = true
		}
	}
	if !foundAuth {
		t.Error("expected akg:auth:1 to be recalled by 'auth bypass' query")
	}

	// 3. Quality gate: Evaluate a candidate, ComputeFinal, Promote, then
	//    ListByStatus(StatusActive) sees the promoted object.
	gate := knowledge.DefaultQualityGateConfig()
	candidate := &knowledge.KnowledgeObject{
		ID: "akg:candidate:1", Type: knowledge.ObjectDecision, Namespace: "default",
		Summary:    "switched session store from cookie to redis",
		Normalized: "session store switched from cookie to redis",
		Status:     knowledge.StatusCandidate,
		Relations:  []knowledge.Relation{{Predicate: knowledge.RelFixes, ObjectText: "cookie session bug"}},
		Evidence:   []knowledge.Evidence{{Source: "distill", Ref: "conv1", Weight: 0.8, Timestamp: time.Now()}},
		CreatedAt:  time.Now(),
	}
	if err := ms.Save(ctx, candidate); err != nil {
		t.Fatalf("candidate save: %v", err)
	}

	q := gate.Evaluate(candidate)
	if q == nil {
		t.Fatal("Evaluate returned nil Quality")
	}
	candidate.Quality = q
	finalScore := gate.ComputeFinal(q)
	candidate.Confidence = finalScore
	if finalScore < gate.MinFinalScore {
		t.Fatalf("final score %v below MinFinalScore %v", finalScore, gate.MinFinalScore)
	}

	if err := ms.Promote(ctx, candidate.ID, q); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	active, err := ms.ListByStatus(ctx, "default", knowledge.StatusActive, 100)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	foundPromoted := false
	for _, o := range active {
		if o.ID == "akg:candidate:1" && o.Status == knowledge.StatusActive {
			foundPromoted = true
		}
	}
	if !foundPromoted {
		t.Error("expected promoted candidate to appear in ListByStatus(StatusActive)")
	}

	// 4. A pure candidate (never promoted) must NOT leak into an active-only
	//    search — proving the status filter enforces the lifecycle boundary.
	pureCandidate := &knowledge.KnowledgeObject{
		ID: "akg:purecandidate:1", Type: knowledge.ObjectMemory, Namespace: "default",
		Summary:    "unverified rumour about connection pool",
		Normalized: "unverified rumour connection pool",
		Status:     knowledge.StatusCandidate,
		CreatedAt:  time.Now(),
	}
	if err := ms.Save(ctx, pureCandidate); err != nil {
		t.Fatalf("pure candidate save: %v", err)
	}
	res, err := ms.HybridSearch(ctx, knowledge.HybridSearchRequest{
		Query:        "connection pool rumour",
		Namespace:    "default",
		TopK:         10,
		FinalK:       10,
		MinScore:     0,
		Model:        "test-model",
		StatusFilter: []knowledge.ObjectStatus{knowledge.StatusActive},
	})
	if err != nil {
		t.Fatalf("HybridSearch pure candidate: %v", err)
	}
	for _, s := range res {
		if s.Object != nil && s.Object.ID == "akg:purecandidate:1" {
			t.Error("candidate-status object leaked into active-only search")
		}
	}
}
