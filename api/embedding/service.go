// Package embedding is the DEPRECATED public alias of
// internal/embedding (M5). New code MUST import internal/embedding; this
// package exists only for external consumers and is scheduled for removal.
package embedding

import "github.com/Timwood0x10/ares/internal/embedding"

// EmbeddingService defines the interface for vector embedding operations.
type EmbeddingService = embedding.EmbeddingService
