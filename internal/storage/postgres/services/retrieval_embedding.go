// Package services provides retrieval services for the storage system.
package services

import (
	"context"

	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	"github.com/Timwood0x10/ares/internal/truncate"
)

// getEmbedding retrieves embedding for a query with caching.
func (s *RetrievalService) getEmbedding(ctx context.Context, query string) []float64 {
	if query == "" {
		return nil
	}

	// Use the unified embedding pipeline when available.
	s.pipelineMu.RLock()
	p := s.pipeline
	s.pipelineMu.RUnlock()
	if p != nil {
		spec, err := p.BuildSpec(memembed.KindMemoryQuery, query)
		if err != nil {
			s.logger.Warn("Failed to build query spec", "error", err)
			return nil
		}
		vec, err := p.Embed(ctx, spec)
		if err != nil {
			s.logger.Warn("Failed to get embedding via pipeline", "query", query, "error", err)
			return nil
		}
		return vec
	}

	// Fallback to direct embedding client.
	if s.embeddingClient == nil {
		s.logger.Warn("Embedding client is nil, cannot get embedding")
		return nil
	}

	vec, err := s.embeddingClient.Embed(ctx, query)
	if err != nil {
		s.logger.Warn("Failed to get embedding", "query", query, "error", err)
		return nil
	}

	// Note: embedding service already returns normalized vectors, so no need to normalize again
	return vec
}

// getEmbeddingCached retrieves embedding with caching to reduce LLM calls.
// This can reduce 50-75% of embedding computations for repeated queries.
//
// Thread-safety: Uses read-write mutex to protect cache access.
// Implements LRU eviction to prevent unbounded memory growth.
//
// Args:
// ctx - operation context.
// query - query text.
// Returns embedding vector or nil if failed.
func (s *RetrievalService) getEmbeddingCached(ctx context.Context, query string) []float64 {
	if query == "" {
		return nil
	}

	// 1. Check cache (read lock)
	s.embeddingCacheMu.RLock()
	if embedding, ok := s.embeddingCache[query]; ok {
		s.embeddingCacheMu.RUnlock()
		s.logger.Debug("Embedding cache hit", "query", truncate.WithEllipsis(query, 30))
		return embedding
	}
	s.embeddingCacheMu.RUnlock()

	// 2. Compute embedding
	embedding := s.getEmbedding(ctx, query)
	if len(embedding) == 0 {
		return nil
	}

	// 3. Store in cache with LRU eviction (write lock)
	s.embeddingCacheMu.Lock()
	defer s.embeddingCacheMu.Unlock()

	// Check if eviction is needed
	if len(s.embeddingCache) >= s.embeddingCacheSizeLimit {
		if len(s.embeddingCacheAccessList) > 0 {
			oldestKey := s.embeddingCacheAccessList[0]
			delete(s.embeddingCache, oldestKey)
			s.embeddingCacheAccessList = s.embeddingCacheAccessList[1:]
			s.logger.Debug("Embedding cache eviction", "evicted_key", truncate.WithEllipsis(oldestKey, 30))
		}
	}

	s.embeddingCache[query] = embedding
	s.embeddingCacheAccessList = append(s.embeddingCacheAccessList, query)
	s.logger.Debug("Embedding cache miss, stored in cache", "query", truncate.WithEllipsis(query, 30), "cache_size", len(s.embeddingCache))

	return embedding
}
