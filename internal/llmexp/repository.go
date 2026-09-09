// Package llmexp provides the canonical experience-storage and
// memory-distillation DTOs.
//
// This file defines the ExperienceRepository interface, which is the
// storage-agnostic contract between the distillation pipeline and any
// backend vector database (PostgreSQL pgvector, SQLite-vec, Weaviate,
// Qdrant, Milvus, etc.).
//
// Modules implement this interface to bridge the distiller with
// their own vector store. The interface intentionally uses only primitive
// Go types and the DTOs defined in types.go, so it carries no ARES-internal
// dependencies.
package llmexp

import (
	"context"
)

// ExperienceRepository defines the interface for experience storage and
// retrieval. It is the storage-agnostic contract that decouples the
// distillation pipeline from any specific vector database.
//
// Implementations MUST be safe for concurrent use. All methods SHOULD
// honour ctx.Done() for cancellation.
type ExperienceRepository interface {
	// SearchByVector searches for similar experiences by embedding vector.
	//
	// Args:
	//   ctx - operation context.
	//   vector - query embedding vector.
	//   tenantID - tenant identifier for multi-tenancy isolation.
	//   limit - maximum number of results to return.
	//
	// Returns:
	//   []Experience - matching experiences ordered by similarity.
	//   error - any error encountered.
	SearchByVector(ctx context.Context, vector []float64, tenantID string, limit int) ([]Experience, error)

	// GetByMemoryType retrieves experiences by memory type for the given tenant.
	//
	// Args:
	//   ctx - operation context.
	//   tenantID - tenant identifier for multi-tenancy isolation.
	//   memoryType - the memory type to filter by.
	//
	// Returns:
	//   []Experience - matching experiences.
	//   error - any error encountered.
	GetByMemoryType(ctx context.Context, tenantID string, memoryType MemoryType) ([]Experience, error)

	// CountByMemoryType returns the number of experiences for the given
	// tenant and memory type.
	//
	// Args:
	//   ctx - operation context.
	//   tenantID - tenant identifier for multi-tenancy isolation.
	//   memoryType - the memory type to count.
	//
	// Returns:
	//   int - the number of matching experiences.
	//   error - any error encountered.
	CountByMemoryType(ctx context.Context, tenantID string, memoryType MemoryType) (int, error)

	// Update updates an existing experience.
	//
	// Args:
	//   ctx - operation context.
	//   experience - the experience to update.
	//
	// Returns:
	//   error - any error encountered.
	Update(ctx context.Context, experience *Experience) error

	// Delete deletes an experience by ID.
	//
	// Args:
	//   ctx - operation context.
	//   id - the unique identifier of the experience to delete.
	//
	// Returns:
	//   error - any error encountered.
	Delete(ctx context.Context, id string) error

	// DeleteBatch deletes multiple experiences by their IDs in a single
	// operation. Implementations SHOULD fall back to individual deletes
	// when the backend does not support batch deletion.
	//
	// Args:
	//   ctx - operation context.
	//   ids - the unique identifiers of the experiences to delete.
	//
	// Returns:
	//   error - any error encountered.
	DeleteBatch(ctx context.Context, ids []string) error

	// Create creates a new experience.
	//
	// Args:
	//   ctx - operation context.
	//   experience - the experience to create.
	//
	// Returns:
	//   error - any error encountered.
	Create(ctx context.Context, experience *Experience) error
}
