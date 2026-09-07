package services

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/errors"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
)

// pipelineSpy implements memembed.EmbeddingPipeline for testing.
type pipelineSpy struct {
	buildCalls atomic.Int64
	embedCalls atomic.Int64
	model      string
}

func (p *pipelineSpy) BuildSpec(kind memembed.EmbeddingKind, payload any) (memembed.EmbeddingSpec, error) {
	p.buildCalls.Add(1)
	switch kind {
	case memembed.KindMemoryQuery:
		q, ok := payload.(string)
		if !ok {
			return memembed.EmbeddingSpec{}, errors.New("invalid query payload")
		}
		return memembed.BuildMemoryQuerySpec(q, p.model, 1, 0), nil
	default:
		return memembed.EmbeddingSpec{}, nil
	}
}

func (p *pipelineSpy) Embed(_ context.Context, _ memembed.EmbeddingSpec) ([]float64, error) {
	p.embedCalls.Add(1)
	return []float64{0.1, 0.2, 0.3}, nil
}

func (p *pipelineSpy) Model() string { return p.model }

// TestQualityGate_SimpleRetrievalUsesPipeline verifies that when a pipeline is set,
// SimpleRetrievalService.embedQuery uses the pipeline instead of calling the
// embedding client directly.
func TestQualityGate_SimpleRetrievalUsesPipeline(t *testing.T) {
	pipe := &pipelineSpy{model: "mock-model"}

	svc := &SimpleRetrievalService{
		config: &SimpleRetrievalConfig{QueryPrefix: "query:"},
	}
	svc.SetEmbeddingPipeline(pipe)

	vec, err := svc.embedQuery(context.Background(), "test query")
	assert.NoError(t, err)
	assert.NotNil(t, vec)
	assert.Len(t, vec, 3)

	// Pipeline must be called.
	assert.Greater(t, pipe.buildCalls.Load(), int64(0), "pipeline.BuildSpec must be called")
	assert.Greater(t, pipe.embedCalls.Load(), int64(0), "pipeline.Embed must be called")

	// Embedding client is nil, but embedQuery doesn't panic because pipeline is used.
}

// TestQualityGate_RetrievalServiceUsesPipeline verifies that when a pipeline is set,
// RetrievalService.getEmbedding uses the pipeline instead of calling the embedding
// client directly.
func TestQualityGate_RetrievalServiceUsesPipeline(t *testing.T) {
	pipe := &pipelineSpy{model: "mock-model"}

	svc := &RetrievalService{
		pipeline: pipe,
	}

	vec := svc.getEmbedding(context.Background(), "test query")
	assert.NotNil(t, vec)

	// Pipeline must be called.
	assert.Greater(t, pipe.buildCalls.Load(), int64(0), "pipeline.BuildSpec must be called")
	assert.Greater(t, pipe.embedCalls.Load(), int64(0), "pipeline.Embed must be called")

	// EmbeddingClient is nil, but getEmbedding doesn't panic because pipeline is used.
}

// TestQualityGate_RetrievalServiceFallsback verifies that without a pipeline
// or embedding client, getEmbedding returns nil without panicking.
func TestQualityGate_RetrievalServiceFallsback(t *testing.T) {
	svc := &RetrievalService{
		logger: slog.Default(),
	}

	vec := svc.getEmbedding(context.Background(), "test query")
	assert.Nil(t, vec, "should return nil when both pipeline and client are nil")
}
