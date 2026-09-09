// Package embedding provides the canonical contract for vector embedding
// services.
//
// The EmbeddingService interface is storage-agnostic: callers may back it
// with PostgreSQL, SQLite-vec, pgvector, or any other vector database.
// The ARES EmbeddingClient (internal/storage/postgres/embedding) implements
// this interface, but external modules are free to provide their own
// implementation when integrating the distillation pipeline.
//
// This package holds the canonical definitions previously exposed as
// api/embedding (M5 migration); api/embedding is now a pure forwarding layer.
package embedding

import (
	"context"
	"time"
)

// EmbeddingService defines the interface for vector embedding operations.
//
// This interface allows for mocking in tests and swapping implementations.
// It deliberately avoids any storage-specific coupling so that external
// integrators can use any vector backend they prefer.
type EmbeddingService interface {
	// Embed generates vector embedding for a query text (uses "query" prefix).
	// For document storage, use EmbedWithPrefix with "passage:" prefix.
	//
	// Args:
	//   ctx - operation context.
	//   text - text to embed.
	//
	// Returns:
	//   []float64 - embedding vector.
	//   error - any error encountered.
	Embed(ctx context.Context, text string) ([]float64, error)

	// EmbedWithPrefix generates vector embedding with custom prefix.
	// Use "query:" for search queries and "passage:" for document storage.
	//
	// Args:
	//   ctx - operation context.
	//   text - text to embed.
	//   prefix - prefix to add before text (e.g., "query:", "passage:").
	//
	// Returns:
	//   []float64 - embedding vector.
	//   error - any error encountered.
	EmbedWithPrefix(ctx context.Context, text, prefix string) ([]float64, error)

	// EmbedBatch generates vector embeddings for multiple texts.
	//
	// Args:
	//   ctx - operation context.
	//   texts - texts to embed.
	//
	// Returns:
	//   [][]float64 - embedding vectors for each text.
	//   error - any error encountered.
	EmbedBatch(ctx context.Context, texts []string) ([][]float64, error)

	// HealthCheck checks if the embedding service is healthy.
	//
	// Args:
	//   ctx - operation context.
	//
	// Returns:
	//   error - any error encountered, nil if healthy.
	HealthCheck(ctx context.Context) error

	// GetModel returns the embedding model name.
	//
	// Returns:
	//   string - the model name.
	GetModel() string

	// GetTimeout returns the embedding timeout.
	//
	// Returns:
	//   time.Duration - the timeout duration.
	GetTimeout() time.Duration
}
