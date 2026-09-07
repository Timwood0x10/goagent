// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"context"
	"time"
)

// MockEmbeddingService is a mock implementation of EmbeddingService for testing.
type MockEmbeddingService struct {
	embeddings map[string][]float64
}

func NewMockEmbeddingService() *MockEmbeddingService {
	return &MockEmbeddingService{
		embeddings: make(map[string][]float64),
	}
}

func (m *MockEmbeddingService) Embed(ctx context.Context, text string) ([]float64, error) {
	return m.EmbedWithPrefix(ctx, text, "query:")
}

func (m *MockEmbeddingService) EmbedWithPrefix(ctx context.Context, text, prefix string) ([]float64, error) {
	key := prefix + text
	if vec, ok := m.embeddings[key]; ok {
		return vec, nil
	}
	// Return a simple mock vector
	return []float64{0.5, 0.5, 0.5, 0.5}, nil
}

func (m *MockEmbeddingService) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	embeddings := make([][]float64, len(texts))
	for i, text := range texts {
		embeddings[i], _ = m.Embed(ctx, text)
	}
	return embeddings, nil
}

func (m *MockEmbeddingService) HealthCheck(ctx context.Context) error {
	return nil
}

func (m *MockEmbeddingService) GetModel() string {
	return "mock-model"
}

func (m *MockEmbeddingService) GetTimeout() time.Duration {
	return 30 * time.Second
}

// MockExperienceRepository is a mock implementation of ExperienceRepository for testing.
type MockExperienceRepository struct {
	experiences []Experience
}

func NewMockExperienceRepository(experiences []Experience) *MockExperienceRepository {
	return &MockExperienceRepository{
		experiences: experiences,
	}
}

func (m *MockExperienceRepository) SearchByVector(ctx context.Context, vector []float64, tenantID string, limit int) ([]Experience, error) {
	if len(m.experiences) == 0 {
		return []Experience{}, nil
	}
	return m.experiences[:minInt(limit, len(m.experiences))], nil
}

func (m *MockExperienceRepository) GetByMemoryType(ctx context.Context, tenantID string, memoryType MemoryType) ([]Experience, error) {
	return m.experiences, nil
}

func (m *MockExperienceRepository) CountByMemoryType(ctx context.Context, tenantID string, memoryType MemoryType) (int, error) {
	return len(m.experiences), nil
}

func (m *MockExperienceRepository) Update(ctx context.Context, experience *Experience) error {
	return nil
}

func (m *MockExperienceRepository) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *MockExperienceRepository) DeleteBatch(ctx context.Context, ids []string) error {
	return nil
}

func (m *MockExperienceRepository) Create(ctx context.Context, experience *Experience) error {
	m.experiences = append(m.experiences, *experience)
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
