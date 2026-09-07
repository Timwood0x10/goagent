package embedding

import (
	"context"
	"testing"
	"time"
)

// testEmbedder is a minimal EmbeddingService mock for tests.
type testEmbedder struct{}

func (t *testEmbedder) Embed(_ context.Context, _ string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedWithPrefix(_ context.Context, _, _ string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedBatch(_ context.Context, _ []string) ([][]float64, error) {
	return [][]float64{{0.1, 0.2, 0.3}}, nil
}

func (t *testEmbedder) HealthCheck(_ context.Context) error { return nil }

func (t *testEmbedder) GetModel() string { return "test-model" }

func (t *testEmbedder) GetTimeout() time.Duration { return 0 }

func TestEmbeddingPipeline_BuildAndEmbed(t *testing.T) {
	p := newTestPipeline(t)

	spec := BuildMemoryQuerySpec("find me a Go REST API example", "test-model", 1, 128)

	vec, err := p.Embed(context.Background(), spec)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(vec) == 0 {
		t.Fatal("expected non-empty vector")
	}
}

func TestEmbeddingPipeline_EmptyText(t *testing.T) {
	p := newTestPipeline(t)

	spec := BuildMemoryQuerySpec("", "test-model", 1, 128)

	_, err := p.Embed(context.Background(), spec)
	if err == nil {
		t.Error("expected error for empty text")
	}
}

func TestEmbeddingPipeline_EmptyPrefix(t *testing.T) {
	p := newTestPipeline(t)

	spec := BuildMemoryQuerySpec("query", "test-model", 1, 128)
	spec.Prefix = ""

	_, err := p.Embed(context.Background(), spec)
	if err == nil {
		t.Error("expected error for empty prefix")
	}
}

func TestEmbeddingPipeline_ModelPropagation(t *testing.T) {
	p := newTestPipeline(t)

	spec := BuildMemoryExperienceSpec("knowledge", "problem", "solution", "test-model", 1, 128)

	vec, err := p.Embed(context.Background(), spec)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(vec) == 0 {
		t.Fatal("expected non-empty vector")
	}
}

func TestEmbeddingPipeline_Model(t *testing.T) {
	p := newTestPipeline(t)
	if got := p.Model(); got != "test-model" {
		t.Errorf("expected test-model, got %s", got)
	}
}

func TestEmbeddingPipeline_BuildSpec_MemoryQuery(t *testing.T) {
	p := newTestPipeline(t)

	spec, err := p.BuildSpec(KindMemoryQuery, "find me Go examples")
	if err != nil {
		t.Fatalf("BuildSpec failed: %v", err)
	}

	if spec.Kind != KindMemoryQuery {
		t.Errorf("expected kind memory_query, got %s", spec.Kind)
	}

	if spec.Text != "find me Go examples" {
		t.Errorf("unexpected text: %s", spec.Text)
	}

	if spec.Prefix != "query:" {
		t.Errorf("unexpected prefix: %s", spec.Prefix)
	}

	if spec.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestEmbeddingPipeline_BuildSpec_MemoryExperience(t *testing.T) {
	p := newTestPipeline(t)

	payload := MemoryExperienceInput{
		MemoryType: "knowledge",
		Problem:    "How to create a Go API",
		Solution:   "Use gin framework",
	}

	spec, err := p.BuildSpec(KindMemoryExperience, payload)
	if err != nil {
		t.Fatalf("BuildSpec failed: %v", err)
	}

	if spec.Kind != KindMemoryExperience {
		t.Errorf("expected kind memory_experience, got %s", spec.Kind)
	}

	if spec.Prefix != "memory:" {
		t.Errorf("unexpected prefix: %s", spec.Prefix)
	}

	expectedText := "MemoryType: knowledge\nProblem: How to create a Go API\nSolution: Use gin framework"
	if spec.Text != expectedText {
		t.Errorf("unexpected text:\ngot:  %q\nwant: %q", spec.Text, expectedText)
	}

	if spec.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestEmbeddingPipeline_BuildSpec_InvalidKind(t *testing.T) {
	p := newTestPipeline(t)

	_, err := p.BuildSpec("invalid_kind", "data")
	if err == nil {
		t.Error("expected error for invalid kind")
	}
}

func TestEmbeddingPipeline_BuildSpec_QueryWrongPayload(t *testing.T) {
	p := newTestPipeline(t)

	_, err := p.BuildSpec(KindMemoryQuery, 42)
	if err == nil {
		t.Error("expected error for non-string query payload")
	}
}

func TestEmbeddingPipeline_BuildSpec_ExperienceWrongPayload(t *testing.T) {
	p := newTestPipeline(t)

	_, err := p.BuildSpec(KindMemoryExperience, "not a struct")
	if err == nil {
		t.Error("expected error for non-MemoryExperienceInput payload")
	}
}

func TestNewEmbeddingPipeline_NilGuard(t *testing.T) {
	_, err := NewEmbeddingPipeline(nil)
	if err == nil {
		t.Error("expected error for nil service")
	}
}

func TestEmbeddingPipeline_BuildSpec_ExperienceFromPipeline(t *testing.T) {
	p := newTestPipeline(t)

	spec, err := p.BuildSpec(KindMemoryExperience, MemoryExperienceInput{
		MemoryType: "preference",
		Problem:    "User prefers dark mode",
		Solution:   "Set theme to dark",
	})
	if err != nil {
		t.Fatalf("BuildSpec failed: %v", err)
	}

	// Ensure model is automatically set from pipeline.
	if spec.Model != "test-model" {
		t.Errorf("expected model test-model from pipeline, got %s", spec.Model)
	}

	// Ensure version 1.
	if spec.Version != 1 {
		t.Errorf("expected version 1, got %d", spec.Version)
	}
}

func TestEmbeddingPipeline_BuildSpec_QueryFromPipeline(t *testing.T) {
	p := newTestPipeline(t)

	spec, err := p.BuildSpec(KindMemoryQuery, "query text")
	if err != nil {
		t.Fatalf("BuildSpec failed: %v", err)
	}

	if spec.Model != "test-model" {
		t.Errorf("expected model test-model from pipeline, got %s", spec.Model)
	}

	if spec.Version != 1 {
		t.Errorf("expected version 1, got %d", spec.Version)
	}
}

func newTestPipeline(t *testing.T) EmbeddingPipeline {
	t.Helper()
	p, err := NewEmbeddingPipeline(&testEmbedder{})
	if err != nil {
		t.Fatalf("NewEmbeddingPipeline failed: %v", err)
	}
	return p
}
