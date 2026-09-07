package embedding

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/api/embedding"
	"github.com/Timwood0x10/ares/internal/errors"
)

// EmbeddingPipeline centralizes embedding generation and spec metadata.
// Only this pipeline should call EmbedWithPrefix for memory/retrieval paths.
type EmbeddingPipeline interface {
	// BuildSpec creates a canonical EmbeddingSpec for the given kind and payload.
	BuildSpec(kind EmbeddingKind, payload any) (EmbeddingSpec, error)

	// Embed generates a vector from the spec's text using the spec's prefix.
	// The spec must have been built by a canonical builder.
	Embed(ctx context.Context, spec EmbeddingSpec) ([]float64, error)

	// Model returns the embedding model name used by this pipeline.
	Model() string
}

type embeddingPipeline struct {
	svc   embedding.EmbeddingService
	model string
	dim   int
}

// NewEmbeddingPipeline creates a pipeline wrapping the given service.
func NewEmbeddingPipeline(svc embedding.EmbeddingService) (EmbeddingPipeline, error) {
	if svc == nil {
		return nil, errors.New("embedding service is nil")
	}
	return &embeddingPipeline{
		svc:   svc,
		model: svc.GetModel(),
		dim:   0,
	}, nil
}

// BuildSpec dispatches to the canonical builder for the given kind.
func (p *embeddingPipeline) BuildSpec(kind EmbeddingKind, payload any) (EmbeddingSpec, error) {
	switch kind {
	case KindMemoryQuery:
		query, ok := payload.(string)
		if !ok {
			return EmbeddingSpec{}, errors.New("memory query spec requires string payload")
		}
		return BuildMemoryQuerySpec(query, p.model, 1, 0), nil

	case KindMemoryExperience:
		inp, ok := payload.(MemoryExperienceInput)
		if !ok {
			return EmbeddingSpec{}, errors.New("memory experience spec requires MemoryExperienceInput payload")
		}
		return BuildMemoryExperienceSpec(inp.MemoryType, inp.Problem, inp.Solution, p.model, 1, 0), nil

	default:
		return EmbeddingSpec{}, fmt.Errorf("unknown embedding kind: %s", kind)
	}
}

// Embed generates a vector using the configured service.
func (p *embeddingPipeline) Embed(ctx context.Context, spec EmbeddingSpec) ([]float64, error) {
	if spec.Text == "" {
		return nil, errors.New("embedding spec text is empty")
	}
	if spec.Prefix == "" {
		return nil, errors.New("embedding spec prefix is empty")
	}
	vec, err := p.svc.EmbedWithPrefix(ctx, spec.Text, spec.Prefix)
	if err != nil {
		return nil, fmt.Errorf("embed %s: %w", spec.Kind, err)
	}
	return vec, nil
}

// Model returns the embedding model name.
func (p *embeddingPipeline) Model() string {
	return p.model
}
