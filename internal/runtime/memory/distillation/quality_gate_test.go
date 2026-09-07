package distillation

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
)

// spyEmbedder wraps MockEmbeddingService to track calls to EmbedWithPrefix.
// Used by quality gate tests to verify the pipeline is used instead of the raw embedder.
type spyEmbedder struct {
	MockEmbeddingService
	embedWithPrefixCalls atomic.Int64
}

func (s *spyEmbedder) EmbedWithPrefix(ctx context.Context, text, prefix string) ([]float64, error) {
	s.embedWithPrefixCalls.Add(1)
	return s.MockEmbeddingService.EmbedWithPrefix(ctx, text, prefix)
}

// mockPipeline implements memembed.EmbeddingPipeline for testing.
type mockPipeline struct {
	embedCalls atomic.Int64
	buildCalls atomic.Int64
}

func (p *mockPipeline) BuildSpec(kind memembed.EmbeddingKind, payload any) (memembed.EmbeddingSpec, error) {
	p.buildCalls.Add(1)
	switch kind {
	case memembed.KindMemoryQuery:
		q, _ := payload.(string)
		return memembed.BuildMemoryQuerySpec(q, "mock-pipeline", 1, 0), nil
	case memembed.KindMemoryExperience:
		inp, _ := payload.(memembed.MemoryExperienceInput)
		return memembed.BuildMemoryExperienceSpec(inp.MemoryType, inp.Problem, inp.Solution, "mock-pipeline", 1, 0), nil
	default:
		return memembed.EmbeddingSpec{}, nil
	}
}

func (p *mockPipeline) Embed(_ context.Context, _ memembed.EmbeddingSpec) ([]float64, error) {
	p.embedCalls.Add(1)
	return []float64{0.1, 0.2, 0.3}, nil
}

func (p *mockPipeline) Model() string { return "mock-pipeline" }

// TestQualityGate_DistillerUsesPipelineNotRawEmbedder verifies that when the
// distiller has a pipeline set, it calls the pipeline.Embed method instead of
// calling the raw embedder's EmbedWithPrefix directly.
func TestQualityGate_DistillerUsesPipelineNotRawEmbedder(t *testing.T) {
	config := DefaultDistillationConfig()
	embedder := &spyEmbedder{}
	embedder.embeddings = make(map[string][]float64)
	repo := NewMockExperienceRepository([]Experience{})
	distiller := NewDistiller(config, embedder, repo)

	pipeline := &mockPipeline{}
	distiller.SetEmbeddingPipeline(pipeline)

	messages := []Message{
		{Role: "user", Content: "I have an error in my code"},
		{Role: "assistant", Content: "Fix the syntax error on line 10"},
	}

	ctx := context.Background()
	memories, err := distiller.DistillConversation(ctx, "test-conv-1", messages, "default", "user1")
	require.NoError(t, err)
	require.NotEmpty(t, memories, "should distill at least one memory")

	// The pipeline must be called for embedding.
	assert.Greater(t, pipeline.embedCalls.Load(), int64(0),
		"pipeline.Embed must be called when pipeline is set")

	// The raw embedder must NOT be called.
	assert.Equal(t, int64(0), embedder.embedWithPrefixCalls.Load(),
		"raw EmbedWithPrefix must NOT be called when pipeline is set")

	// BuildSpec must be called for each memory candidate.
	assert.Greater(t, pipeline.buildCalls.Load(), int64(0),
		"pipeline.BuildSpec must be called for each memory candidate")
}

// TestQualityGate_DistillerFallsBackToEmbedder verifies that when no pipeline
// is set, the distiller falls back to the raw embedder (backward compatibility).
func TestQualityGate_DistillerFallsBackToEmbedder(t *testing.T) {
	config := DefaultDistillationConfig()
	embedder := &spyEmbedder{}
	embedder.embeddings = make(map[string][]float64)
	repo := NewMockExperienceRepository([]Experience{})
	distiller := NewDistiller(config, embedder, repo)

	messages := []Message{
		{Role: "user", Content: "I have an error in my code"},
		{Role: "assistant", Content: "Fix the syntax error on line 10"},
	}

	ctx := context.Background()
	memories, err := distiller.DistillConversation(ctx, "test-conv-1", messages, "default", "user1")
	require.NoError(t, err)
	require.NotEmpty(t, memories, "should distill at least one memory")

	// Without pipeline, the raw embedder should be called.
	assert.Greater(t, embedder.embedWithPrefixCalls.Load(), int64(0),
		"raw EmbedWithPrefix should be called when no pipeline is set")
}
