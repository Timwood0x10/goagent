// Package services provides retrieval services for the storage system.
package services

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	experience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// searchSingleQuery executes retrieval for a single weighted query.
// This is the unified entry point for both vector and keyword search.
// It attaches query information to results for traceability.
// Args:
// ctx - operation context.
// q - weighted query to search.
// req - search request with configuration.
// Returns search results for this query.
func (s *RetrievalService) searchSingleQuery(ctx context.Context, q WeightedQuery, req *SearchRequest) []*SearchResult {
	var vectorResults, keywordResults []*SearchResult

	// Set 2 second timeout for search
	searchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Use database timeout from retrieval guard
	searchCtx, dbCancel := s.retrievalGuard.WithDBTimeout(searchCtx)
	defer dbCancel()

	// Create errgroup for parallel search (per design standard)
	eg, ctx := errgroup.WithContext(searchCtx)
	eg.SetLimit(2) // Vector and keyword in parallel

	var mu sync.Mutex

	// Vector search (parallel)
	eg.Go(func() error {
		if s.embeddingClient != nil && s.embeddingClient.IsEnabled() {
			// Check embedding circuit breaker
			if err := s.retrievalGuard.CheckEmbeddingCircuitBreaker(); err == nil {
				embedding := s.getEmbeddingCached(ctx, q.Query)
				if len(embedding) > 0 {
					results := s.searchAllVectorSources(ctx, embedding, q.Query, req)
					mu.Lock()
					vectorResults = append(vectorResults, results...)
					mu.Unlock()
				}
				s.retrievalGuard.RecordEmbeddingSuccess()
			} else {
				s.retrievalGuard.RecordEmbeddingFailure()
				s.logger.Warn("Embedding circuit breaker open", "query", q.Query, "error", err)
			}
		}
		return nil
	})

	// Keyword search (parallel)
	eg.Go(func() error {
		if req.Plan.EnableKeywordSearch {
			results := s.searchAllKeywordSources(ctx, q.Query, req.TenantID, req.Plan.TopK)
			mu.Lock()
			keywordResults = append(keywordResults, results...)
			mu.Unlock()
		}
		return nil
	})

	// Wait for both searches to complete
	if err := eg.Wait(); err != nil {
		s.logger.Warn("Some searches failed", "error", err)
	}

	// Merge vector and keyword results
	allResults := make([]*SearchResult, 0, len(vectorResults)+len(keywordResults))
	allResults = append(allResults, vectorResults...)
	allResults = append(allResults, keywordResults...)

	// Attach query information to all results
	for _, result := range allResults {
		result.Query = q.Query
		result.QueryWeight = q.Weight
	}

	return allResults
}

// searchAllVectorSources performs vector search across all enabled sources.
// Args:
// ctx - operation context.
// embedding - query embedding vector.
// query - original query text for logging.
// req - search request with configuration.
// Returns vector search results from all sources.
func (s *RetrievalService) searchAllVectorSources(ctx context.Context, embedding []float64, query string, req *SearchRequest) []*SearchResult {
	var results []*SearchResult

	// Search knowledge base
	if req.Plan.SearchKnowledge {
		kbResults := s.searchKnowledgeVector(ctx, embedding, req)
		results = append(results, kbResults...)
	}

	// Search experiences
	if req.Plan.SearchExperience {
		expResults := s.searchExperienceVector(ctx, embedding, req)
		results = append(results, expResults...)
	}

	// Search tools
	if req.Plan.SearchTools {
		toolResults := s.searchToolsVector(ctx, embedding, req)
		results = append(results, toolResults...)
	}

	return results
}

// searchAllKeywordSources performs keyword search across all enabled sources.
// Args:
// ctx - operation context.
// query - query text.
// tenantID - tenant identifier.
// limit - maximum results per source.
// Returns keyword search results from all sources.
func (s *RetrievalService) searchAllKeywordSources(ctx context.Context, query, tenantID string, limit int) []*SearchResult {
	// Search knowledge base
	kbResults := s.bm25SearchKnowledge(ctx, query, tenantID, limit)
	// Search experiences
	expResults := s.bm25SearchExperience(ctx, query, tenantID, limit)
	// Search tools
	toolResults := s.bm25SearchTools(ctx, query, tenantID, limit)

	results := make([]*SearchResult, 0, len(kbResults)+len(expResults)+len(toolResults))
	results = append(results, kbResults...)
	results = append(results, expResults...)
	results = append(results, toolResults...)

	return results
}

// searchKnowledgeVector performs vector search on knowledge base using pgvector.
// This uses cosine similarity to find the most relevant knowledge chunks.
func (s *RetrievalService) searchKnowledgeVector(ctx context.Context, embedding []float64, req *SearchRequest) []*SearchResult {
	if len(embedding) == 0 {
		return []*SearchResult{}
	}

	// Nil guard, symmetric with searchExperienceVector / searchToolsVector.
	// NewRetrievalService takes kbRepo as a caller-supplied argument and does
	// not validate it; a nil repository here used to dereference and panic
	// instead of degrading to "no knowledge results" the way its siblings do.
	if s.kbRepo == nil {
		if s.logger != nil {
			s.logger.Debug("KnowledgeRepository not available, skipping knowledge vector search")
		}
		return []*SearchResult{}
	}

	// Use Repository layer to search knowledge base
	chunks, err := s.kbRepo.SearchByVector(ctx, embedding, req.TenantID, req.Plan.TopK)
	if err != nil {
		s.logger.Error("Knowledge vector search failed", "error", err)
		return []*SearchResult{}
	}

	// Convert KnowledgeChunk to SearchResult
	results := make([]*SearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		result := &SearchResult{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Source:    chunk.SourceType,
			SubSource: "vector",
			Type:      "knowledge",
			Metadata:  chunk.Metadata,
			CreatedAt: chunk.CreatedAt,
		}

		// Extract similarity score from metadata if available
		if similarity, ok := chunk.Metadata["similarity"].(float64); ok {
			result.Score = similarity
		}

		results = append(results, result)
	}

	return results
}

// searchExperienceVector performs vector search on experiences using pgvector.
// This uses cosine similarity to find the most relevant agent experiences.
// Supports ranking and conflict resolution if enabled in the plan.
func (s *RetrievalService) searchExperienceVector(ctx context.Context, embedding []float64, req *SearchRequest) []*SearchResult {
	if len(embedding) == 0 {
		return []*SearchResult{}
	}

	if s.expRepo == nil {
		s.logger.Debug("ExperienceRepository not available, skipping experience search")
		return []*SearchResult{}
	}

	// Determine topK for vector search (default 20 for ranking)
	topK := req.Plan.TopK
	if req.Plan.ExperienceTopK > 0 {
		topK = req.Plan.ExperienceTopK
	}

	// Use Repository layer to search experiences
	experiences, err := s.expRepo.SearchByVector(ctx, embedding, req.TenantID, topK)
	if err != nil {
		s.logger.Error("Experience vector search failed", "error", err)
		return []*SearchResult{}
	}

	// If ranking is enabled, apply ranking and conflict resolution
	if req.Plan.ExperienceRankingEnabled && s.rankingService != nil {
		return s.applyExperienceRanking(ctx, experiences, req)
	}

	// Otherwise, convert directly to SearchResult
	return s.convertExperiencesToResults(experiences)
}

// applyExperienceRanking applies ranking and conflict resolution to experiences.
// Args:
// ctx - operation context.
// experiences - experiences to rank and resolve.
// req - search request containing configuration.
// Returns ranked and resolved search results.
func (s *RetrievalService) applyExperienceRanking(ctx context.Context, experiences []*storage_models.Experience, req *SearchRequest) []*SearchResult {
	if len(experiences) == 0 {
		return []*SearchResult{}
	}

	// Extract base semantic scores from metadata
	baseScores := make([]float64, len(experiences))
	apiExperiences := make([]*experience.Experience, len(experiences))

	for i, exp := range experiences {
		// Get semantic similarity from metadata (stored by SearchByVector)
		semanticScore := 0.5 // default score
		if exp.Metadata != nil {
			if score, ok := exp.Metadata["similarity"].(float64); ok {
				semanticScore = score
			}
		}
		baseScores[i] = semanticScore

		// Convert to API model
		apiExperiences[i] = &experience.Experience{
			ID:               exp.ID,
			TenantID:         exp.TenantID,
			Type:             exp.Type,
			Problem:          exp.Problem,
			Solution:         exp.Solution,
			Constraints:      exp.Constraints,
			Embedding:        exp.Embedding,
			EmbeddingModel:   exp.EmbeddingModel,
			EmbeddingVersion: exp.EmbeddingVersion,
			Score:            exp.Score,
			Success:          exp.Success,
			AgentID:          exp.AgentID,
			UsageCount:       exp.GetUsageCount(), // metadata["usage_count"] is authoritative for backward compatibility
			DecayAt:          exp.DecayAt,
			CreatedAt:        exp.CreatedAt,
		}
	}

	// Apply ranking
	ranked, rankErr := s.rankingService.Rank(ctx, apiExperiences, baseScores)
	if rankErr != nil {
		log.Error("rank experiences failed, returning empty results", "error", rankErr)
		return []*SearchResult{}
	}

	// Propagate the computed FinalScore onto the experience objects so the
	// final conversion (and any conflict-resolution winner, which reuses the
	// same *Experience pointers) carries the ranked score instead of the
	// persisted raw score — a bandit feedback signal that is typically 0 in
	// production, not a relevance score. Without this, rerankResults re-sorts
	// on near-zero scores and filterByScore drops experience results whenever
	// MinScore > 0, so the ranking feature never surfaces its scores.
	for _, r := range ranked {
		r.Experience.Score = r.FinalScore
	}

	// Apply conflict resolution if enabled
	var resolved []*experience.Experience
	if req.Plan.ExperienceConflictResolve && s.conflictResolver != nil {
		resolved = s.conflictResolver.Resolve(ctx, ranked)
	} else {
		// Extract experiences from ranked results
		resolved = make([]*experience.Experience, len(ranked))
		for i, r := range ranked {
			resolved[i] = r.Experience
		}
	}

	// Limit to TopK results
	if len(resolved) > req.Plan.TopK {
		resolved = resolved[:req.Plan.TopK]
	}

	// Convert to SearchResult
	return s.convertAPIExperiencesToResults(resolved)
}

// convertExperiencesToResults converts storage model experiences to search results.
// Args:
// experiences - storage model experiences.
// Returns search results.
func (s *RetrievalService) convertExperiencesToResults(experiences []*storage_models.Experience) []*SearchResult {
	results := make([]*SearchResult, 0, len(experiences))
	for _, exp := range experiences {
		result := &SearchResult{
			ID:        exp.ID,
			Content:   exp.Output,
			Source:    "experience",
			SubSource: "vector",
			Type:      "experience",
			Metadata:  exp.Metadata,
			CreatedAt: exp.CreatedAt,
		}

		// Extract similarity score from metadata if available
		if exp.Metadata != nil {
			if similarity, ok := exp.Metadata["similarity"].(float64); ok {
				result.Score = similarity
			}
		}

		results = append(results, result)
	}

	return results
}

// convertAPIExperiencesToResults converts API model experiences to search results.
// Args:
// experiences - API model experiences.
// Returns search results with ranking scores.
func (s *RetrievalService) convertAPIExperiencesToResults(experiences []*experience.Experience) []*SearchResult {
	results := make([]*SearchResult, 0, len(experiences))
	for _, exp := range experiences {
		result := &SearchResult{
			ID:        exp.ID,
			Content:   exp.Solution,
			Source:    "experience",
			SubSource: "vector",
			Type:      "experience",
			Metadata: map[string]interface{}{
				"problem":     exp.Problem,
				"constraints": exp.Constraints,
				"usage_count": exp.UsageCount,
				"success":     exp.Success,
				"agent_id":    exp.AgentID,
			},
			CreatedAt: exp.CreatedAt,
			Score:     exp.Score, // Use ranked score
		}

		results = append(results, result)
	}

	return results
}

// searchToolsVector performs vector search on tools using pgvector.
// This combines semantic search with usage statistics for tool ranking.
func (s *RetrievalService) searchToolsVector(ctx context.Context, embedding []float64, req *SearchRequest) []*SearchResult {
	if len(embedding) == 0 {
		return []*SearchResult{}
	}

	if s.toolRepo == nil {
		s.logger.Debug("ToolRepository not available, skipping tool search")
		return []*SearchResult{}
	}

	// Limit tool recommendations to avoid overwhelming results
	maxTools := 5
	if req.Plan.TopK < maxTools {
		maxTools = req.Plan.TopK
	}

	// Use Repository layer to search tools
	tools, err := s.toolRepo.SearchByVector(ctx, embedding, req.TenantID, maxTools)
	if err != nil {
		s.logger.Error("Tool vector search failed", "error", err)
		return []*SearchResult{}
	}

	// Convert Tool to SearchResult
	results := make([]*SearchResult, 0, len(tools))
	for _, tool := range tools {
		result := &SearchResult{
			ID:        tool.ID,
			Content:   tool.Description,
			Source:    "tool",
			SubSource: "vector",
			Type:      "tool",
			Metadata: map[string]interface{}{
				"name":         tool.Name,
				"agent_type":   tool.AgentType,
				"tags":         tool.Tags,
				"usage_count":  tool.UsageCount,
				"success_rate": tool.SuccessRate,
			},
			CreatedAt: tool.CreatedAt,
		}

		// Extract similarity score from metadata if available
		if similarity, ok := tool.Metadata["similarity"].(float64); ok {
			result.Score = similarity
		} else {
			// Default score based on success rate and usage
			result.Score = tool.SuccessRate*0.7 + float64(tool.UsageCount)/100.0*0.3
		}

		results = append(results, result)
	}

	return results
}

// bm25Search performs BM25 full-text search using PostgreSQL tsvector.
// This serves as a fallback when vector search fails or is disabled.
func (s *RetrievalService) bm25Search(ctx context.Context, req *SearchRequest) []*SearchResult {
	if req.Query == "" {
		return []*SearchResult{}
	}

	// Search knowledge base using BM25
	knowledgeResults := s.bm25SearchKnowledge(ctx, req.Query, req.TenantID, req.Plan.TopK)
	// Search experiences using BM25
	experienceResults := s.bm25SearchExperience(ctx, req.Query, req.TenantID, req.Plan.TopK)
	// Search tools using BM25
	toolResults := s.bm25SearchTools(ctx, req.Query, req.TenantID, req.Plan.TopK)

	results := make([]*SearchResult, 0, len(knowledgeResults)+len(experienceResults)+len(toolResults))
	results = append(results, knowledgeResults...)
	results = append(results, experienceResults...)
	results = append(results, toolResults...)

	return results
}

// bm25SearchKnowledge performs BM25 search on knowledge base.
func (s *RetrievalService) bm25SearchKnowledge(ctx context.Context, query string, tenantID string, limit int) []*SearchResult {
	// Nil guard, symmetric with bm25SearchExperience / bm25SearchTools — see
	// searchKnowledgeVector for why a nil kbRepo must degrade, not panic.
	if s.kbRepo == nil {
		if s.logger != nil {
			s.logger.Debug("KnowledgeRepository not available, skipping knowledge BM25 search")
		}
		return []*SearchResult{}
	}

	// Use Repository layer for keyword search
	chunks, err := s.kbRepo.SearchByKeyword(ctx, query, tenantID, limit)
	if err != nil {
		s.logger.Error("Knowledge BM25 search failed", "error", err)
		return []*SearchResult{}
	}

	// Convert KnowledgeChunk to SearchResult
	results := make([]*SearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		result := &SearchResult{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Source:    "knowledge",
			SubSource: "keyword",
			Type:      "knowledge",
			Metadata:  chunk.Metadata,
			CreatedAt: chunk.CreatedAt,
		}

		// Extract keyword score from metadata if available
		if score, ok := chunk.Metadata["keyword_score"].(float64); ok {
			result.Score = score
		}

		results = append(results, result)
	}

	return results
}

// bm25SearchExperience performs BM25 search on experiences.
func (s *RetrievalService) bm25SearchExperience(ctx context.Context, query string, tenantID string, limit int) []*SearchResult {
	if s.expRepo == nil {
		return []*SearchResult{}
	}

	experiences, err := s.expRepo.SearchByKeyword(ctx, query, tenantID, limit)
	if err != nil {
		s.logger.Error("Experience BM25 search failed", "error", err)
		return []*SearchResult{}
	}

	results := make([]*SearchResult, 0, len(experiences))
	for _, exp := range experiences {
		result := &SearchResult{
			ID:          exp.ID,
			Content:     exp.Output,
			Score:       exp.Score * 0.5, // BM25 scores are typically lower
			Source:      "experience",
			SubSource:   "keyword",
			Query:       query,
			QueryWeight: 1.0,
			Metadata: map[string]interface{}{
				"task_type": exp.Type,
				"success":   exp.Success,
				"agent_id":  exp.AgentID,
				"lessons":   exp.Input,
			},
			CreatedAt: exp.CreatedAt,
		}
		results = append(results, result)
	}

	return results
}

// bm25SearchTools performs BM25 search on tools.
func (s *RetrievalService) bm25SearchTools(ctx context.Context, query string, tenantID string, limit int) []*SearchResult {
	if s.toolRepo == nil {
		return []*SearchResult{}
	}

	tools, err := s.toolRepo.SearchByKeyword(ctx, query, tenantID, limit)
	if err != nil {
		s.logger.Error("Tool BM25 search failed", "error", err)
		return []*SearchResult{}
	}

	// Convert Tool to SearchResult
	results := make([]*SearchResult, 0, len(tools))
	for _, tool := range tools {
		result := &SearchResult{
			ID:        tool.ID,
			Content:   tool.Description,
			Source:    "tool",
			SubSource: "keyword",
			Type:      "tool",
			Metadata: map[string]interface{}{
				"name":         tool.Name,
				"agent_type":   tool.AgentType,
				"tags":         tool.Tags,
				"usage_count":  tool.UsageCount,
				"success_rate": tool.SuccessRate,
			},
			CreatedAt: tool.CreatedAt,
		}

		// Default score based on success rate and usage
		result.Score = tool.SuccessRate*0.7 + float64(tool.UsageCount)/100.0*0.3

		results = append(results, result)
	}

	return results
}

// mergeAndRerank merges and reranks results using deduplication with score accumulation.
// This implements the converged version where all weights are applied in rerank only.
// Args:
// results - all results from all queries.
// plan - retrieval plan with configuration.
// Returns merged and reranked results.
func (s *RetrievalService) mergeAndRerank(results []*SearchResult, plan *RetrievalPlan) []*SearchResult {
	if len(results) == 0 {
		return results
	}

	// 1. Deduplicate with score accumulation (improved version)
	deduped := s.deduplicateResults(results)

	// 2. Unified reranking (all weights applied here)
	reranked := s.rerankResults(deduped, plan)

	return reranked
}

// deduplicateResults removes duplicate results by ID and accumulates scores.
// This is the improved version that preserves "multi-hit signals" by accumulating scores.
// Multi-query hits get naturally higher scores without extra features.
// Args:
// results - results to deduplicate.
// Returns deduplicated results with accumulated scores.
func (s *RetrievalService) deduplicateResults(results []*SearchResult) []*SearchResult {
	seen := make(map[string]*SearchResult)

	for _, result := range results {
		if existing, exists := seen[result.ID]; exists {
			// Accumulate scores (30% of new score added)
			// This preserves multi-hit signals without over-weighting
			existing.Score += result.Score * 0.3

			// Update query info if this one has higher weight
			if result.QueryWeight > existing.QueryWeight {
				existing.Query = result.Query
				existing.QueryWeight = result.QueryWeight
			}
		} else {
			seen[result.ID] = result
		}
	}

	deduped := make([]*SearchResult, 0, len(seen))
	for _, result := range seen {
		deduped = append(deduped, result)
	}

	return deduped
}

// rerankResults performs unified reranking as the single scoring entry point.
// This applies all weights (query, source, subSource, signals) in one place.
// This fixes the double-application bug from the original design.
// Args:
// results - results to rerank.
// plan - retrieval plan with configuration.
// Returns reranked results sorted by final score.
func (s *RetrievalService) rerankResults(results []*SearchResult, plan *RetrievalPlan) []*SearchResult {
	if len(results) == 0 {
		return results
	}

	// Check if multiple sources are actually being searched
	// Only apply source weight if multiple sources are enabled
	multipleSourcesEnabled := false
	activeSources := 0
	if plan.SearchKnowledge {
		activeSources++
	}
	if plan.SearchExperience {
		activeSources++
	}
	if plan.SearchTools {
		activeSources++
	}
	if plan.SearchTaskResults {
		activeSources++
	}
	if activeSources > 1 {
		multipleSourcesEnabled = true
	}

	// Apply all weights here (unified scoring entry point)
	for _, result := range results {
		baseScore := result.Score

		// 1. Query weight (only applied here, not in merge)
		baseScore *= result.QueryWeight

		// 2. Source weight - only apply if multiple sources are being searched
		// This avoids reducing scores in single-source retrieval
		if multipleSourcesEnabled {
			baseScore *= s.sourceWeight(result.Source, plan)
		}

		// 3. SubSource weight (vector vs keyword)
		baseScore *= s.subSourceWeight(result.SubSource)

		// 4. Source-specific signals
		baseScore = s.applySourceSignals(baseScore, result)

		// 5. Time decay (if enabled)
		if plan.EnableTimeDecay {
			baseScore *= s.calculateTimeDecay(result.CreatedAt)
		}

		result.Score = baseScore
	}

	// Sort by score (descending)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// sourceWeight calculates weight based on result source.
// Args:
// source - result source (knowledge, experience, tool).
// plan - retrieval plan with source weights.
// Returns weight multiplier.
func (s *RetrievalService) sourceWeight(source string, plan *RetrievalPlan) float64 {
	switch source {
	case "experience":
		return plan.ExperienceWeight
	case "tool":
		return plan.ToolsWeight
	case "knowledge":
		return plan.KnowledgeWeight
	case "task_result":
		return plan.TaskResultsWeight
	default:
		return 1.0
	}
}

// subSourceWeight calculates weight based on sub-source (vector vs keyword).
// Vector search gets 1.0, keyword search gets 0.8 to avoid contamination.
// Args:
// sub - sub-source (vector, keyword).
// Returns weight multiplier.
func (s *RetrievalService) subSourceWeight(sub string) float64 {
	switch sub {
	case "vector":
		return 1.0 // Vector search is baseline
	case "keyword":
		return 0.8 // Keyword search has lower weight
	default:
		return 1.0
	}
}

// applySourceSignals applies source-specific signals to the score.
// Args:
// baseScore - current score.
// result - search result with metadata.
// Returns adjusted score.
func (s *RetrievalService) applySourceSignals(baseScore float64, result *SearchResult) float64 {
	// Experience-specific signals
	if result.Source == "experience" {
		if success, ok := result.Metadata["success"].(bool); ok {
			if success {
				baseScore *= 1.2 // Successful experiences get boost
			} else {
				baseScore *= 0.7 // Failed experiences get penalty
			}
		}

		// Execution time signal (faster experiences get preference)
		if executionTime, ok := result.Metadata["execution_time"].(float64); ok {
			// Normalize execution time: < 1s = 1.2x, 1-5s = 1.0x, > 5s = 0.8x
			switch {
			case executionTime < 1.0:
				baseScore *= 1.2 // Very fast experiences get boost
			case executionTime < 5.0:
				baseScore *= 1.0 // Normal speed, no change
			default:
				baseScore *= 0.8 // Slow experiences get penalty
			}
		}

		// Reuse count signal (highly reusable experiences get boost)
		if reuseCount, ok := result.Metadata["reuse_count"].(int); ok && reuseCount > 3 {
			baseScore *= 1.1
		}

		// Lessons learned signal (experiences with lessons get boost)
		if lessons, ok := result.Metadata["lessons"].(string); ok && lessons != "" {
			baseScore *= 1.05 // Experiences with documented lessons get slight boost
		}
	}

	// Tool-specific signals
	if result.Source == "tool" {
		if requiresAuth, ok := result.Metadata["requires_auth"].(bool); ok && requiresAuth {
			baseScore *= 0.9 // Tools requiring auth get slight penalty
		}

		// Success rate signal
		if successRate, ok := result.Metadata["success_rate"].(float64); ok {
			if successRate < 0.5 {
				baseScore *= 0.8 // Low success rate tools get penalty
			} else if successRate > 0.8 {
				baseScore *= 1.1 // High success rate tools get boost
			}
		}
	}

	return baseScore
}

// filterByScore filters results by minimum score threshold.
func (s *RetrievalService) filterByScore(results []*SearchResult, minScore float64) []*SearchResult {
	// Filter by minimum score (negative minScore means no filtering)
	filtered := make([]*SearchResult, 0, len(results))
	for _, result := range results {
		if result.Score >= minScore {
			filtered = append(filtered, result)
		}
	}

	return filtered
}

// countResultsBySource counts results by source for trace information.
func (s *RetrievalService) countResultsBySource(results []*SearchResult) map[string]int {
	counts := make(map[string]int)
	for _, result := range results {
		counts[result.Source]++
	}
	return counts
}

// Helper: ensure strings import is used in this file for inline usage below.
var _ = strings.ToLower
