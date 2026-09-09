// Package services provides retrieval services for the storage system.
package services

import (
	"context"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/llm"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	experience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
	"github.com/Timwood0x10/ares/internal/truncate"
)

// allowedSynonymDirMu protects allowedSynonymDir from concurrent access.
var (
	allowedSynonymDirMu sync.RWMutex
	allowedSynonymDir   string
)

// SetAllowedSynonymDir sets the allowed directory for synonym config files.
// This is a security measure to prevent path traversal attacks.
func SetAllowedSynonymDir(dir string) {
	allowedSynonymDirMu.Lock()
	defer allowedSynonymDirMu.Unlock()
	allowedSynonymDir = dir
}

// getAllowedSynonymDir returns the configured synonym directory safely.
func getAllowedSynonymDir() string {
	allowedSynonymDirMu.RLock()
	defer allowedSynonymDirMu.RUnlock()
	return allowedSynonymDir
}

// SearchRequest represents a search request with configuration.
type SearchRequest struct {
	Query       string          `json:"query"`           // Search query text
	TenantID    string          `json:"tenant_id"`       // Tenant ID for isolation
	TopK        int             `json:"top_k"`           // Number of results to return
	MinScore    float64         `json:"min_score"`       // Minimum similarity score
	Plan        *RetrievalPlan  `json:"plan"`            // Retrieval strategy
	EnableTrace bool            `json:"enable_trace"`    // Enable trace logging
	Trace       *RetrievalTrace `json:"trace,omitempty"` // Trace information
}

// SearchResult represents a single search result.
type SearchResult struct {
	ID        string                 `json:"id"`
	Content   string                 `json:"content"`
	Score     float64                `json:"score"`
	Source    string                 `json:"source"`     // knowledge, experience, tool, task_result
	SubSource string                 `json:"sub_source"` // vector, keyword
	Type      string                 `json:"type"`       // Result type for filtering
	Metadata  map[string]interface{} `json:"metadata"`   // Additional metadata
	CreatedAt time.Time              `json:"created_at"`

	// Query information for traceability and scoring
	Query       string  `json:"query"`        // Query that matched this result
	QueryWeight float64 `json:"query_weight"` // Weight of the query (original=1.0, rule=0.7, llm=0.5)
}

// WeightedQuery represents a query with associated weight for retrieval.
// This enables controlling the impact of query rewrites on final results.
type WeightedQuery struct {
	Query  string  `json:"query"`  // Query text
	Weight float64 `json:"weight"` // Weight for scoring (original=1.0, rule=0.7, llm=0.5)
	Source string  `json:"source"` // Source: original, rewrite_rule, rewrite_llm
}

// ResultDebugInfo contains detailed debugging information for a search result.
// This helps answer "why this result is ranked first?" and supports observability.
type ResultDebugInfo struct {
	ID           string                 `json:"id"`            // Result ID
	Score        float64                `json:"score"`         // Final score
	Query        string                 `json:"query"`         // Query that matched this result
	QueryWeight  float64                `json:"query_weight"`  // Weight of the query
	Source       string                 `json:"source"`        // Result source (knowledge/experience/tool)
	SubSource    string                 `json:"sub_source"`    // Sub-source (vector/keyword)
	SourceWeight float64                `json:"source_weight"` // Weight from source
	SubWeight    float64                `json:"sub_weight"`    // Weight from sub-source
	Signals      map[string]interface{} `json:"signals"`       // Applied signals (success, reuse_count, etc.)
	Breakdown    map[string]float64     `json:"breakdown"`     // Score breakdown for analysis
}

// QueryPriorityConfig defines priority weights for different query types.
// This controls how much influence rewrites have on retrieval results.
type QueryPriorityConfig struct {
	OriginalWeight    float64 `json:"original_weight"`     // Original query weight (default 1.0)
	RuleRewriteWeight float64 `json:"rule_rewrite_weight"` // Rule-based rewrite weight (default 0.7)
	LLMRewriteWeight  float64 `json:"llm_rewrite_weight"`  // LLM-based rewrite weight (default 0.5)
	MaxQueries        int     `json:"max_queries"`         // Maximum number of queries (default 3)
}

// RetrievalPlan defines the retrieval strategy for multi-source search.
type RetrievalPlan struct {
	SearchKnowledge   bool `json:"search_knowledge"`    // Search in knowledge base
	SearchExperience  bool `json:"search_experience"`   // Search in experiences
	SearchTools       bool `json:"search_tools"`        // Search in tools
	SearchTaskResults bool `json:"search_task_results"` // Search in task results

	KnowledgeWeight   float64 `json:"knowledge_weight"`    // Weight for knowledge results (default 0.4)
	ExperienceWeight  float64 `json:"experience_weight"`   // Weight for experience results (default 0.3)
	ToolsWeight       float64 `json:"tools_weight"`        // Weight for tool results (default 0.2)
	TaskResultsWeight float64 `json:"task_results_weight"` // Weight for task result results (default 0.1)

	EnableQueryRewrite  bool `json:"enable_query_rewrite"`  // Enable query rewriting
	EnableKeywordSearch bool `json:"enable_keyword_search"` // Enable keyword/BM25 search
	EnableTimeDecay     bool `json:"enable_time_decay"`     // Enable time-based scoring decay

	TopK int `json:"top_k"` // Maximum results per source

	// Experience-specific configuration
	ExperienceRankingEnabled  bool `json:"experience_ranking_enabled"`  // Enable experience ranking
	ExperienceConflictResolve bool `json:"experience_conflict_resolve"` // Enable conflict resolution
	ExperienceTopK            int  `json:"experience_top_k"`            // Experience recall count (default 20)
}

// RetrievalTrace contains debugging information for retrieval operations.
type RetrievalTrace struct {
	OriginalQuery   string         `json:"original_query"`
	RewrittenQuery  string         `json:"rewritten_query"`
	RewriteUsed     bool           `json:"rewrite_used"`
	VectorResults   int            `json:"vector_results"`
	KeywordResults  int            `json:"keyword_results"`
	FinalResults    int            `json:"final_results"`
	ExecutionTime   time.Duration  `json:"execution_time"`
	VectorError     error          `json:"vector_error,omitempty"`
	SearchBreakdown map[string]int `json:"search_breakdown,omitempty"` // Results per source
}

// RetrievalService provides intelligent retrieval across multiple data sources.
// It implements hybrid search (vector + keyword), query rewriting, and time-based decay.
type RetrievalService struct {
	db                       *postgres.Pool
	embeddingClient          *embedding.EmbeddingClient
	llmClient                *llm.Client
	retrievalGuard           *postgres.RetrievalGuard
	kbRepo                   *repositories.KnowledgeRepository
	expRepo                  *repositories.ExperienceRepository
	toolRepo                 *repositories.ToolRepository
	logger                   *slog.Logger
	queryPriority            *QueryPriorityConfig
	embeddingCache           map[string][]float64
	embeddingCacheMu         sync.RWMutex
	embeddingCacheSizeLimit  int      // Maximum cache entries
	embeddingCacheAccessList []string // LRU access list for eviction
	synonymRules             map[string][]string

	// Query result cache to skip redundant LLM rewrites.
	queryCache       map[string]time.Time
	queryCacheMu     sync.RWMutex
	queryCacheTTL    time.Duration // TTL for cache entries
	queryCacheMaxLen int           // Maximum cache entries

	// Experience-specific services
	distillationService *experience.DistillationService
	rankingService      *experience.RankingService
	conflictResolver    *experience.ConflictResolver

	// Embedding pipeline for unified query embedding.
	pipelineMu sync.RWMutex // guards pipeline
	pipeline   memembed.EmbeddingPipeline
}

// NewRetrievalService creates a new RetrievalService instance.
// Args:
// pool - database connection pool.
// embeddingClient - embedding service client for vector search.
// llmClient - LLM client for query rewriting (optional, can be nil).
// retrievalGuard - rate limiting and circuit breaker for retrieval.
// kbRepo - knowledge repository for data access.
// expRepo - experience repository for experience search.
// toolRepo - tool repository for tool search.
// Returns new RetrievalService instance.
func NewRetrievalService(
	pool *postgres.Pool,
	embeddingClient *embedding.EmbeddingClient,
	llmClient *llm.Client,
	retrievalGuard *postgres.RetrievalGuard,
	kbRepo *repositories.KnowledgeRepository,
	expRepo *repositories.ExperienceRepository,
	toolRepo *repositories.ToolRepository,
) *RetrievalService {
	return &RetrievalService{
		db:                       pool,
		embeddingClient:          embeddingClient,
		llmClient:                llmClient,
		retrievalGuard:           retrievalGuard,
		kbRepo:                   kbRepo,
		expRepo:                  expRepo,
		toolRepo:                 toolRepo,
		logger:                   slog.Default(),
		queryPriority:            DefaultQueryPriorityConfig(),
		embeddingCache:           make(map[string][]float64),
		embeddingCacheSizeLimit:  1000, // Limit cache to 1000 entries (~8-12MB)
		embeddingCacheAccessList: make([]string, 0, 1000),
		synonymRules:             loadSynonymRules(),
		queryCache:               make(map[string]time.Time),
		queryCacheTTL:            10 * time.Minute, // Queries cached for 10 minutes
		queryCacheMaxLen:         500,              // Limit cache to 500 entries
	}
}

// SetEmbeddingPipeline configures the unified embedding pipeline for query embedding.
// When set, getEmbedding uses the pipeline with canonical query specs instead of
// calling the embedding client directly.
func (s *RetrievalService) SetEmbeddingPipeline(pipeline memembed.EmbeddingPipeline) {
	s.pipelineMu.Lock()
	defer s.pipelineMu.Unlock()
	s.pipeline = pipeline
}

// SetExperienceServices sets the experience-specific services.
// Args:
// distillationService - experience distillation service.
// rankingService - experience ranking service.
// conflictResolver - experience conflict resolver.
func (s *RetrievalService) SetExperienceServices(
	distillationService *experience.DistillationService,
	rankingService *experience.RankingService,
	conflictResolver *experience.ConflictResolver,
) {
	s.distillationService = distillationService
	s.rankingService = rankingService
	s.conflictResolver = conflictResolver
}

// DefaultQueryPriorityConfig returns the default query priority configuration.
func DefaultQueryPriorityConfig() *QueryPriorityConfig {
	return &QueryPriorityConfig{
		OriginalWeight:    1.0,
		RuleRewriteWeight: 0.7,
		LLMRewriteWeight:  0.5,
		MaxQueries:        3,
	}
}

// DefaultRetrievalPlan returns the default retrieval plan.
func DefaultRetrievalPlan() *RetrievalPlan {
	return &RetrievalPlan{
		SearchKnowledge:           true,
		SearchExperience:          true,
		SearchTools:               true,
		SearchTaskResults:         false,
		KnowledgeWeight:           0.4,
		ExperienceWeight:          0.3,
		ToolsWeight:               0.2,
		TaskResultsWeight:         0.1,
		EnableQueryRewrite:        true,
		EnableKeywordSearch:       true,
		EnableTimeDecay:           true,
		TopK:                      10,
		ExperienceRankingEnabled:  true,
		ExperienceConflictResolve: true,
		ExperienceTopK:            20,
	}
}

// Search performs intelligent retrieval across multiple data sources.
// This implements the core retrieval pipeline with hybrid search, query rewriting, and time decay.
// Args:
// ctx - database operation context.
// req - search request with query and configuration.
// Returns search results or error if retrieval fails.
func (s *RetrievalService) Search(ctx context.Context, req *SearchRequest) ([]*SearchResult, error) {
	startTime := time.Now()

	// Validate request
	if err := s.validateRequest(req); err != nil {
		return nil, err
	}

	// Check if precision mode should be used
	if s.isPrecisionMode(req.Query) {
		s.logger.Info("Using precision mode", "query", req.Query)
		return s.searchPrecision(ctx, req)
	}

	// Set default plan if not provided
	if req.Plan == nil {
		req.Plan = DefaultRetrievalPlan()
	}

	// Tenant isolation is enforced by the explicit tenant_id filters in every
	// repository query below; the RLS context approach is ineffective here
	// because set_config on a pooled connection is transaction-local and is
	// not visible to queries served by other pooled connections.

	// Check rate limiting and circuit breaker
	if err := s.retrievalGuard.AllowRateLimit(); err != nil {
		return nil, err
	}

	// 1. Build weighted queries (original + rewrites)
	queries := s.buildQueries(ctx, req.Query, req.Plan)
	s.logger.Debug("Built weighted queries", "count", len(queries), "queries", queries)

	// 2. Execute search for each weighted query in parallel
	var (
		allResults []*SearchResult
		resultsMu  sync.Mutex
	)
	eg, egCtx := errgroup.WithContext(ctx)
	for _, q := range queries {
		q := q
		eg.Go(func() error {
			results := s.searchSingleQuery(egCtx, q, req)
			resultsMu.Lock()
			allResults = append(allResults, results...)
			resultsMu.Unlock()
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		// searchSingleQuery does not return errors, but errgroup cancels
		// the context on panic. Log any unexpected error for observability.
		s.logger.Error("Parallel query search encountered an error", "error", err)
	}

	s.logger.Debug("Collected results from all queries", "total", len(allResults))

	// 3. Merge and rank results
	finalResults := s.mergeAndRerank(allResults, req.Plan)

	// 4. Apply TopK limit
	if len(finalResults) > req.TopK {
		finalResults = finalResults[:req.TopK]
	}

	// 5. Apply minimum score filter
	s.logger.Info("Before score filter", "results_count", len(finalResults), "min_score", req.MinScore)
	for i, result := range finalResults {
		s.logger.Info("Result before filter", "index", i, "score", result.Score, "content", truncate.WithEllipsis(result.Content, 50))
	}

	finalResults = s.filterByScore(finalResults, req.MinScore)

	s.logger.Info("After score filter", "results_count", len(finalResults))

	// 6. Generate retrieval trace (if enabled)
	if req.EnableTrace {
		req.Trace = &RetrievalTrace{
			OriginalQuery:   req.Query,
			RewrittenQuery:  "",
			RewriteUsed:     len(queries) > 1,
			VectorResults:   0,
			KeywordResults:  0,
			FinalResults:    len(finalResults),
			ExecutionTime:   time.Since(startTime),
			VectorError:     nil,
			SearchBreakdown: s.countResultsBySource(finalResults),
		}
	}

	// 7. Mark query as cached to skip redundant rewrites.
	s.markQueryCached(req.Query)

	return finalResults, nil
}

// validateRequest validates the search request.
func (s *RetrievalService) validateRequest(req *SearchRequest) error {
	if req == nil {
		return errors.ErrInvalidArgument
	}
	if req.Query == "" {
		return errors.ErrInvalidArgument
	}
	if req.TenantID == "" {
		return errors.ErrInvalidArgument
	}
	if req.TopK <= 0 {
		req.TopK = 10
	}
	// Cap TopK to prevent unbounded result sets from scanning millions of rows.
	if req.TopK > 100 {
		req.TopK = 100
	}
	return nil
}

// mathExprPattern matches mathematical expressions like "3+5", "a*2", "10/3".
// Operators are only matched when adjacent to digits to avoid false positives
// on programming language symbols (C++, *args, **kwargs).
var mathExprPattern = regexp.MustCompile(`\d+\s*[+*/]\s*\d+`)

// isPrecisionMode determines if precision mode should be used for the query.
// Precision mode is triggered for:
// - Short queries (≤10 characters)
// - Queries containing special symbols (= or mathematical expressions)
// This uses deterministic matching to cover semantic retrieval for precise queries.
func (s *RetrievalService) isPrecisionMode(query string) bool {
	// Short queries use exact/keyword matching for precision
	// Use rune count instead of byte length for proper Unicode support
	if utf8.RuneCountInString(query) <= 10 {
		return true
	}

	// Core expression patterns: containing equals sign or mathematical operators
	// Note: - is intentionally excluded to avoid matching hyphens in compound words (e.g., "go-agent")
	// Note: +, *, / are checked via regex requiring digit adjacency to avoid matching
	//       programming symbols like "C++", "*args", "**kwargs"
	if strings.ContainsAny(query, "=:") || mathExprPattern.MatchString(query) {
		return true
	}

	return false
}

// searchPrecision executes the precision retrieval pipeline.
// It follows strict order: Exact Match -> Keyword -> Vector (fallback)
func (s *RetrievalService) searchPrecision(ctx context.Context, req *SearchRequest) ([]*SearchResult, error) {
	s.logger.Debug("Executing precision search pipeline", "query", req.Query)

	// 1. Exact Match (highest priority)
	exact, err := s.searchExact(ctx, req)
	if err != nil {
		s.logger.Error("Failed to execute exact match search", "error", err)
		return nil, errors.Wrap(err, "exact match search")
	}
	if len(exact) > 0 {
		s.logger.Debug("Precision search: exact match found", "count", len(exact))
		return exact, nil
	}

	// 2. Keyword Search (second priority)
	keyword, err := s.searchKeyword(ctx, req)
	if err != nil {
		s.logger.Error("Failed to execute keyword search", "error", err)
		return nil, errors.Wrap(err, "keyword search")
	}
	if len(keyword) > 0 {
		s.logger.Debug("Precision search: keyword match found", "count", len(keyword))
		return keyword, nil
	}

	// 3. Vector Search (fallback)
	vector, err := s.searchVector(ctx, req)
	if err != nil {
		s.logger.Error("Failed to execute vector search", "error", err)
		return nil, errors.Wrap(err, "vector search")
	}
	s.logger.Debug("Precision search: using vector fallback", "count", len(vector))

	// Mark query as cached to skip redundant rewrites on repeated short queries.
	s.markQueryCached(req.Query)

	return vector, nil
}

// searchExact performs exact substring matching.
func (s *RetrievalService) searchExact(ctx context.Context, req *SearchRequest) ([]*SearchResult, error) {
	s.logger.Debug("Running exact match search", "query", req.Query)

	chunks, err := s.kbRepo.SearchBySubstring(ctx, req.Query, req.TenantID, req.TopK)
	if err != nil {
		s.logger.Error("Exact match search failed", "error", err)
		return nil, errors.Wrap(err, "exact match search")
	}

	if len(chunks) == 0 {
		return []*SearchResult{}, nil
	}

	results := make([]*SearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		results = append(results, &SearchResult{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Score:     1.0, // Fixed highest score for exact matches
			Source:    "knowledge",
			SubSource: "exact",
			CreatedAt: chunk.CreatedAt,
		})
	}

	return results, nil
}

// searchKeyword performs BM25 keyword search with simplified scoring.
func (s *RetrievalService) searchKeyword(ctx context.Context, req *SearchRequest) ([]*SearchResult, error) {
	s.logger.Debug("Running keyword search", "query", req.Query)

	chunks, err := s.kbRepo.SearchByKeyword(ctx, req.Query, req.TenantID, req.TopK)
	if err != nil {
		s.logger.Error("Keyword search failed", "error", err)
		return nil, errors.Wrap(err, "keyword search")
	}

	if len(chunks) == 0 {
		return []*SearchResult{}, nil
	}

	results := make([]*SearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		score := 1.0
		if chunk.Metadata != nil {
			if keywordScore, ok := chunk.Metadata["keyword_score"].(float64); ok {
				score = math.Min(keywordScore, 1.0)
			}
		}

		results = append(results, &SearchResult{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Score:     score,
			Source:    "knowledge",
			SubSource: "keyword",
			CreatedAt: chunk.CreatedAt,
		})
	}

	return results, nil
}

// searchVector performs vector similarity search.
func (s *RetrievalService) searchVector(ctx context.Context, req *SearchRequest) ([]*SearchResult, error) {
	s.logger.Debug("Running vector search", "query", req.Query)

	embedding := s.getEmbeddingCached(ctx, req.Query)
	if len(embedding) == 0 {
		s.logger.Warn("No embedding available for vector search")
		return []*SearchResult{}, nil
	}

	chunks, err := s.kbRepo.SearchByVector(ctx, embedding, req.TenantID, req.TopK)
	if err != nil {
		s.logger.Error("Vector search failed", "error", err)
		return nil, errors.Wrap(err, "vector search")
	}

	if len(chunks) == 0 {
		return []*SearchResult{}, nil
	}

	results := make([]*SearchResult, 0, len(chunks))
	for _, chunk := range chunks {
		score := 0.0
		if chunk.Metadata != nil {
			if similarity, ok := chunk.Metadata["similarity"].(float64); ok {
				score = similarity
			}
		}

		results = append(results, &SearchResult{
			ID:        chunk.ID,
			Content:   chunk.Content,
			Score:     score,
			Source:    "knowledge",
			SubSource: "vector",
			CreatedAt: chunk.CreatedAt,
		})
	}

	return results, nil
}
