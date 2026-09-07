package knowledge

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func TestKnowledgeObject(t *testing.T) {
	now := time.Now()
	obj := &KnowledgeObject{
		ID:         "obj_001",
		Type:       ObjectDecision,
		Summary:    "Chose Redis for caching layer",
		Normalized: "Redis was chosen as the primary caching solution due to its low latency and rich data structures.",
		Raw:        []byte("raw decision record bytes"),
		Confidence: 0.95,
		Version:    1,
		CreatedAt:  now,
		UpdatedAt:  now,
		Evidence: []Evidence{
			{Source: "memory", Ref: "conv_abc123", Weight: 1.0, Timestamp: now},
		},
		Representations: map[string]string{
			"openai-text-3-large": "rep_001",
		},
	}

	if obj.ID != "obj_001" {
		t.Errorf("expected obj_001, got %s", obj.ID)
	}
	if obj.Type != ObjectDecision {
		t.Errorf("expected ObjectDecision, got %s", obj.Type)
	}
	if obj.Summary != "Chose Redis for caching layer" {
		t.Errorf("unexpected summary: %s", obj.Summary)
	}
	if len(obj.Evidence) != 1 {
		t.Errorf("expected 1 evidence, got %d", len(obj.Evidence))
	}
	if obj.Representations["openai-text-3-large"] != "rep_001" {
		t.Errorf("unexpected representation mapping")
	}
}

func TestRepresentation(t *testing.T) {
	rep := &Representation{
		ID:        "rep_001",
		ObjectID:  "obj_001",
		Model:     "openai-text-3-large",
		Dimension: 1536,
		Vector:    make([]float32, 1536),
	}
	rep.Vector[0] = 0.5
	rep.Vector[1535] = -0.5

	if rep.Model != "openai-text-3-large" {
		t.Errorf("unexpected model: %s", rep.Model)
	}
	if rep.Dimension != 1536 {
		t.Errorf("unexpected dimension: %d", rep.Dimension)
	}
	if rep.Vector[0] != 0.5 {
		t.Errorf("unexpected vector[0]: %f", rep.Vector[0])
	}
}

func TestMergeConfidence(t *testing.T) {
	tests := []struct {
		name string
		a, b float64
		want float64
	}{
		{"both_max_clamps_to_one", 1.0, 1.0, 1.0},
		{"high_plus_high_clamps", 0.95, 0.95, 1.0},
		{"unequal_prefers_higher", 0.8, 0.5, 0.85},
		{"equal_low_no_clamp", 0.4, 0.4, 0.44},
		{"zero_inputs_stays_zero", 0.0, 0.0, 0.0},
	}
	const eps = 1e-9
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeConfidence(tt.a, tt.b)
			if got > 1.0 {
				t.Fatalf("mergeConfidence(%v,%v) = %v, exceeds [0,1] contract", tt.a, tt.b, got)
			}
			if math.Abs(got-tt.want) > eps {
				t.Errorf("mergeConfidence(%v,%v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestRelationCustomType(t *testing.T) {
	// Users can define custom relation types without modifying AKF.
	r := Relation{
		From: "person_001",
		To:   "person_002",
		Name: "worked_with",
		Properties: map[string]any{
			"project": "ARES",
			"since":   "2025-01-01",
		},
		Score: 0.9,
	}

	if r.Name != "worked_with" {
		t.Errorf("expected custom relation name 'worked_with', got %s", r.Name)
	}
	if r.Properties["project"] != "ARES" {
		t.Errorf("unexpected property value")
	}
}

func TestBuiltinRelations(t *testing.T) {
	builtins := []string{
		RelDependsOn, RelCalls, RelCauses, RelFixes, RelBelongsTo,
		RelUses, RelImplements, RelSimilarTo, RelGeneratedBy,
		RelDecidedBy, RelSupersedes, RelLearnsFrom,
	}
	for _, name := range builtins {
		if name == "" {
			t.Error("built-in relation name should not be empty")
		}
	}
}

func TestWorkingGraph(t *testing.T) {
	graph := &WorkingGraph{
		Nodes: map[string]*KnowledgeObject{
			"obj_001": {ID: "obj_001", Type: ObjectDecision, Summary: "Redis choice"},
			"obj_002": {ID: "obj_002", Type: ObjectCode, Summary: "cache.go"},
		},
		Edges: []Relation{
			{From: "obj_002", To: "obj_001", Name: RelDependsOn},
		},
	}

	if len(graph.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(graph.Nodes))
	}
	if len(graph.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(graph.Edges))
	}
}

func TestIntent(t *testing.T) {
	intent := Intent{
		Goal: "Why Redis?",
		Scope: Scope{
			Types:      []ObjectType{ObjectDecision, ObjectArchitecture},
			MaxObjects: 100,
		},
		Budget: TokenBudget{
			MaxTokens: 2000,
			Reserved:  1000,
			ForGraph:  1000,
		},
	}

	if intent.Goal != "Why Redis?" {
		t.Errorf("unexpected goal: %s", intent.Goal)
	}
	if intent.Scope.MaxObjects != 100 {
		t.Errorf("unexpected max objects: %d", intent.Scope.MaxObjects)
	}
	if intent.Budget.ForGraph != 1000 {
		t.Errorf("unexpected graph budget: %d", intent.Budget.ForGraph)
	}
}

func TestKnowledgeObjectObjectTypes(t *testing.T) {
	types := map[ObjectType]bool{
		ObjectMemory:     true,
		ObjectUser:       true,
		ObjectProject:    true,
		ObjectCode:       true,
		ObjectIssue:      true,
		ObjectCommit:     true,
		ObjectDecision:   true,
		ObjectDocument:   true,
		ObjectToolResult: true,
		ObjectWorkflow:   true,
		ObjectRuntime:    true,
	}

	for _, valid := range types {
		if !valid {
			t.Error("all predefined types should be valid")
		}
	}
}

func TestKnowledgePipelineProcessesObject(t *testing.T) {
	pipeline := NewKnowledgePipeline(nil, nil, nil, nil)
	obj := &KnowledgeObject{
		ID:      "test_001",
		Type:    ObjectMemory,
		Summary: "test summary",
	}

	result, err := pipeline.Process(context.Background(), obj)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if result.ID != "test_001" {
		t.Errorf("expected test_001, got %s", result.ID)
	}
}

func TestKnowledgePipelineProcessStream(t *testing.T) {
	pipeline := NewKnowledgePipeline(nil, nil, nil, nil)
	in := make(chan *KnowledgeObject, 3)
	in <- &KnowledgeObject{ID: "a", Summary: "A"}
	in <- &KnowledgeObject{ID: "b", Summary: "B"}
	in <- &KnowledgeObject{ID: "c", Summary: "C"}
	close(in)

	out := pipeline.ProcessStream(context.Background(), in)
	count := 0
	for range out {
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 objects, got %d", count)
	}
}

func TestKnowledgePipelineWithNormalizer(t *testing.T) {
	normalizer := &testNormalizer{prefix: "norm:"}
	pipeline := NewKnowledgePipeline(
		[]Normalizer{normalizer},
		nil, nil, nil,
	)

	obj := &KnowledgeObject{
		ID:      "test",
		Summary: "raw summary",
	}
	result, err := pipeline.Process(context.Background(), obj)
	if err != nil {
		t.Fatalf("Process error: %v", err)
	}
	if result.Summary != "norm:raw summary" {
		t.Errorf("expected 'norm:raw summary', got '%s'", result.Summary)
	}
}

// testNormalizer prepends a prefix to Summary.
type testNormalizer struct {
	prefix string
}

func (n *testNormalizer) Name() string { return "test-normalizer" }
func (n *testNormalizer) Normalize(_ context.Context, obj *KnowledgeObject) (*KnowledgeObject, error) {
	obj.Summary = n.prefix + obj.Summary
	return obj, nil
}

// The following minimal implementations exercise KnowledgePipeline.Process
// with a configured matcher/validator so that the shared resolvedObjects map
// is read and written. Running this test under `go test -race` proves the
// data race is fixed.

type raceMatcher struct{}

func (raceMatcher) Name() string { return "race-matcher" }
func (raceMatcher) Match(_ context.Context, _ *KnowledgeObject, _ []*KnowledgeObject) (*ResolveResult, error) {
	return &ResolveResult{IsNew: true, Confidence: 0.5}, nil
}

type raceValidator struct{}

func (raceValidator) Name() string { return "race-validator" }
func (raceValidator) Validate(_ context.Context, _ *KnowledgeObject, _ []*KnowledgeObject) (*ValidationResult, error) {
	return &ValidationResult{Confidence: 0.5}, nil
}

type raceSummarizer struct{}

func (raceSummarizer) Name() string { return "race-summarizer" }
func (raceSummarizer) Summarize(_ context.Context, obj *KnowledgeObject) (*KnowledgeObject, error) {
	return obj, nil
}

// TestKnowledgePipelineProcessConcurrent verifies that Process is safe for
// concurrent use. The pipeline is invoked from many goroutines so the
// shared resolvedObjects candidate pool is read and written in parallel; the
// race detector fails the test if the access is not serialized by a mutex.
func TestKnowledgePipelineProcessConcurrent(t *testing.T) {
	pipeline := NewKnowledgePipeline(
		[]Normalizer{&testNormalizer{prefix: "n:"}},
		[]EntityMatcher{raceMatcher{}},
		[]Validator{raceValidator{}},
		[]Summarizer{raceSummarizer{}},
	)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			obj := &KnowledgeObject{ID: fmt.Sprintf("obj-%d", i), Summary: "body"}
			if _, err := pipeline.Process(context.Background(), obj); err != nil {
				t.Errorf("Process error: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := len(pipeline.resolvedObjects); got != 32 {
		t.Errorf("expected 32 resolved objects, got %d", got)
	}
}
