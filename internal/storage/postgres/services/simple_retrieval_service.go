// Package services provides simplified retrieval services for the storage system.
// This service focuses on pure vector similarity search without complex weight calculations.
package services

import (
	"context"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/internal/errors"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// SimpleRetrievalConfig configuration for simple retrieval service
type SimpleRetrievalConfig struct {
	TopK        int     `json:"top_k"`        // Number of results to return
	MinScore    float64 `json:"min_score"`    // Minimum similarity score
	QueryPrefix string  `json:"query_prefix"` // Prefix for query embedding (e.g., "query:")
}

// SimpleSearchResult simple search result with only essential fields
type SimpleSearchResult struct {
	Content string  `json:"content"` // Content text
	Source  string  `json:"source"`  // Source document/file path
	Score   float64 `json:"score"`   // Cosine similarity score (1 - distance)
}

// SimpleRetrievalService provides pure vector similarity search
// This is inspired by ChromaDB's simple and direct approach:
// - Direct vector similarity search (1 - cosine_distance)
// - No complex weight calculations
// - No time decay
// - No query rewrites
// - Simple and effective for single knowledge base scenarios
type SimpleRetrievalService struct {
	mu        sync.RWMutex
	repo      *repositories.KnowledgeRepository
	embedding *embedding.EmbeddingClient
	config    *SimpleRetrievalConfig
	pipeline  memembed.EmbeddingPipeline
}

// SetEmbeddingPipeline configures the unified embedding pipeline for query embedding.
// When set, Search uses the pipeline with canonical query specs instead of calling
// the embedding client directly.
func (s *SimpleRetrievalService) SetEmbeddingPipeline(pipeline memembed.EmbeddingPipeline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pipeline = pipeline
}

// embedQuery generates a query embedding using the pipeline when available,
// falling back to direct embedder call.
func (s *SimpleRetrievalService) embedQuery(ctx context.Context, query string) ([]float64, error) {
	s.mu.RLock()
	pipeline := s.pipeline
	config := s.config
	s.mu.RUnlock()

	if pipeline != nil {
		spec, err := pipeline.BuildSpec(memembed.KindMemoryQuery, query)
		if err != nil {
			return nil, errors.Wrap(err, "build query spec")
		}
		vec, err := pipeline.Embed(ctx, spec)
		if err != nil {
			return nil, errors.Wrap(err, "embed via pipeline")
		}
		return vec, nil
	}
	if s.embedding == nil {
		return nil, errors.New("embedding client is not configured")
	}
	return s.embedding.EmbedWithPrefix(ctx, query, config.QueryPrefix)
}

// NewSimpleRetrievalService creates a new simple retrieval service
func NewSimpleRetrievalService(
	repo *repositories.KnowledgeRepository,
	embeddingClient *embedding.EmbeddingClient,
	config *SimpleRetrievalConfig,
) *SimpleRetrievalService {
	if config == nil {
		config = &SimpleRetrievalConfig{
			TopK:        5,
			MinScore:    0.6,
			QueryPrefix: "query:",
		}
	}

	return &SimpleRetrievalService{
		repo:      repo,
		embedding: embeddingClient,
		config:    config,
	}
}

// Search performs intelligent retrieval with precision mode support
// Returns results with cosine similarity score (1 - distance), where:
// - 1.0 = perfect match
// - 0.0 = orthogonal (no relation)
// - -1.0 = opposite meaning
func (s *SimpleRetrievalService) Search(ctx context.Context, tenantID, query string) ([]*SimpleSearchResult, error) {
	log.Info("SimpleRetrievalService.Search",
		"tenant_id", tenantID,
		"query", query,
		"top_k", s.GetConfig().TopK,
		"min_score", s.GetConfig().MinScore)

	// Check if precision mode should be used
	if s.isPrecisionMode(query) {
		log.Info("Using precision mode", "query", query)
		return s.searchPrecision(ctx, tenantID, query), nil
	}

	// Generate embedding using unified pipeline when available.
	queryEmbedding, err := s.embedQuery(ctx, query)
	if err != nil {
		log.Error("Failed to embed query", "error", err)
		return nil, errors.Wrap(err, "embed query")
	}

	log.Debug("Query embedding generated", "dimension", len(queryEmbedding))

	// Perform pure vector similarity search
	// SearchByVector returns chunks with similarity in metadata
	// Use larger limit to get more candidates before filtering
	chunks, err := s.repo.SearchByVector(ctx, queryEmbedding, tenantID, s.GetConfig().TopK*5)
	if err != nil {
		log.Error("Vector search failed", "error", err)
		return nil, errors.Wrap(err, "vector search")
	}

	log.Debug("Raw chunks retrieved", "count", len(chunks))

	// Convert to simple results and filter by min_score
	var results []*SimpleSearchResult
	for _, chunk := range chunks {
		// Extract similarity from metadata (set by SearchByVector)
		// SearchByVector computes: 1 - cosine_distance
		similarity, ok := chunk.Metadata["similarity"].(float64)
		if !ok {
			log.Warn("No similarity score found for chunk", "id", chunk.ID)
			continue
		}

		// Filter by min_score threshold
		if similarity < s.GetConfig().MinScore {
			continue
		}

		results = append(results, &SimpleSearchResult{
			Content: chunk.Content,
			Source:  chunk.Source,
			Score:   similarity,
		})
	}

	// Limit to TopK results
	if len(results) > s.GetConfig().TopK {
		results = results[:s.GetConfig().TopK]
	}

	log.Info("Search completed",
		"results_count", len(results),
		"min_score", s.GetConfig().MinScore)

	return results, nil
}

// isPrecisionMode determines if precision mode should be used for the query.
// Precision mode is triggered for:
// - Short queries (≤10 characters)
// - Queries containing special symbols (= or mathematical expressions)
// This uses deterministic matching to cover semantic retrieval for precise queries.
func (s *SimpleRetrievalService) isPrecisionMode(query string) bool {
	// Short queries use exact/keyword matching for precision
	if utf8.RuneCountInString(query) <= 10 {
		return true
	}

	// Core expression patterns: containing equals sign or mathematical operators
	// Note: - is intentionally excluded to avoid matching hyphens in compound words (e.g., "go-agent")
	// Note: +, *, / are checked via regex requiring digit adjacency to avoid matching
	//       programming symbols like "C++", "*args", "**kwargs"
	if strings.Contains(query, "=") || mathExprPattern.MatchString(query) {
		return true
	}

	return false
}

// searchPrecision executes the precision retrieval pipeline for SimpleRetrievalService.
func (s *SimpleRetrievalService) searchPrecision(ctx context.Context, tenantID, query string) []*SimpleSearchResult {
	log.Debug("Executing precision search pipeline", "query", query)

	// 1. Exact Match (highest priority)
	exact, err := s.searchExact(ctx, tenantID, query)
	if err != nil {
		log.Error("Failed to execute exact match search, falling back to keyword", "error", err)
		exact = nil
	}
	if len(exact) > 0 {
		log.Debug("Precision search: exact match found", "count", len(exact))
		return exact
	}

	// 2. Keyword Search (second priority)
	keyword, err := s.searchKeyword(ctx, tenantID, query)
	if err != nil {
		log.Error("Failed to execute keyword search, falling back to vector", "error", err)
		keyword = nil
	}
	if len(keyword) > 0 {
		log.Debug("Precision search: keyword match found", "count", len(keyword))
		return keyword
	}

	// 3. Vector Search (fallback)
	vector, err := s.searchVector(ctx, tenantID, query)
	if err != nil {
		log.Error("Failed to execute vector search", "error", err)
		return []*SimpleSearchResult{}
	}
	log.Debug("Precision search: using vector fallback", "count", len(vector))

	return vector
}

// searchExact performs exact substring matching.
func (s *SimpleRetrievalService) searchExact(ctx context.Context, tenantID, query string) ([]*SimpleSearchResult, error) {
	log.Debug("Running exact match search", "query", query)

	chunks, err := s.repo.SearchBySubstring(ctx, query, tenantID, s.GetConfig().TopK)
	if err != nil {
		log.Error("Exact match search failed", "error", err)
		return nil, errors.Wrap(err, "exact match search")
	}

	if len(chunks) == 0 {
		return []*SimpleSearchResult{}, nil
	}

	results := make([]*SimpleSearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		results = append(results, &SimpleSearchResult{
			Content: chunk.Content,
			Source:  chunk.Source,
			Score:   1.0, // Fixed highest score for exact matches
		})
	}

	return results, nil
}

// searchKeyword performs BM25 keyword search with simplified scoring.
func (s *SimpleRetrievalService) searchKeyword(ctx context.Context, tenantID, query string) ([]*SimpleSearchResult, error) {
	log.Debug("Running keyword search", "query", query)

	chunks, err := s.repo.SearchByKeyword(ctx, query, tenantID, s.GetConfig().TopK)
	if err != nil {
		log.Error("Keyword search failed", "error", err)
		return nil, errors.Wrap(err, "keyword search")
	}

	if len(chunks) == 0 {
		return []*SimpleSearchResult{}, nil
	}

	results := make([]*SimpleSearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		score := 1.0
		if chunk.Metadata != nil {
			if keywordScore, ok := chunk.Metadata["keyword_score"].(float64); ok {
				score = math.Min(keywordScore, 1.0)
			}
		}

		results = append(results, &SimpleSearchResult{
			Content: chunk.Content,
			Source:  chunk.Source,
			Score:   score,
		})
	}

	return results, nil
}

// searchVector performs vector similarity search.
func (s *SimpleRetrievalService) searchVector(ctx context.Context, tenantID, query string) ([]*SimpleSearchResult, error) {
	log.Debug("Running vector search", "query", query)

	// Generate embedding using unified pipeline when available.
	queryEmbedding, err := s.embedQuery(ctx, query)
	if err != nil {
		log.Error("Failed to embed query", "error", err)
		return nil, errors.Wrap(err, "embed query")
	}

	chunks, err := s.repo.SearchByVector(ctx, queryEmbedding, tenantID, s.GetConfig().TopK)
	if err != nil {
		log.Error("Vector search failed", "error", err)
		return nil, errors.Wrap(err, "vector search")
	}

	if len(chunks) == 0 {
		return []*SimpleSearchResult{}, nil
	}

	results := make([]*SimpleSearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		score := 0.0
		if chunk.Metadata != nil {
			if similarity, ok := chunk.Metadata["similarity"].(float64); ok {
				score = similarity
			}
		}

		// Apply min_score filter, consistent with normal Search path behavior
		if score < s.GetConfig().MinScore {
			continue
		}

		results = append(results, &SimpleSearchResult{
			Content: chunk.Content,
			Source:  chunk.Source,
			Score:   score,
		})
	}

	return results, nil
}

// SetConfig updates the retrieval configuration
func (s *SimpleRetrievalService) SetConfig(config *SimpleRetrievalConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
}

// GetConfig returns the current configuration
func (s *SimpleRetrievalService) GetConfig() *SimpleRetrievalConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}
