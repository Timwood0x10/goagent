// Package embedding provides vector embedding functionality with caching.
//
// The EmbeddingService interface is defined in the canonical package
// internal/embedding. This internal package provides the EmbeddingClient
// implementation, which satisfies internal/embedding.EmbeddingService.
//
// Importing the canonical interface here lets internal callers depend on the
// storage-agnostic contract while still using the PostgreSQL-backed
// implementation provided below.
package embedding

import (
	aresembed "github.com/Timwood0x10/ares/internal/embedding"
)

// Ensure EmbeddingClient implements the canonical EmbeddingService interface.
var _ aresembed.EmbeddingService = (*EmbeddingClient)(nil)
