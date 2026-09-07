// Package services provides retrieval services for the storage system.
package services

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/errors"
	experience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
	"github.com/Timwood0x10/ares/internal/truncate"
)

// TestDefaultRetrievalPlan tests the default retrieval plan configuration.
func TestDefaultRetrievalPlan(t *testing.T) {
	plan := DefaultRetrievalPlan()

	if !plan.SearchKnowledge {
		t.Error("knowledge search should be enabled by default")
	}
	if !plan.SearchExperience {
		t.Error("experience search should be enabled by default")
	}
	if !plan.SearchTools {
		t.Error("tool search should be enabled by default")
	}
	if plan.SearchTaskResults {
		t.Error("task result search should be disabled by default")
	}

	if plan.KnowledgeWeight != 0.4 {
		t.Errorf("knowledge weight should be 0.4, got %f", plan.KnowledgeWeight)
	}
	if plan.ExperienceWeight != 0.3 {
		t.Errorf("experience weight should be 0.3, got %f", plan.ExperienceWeight)
	}
	if plan.ToolsWeight != 0.2 {
		t.Errorf("tool weight should be 0.2, got %f", plan.ToolsWeight)
	}
	if plan.TaskResultsWeight != 0.1 {
		t.Errorf("task result weight should be 0.1, got %f", plan.TaskResultsWeight)
	}

	if !plan.EnableQueryRewrite {
		t.Error("query rewrite should be enabled by default")
	}
	if !plan.EnableKeywordSearch {
		t.Error("keyword search should be enabled by default")
	}
	if !plan.EnableTimeDecay {
		t.Error("time decay should be enabled by default")
	}

	if plan.TopK != 10 {
		t.Errorf("top_k should be 10 by default, got %d", plan.TopK)
	}
}

// TestCalculateTimeDecay tests the time decay calculation.
func TestCalculateTimeDecay(t *testing.T) {
	service := &RetrievalService{}

	now := time.Now()

	// Test recent content (should have high decay factor)
	recentDecay := service.calculateTimeDecay(now.Add(-1 * time.Hour))
	if recentDecay <= 0.9 {
		t.Errorf("recent content should have high decay factor > 0.9, got %f", recentDecay)
	}

	// Test old content (should have lower decay factor)
	oldDecay := service.calculateTimeDecay(now.Add(-30 * 24 * time.Hour))
	if oldDecay >= recentDecay {
		t.Error("old content should have lower decay factor than recent content")
	}
	if oldDecay < 0.1 {
		t.Errorf("decay factor should not go below 0.1, got %f", oldDecay)
	}

	// Test very old content (should hit minimum threshold)
	veryOldDecay := service.calculateTimeDecay(now.Add(-365 * 24 * time.Hour))
	if veryOldDecay != 0.1 {
		t.Errorf("very old content should hit minimum decay threshold 0.1, got %f", veryOldDecay)
	}
}

// TestFilterByScore tests score filtering.
func TestFilterByTestScore(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.9},
		{ID: "2", Score: 0.7},
		{ID: "3", Score: 0.5},
		{ID: "4", Score: 0.3},
		{ID: "5", Score: 0.1},
	}

	// Test filtering with minimum score 0.5
	filtered := service.filterByScore(results, 0.5)
	if len(filtered) != 3 {
		t.Errorf("should return 3 results with score >= 0.5, got %d", len(filtered))
	}

	// Test filtering with minimum score 0.0 (should return all)
	filtered = service.filterByScore(results, 0.0)
	if len(filtered) != 5 {
		t.Errorf("should return all results when min score is 0, got %d", len(filtered))
	}

	// Test filtering with high minimum score
	filtered = service.filterByScore(results, 0.8)
	if len(filtered) != 1 {
		t.Errorf("should return only 1 result with score >= 0.8, got %d", len(filtered))
	}
}

// TestMergeAndRank tests the merge and rank functionality.
func TestMergeAndRank(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()

	// Create mock keyword results (some overlapping IDs)
	keywordResults := []*SearchResult{
		{ID: "2", Score: 0.6, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "4", Score: 0.5, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-4 * time.Hour)},
		{ID: "5", Score: 0.4, Source: "tool", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-5 * time.Hour)},
	}

	// Create mock vector results
	vectorResults := make([]*SearchResult, 3, 3+len(keywordResults))
	vectorResults[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-1 * time.Hour)}
	vectorResults[1] = &SearchResult{ID: "2", Score: 0.8, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-2 * time.Hour)}
	vectorResults[2] = &SearchResult{ID: "3", Score: 0.7, Source: "tool", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now.Add(-3 * time.Hour)}

	// Merge and rank
	merged := service.mergeAndRerank(append(vectorResults, keywordResults...), plan)

	// Should have 5 unique results
	if len(merged) != 5 {
		t.Errorf("should have 5 unique results, got %d", len(merged))
	}

	// Results should be sorted by score (descending)
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Score < merged[i].Score {
			t.Errorf("results should be sorted by score in descending order, got %f < %f at position %d",
				merged[i-1].Score, merged[i].Score, i)
		}
	}

	// Overlapping result (ID: 2) should have combined score
	result2 := findResultByID(merged, "2")
	require.NotNil(t, result2)

	// Score should reflect combination from both sources
	// Since it appears in both vector (position 1) and keyword (position 0) results
	// the combined score should be: (0.8/2 * 0.3) + (0.6/1 * 0.3) = 0.12 + 0.18 = 0.30 (approx)
	if result2.Score <= 0.1 {
		t.Errorf("combined score should reflect combination from both sources, got %f", result2.Score)
	}
}

// TestSearchRequestValidation tests search request validation.
func TestSearchRequestValidation(t *testing.T) {
	service := &RetrievalService{}

	tests := []struct {
		name        string
		request     *SearchRequest
		expectError bool
		errorType   error
	}{
		{
			name: "valid request",
			request: &SearchRequest{
				Query:    "test query",
				TenantID: "tenant-123",
				TopK:     10,
			},
			expectError: false,
		},
		{
			name:        "nil request",
			request:     nil,
			expectError: true,
			errorType:   errors.ErrInvalidArgument,
		},
		{
			name: "empty query",
			request: &SearchRequest{
				Query:    "",
				TenantID: "tenant-123",
				TopK:     10,
			},
			expectError: true,
			errorType:   errors.ErrInvalidArgument,
		},
		{
			name: "empty tenant ID",
			request: &SearchRequest{
				Query:    "test query",
				TenantID: "",
				TopK:     10,
			},
			expectError: true,
			errorType:   errors.ErrInvalidArgument,
		},
		{
			name: "zero TopK (should be set to default)",
			request: &SearchRequest{
				Query:    "test query",
				TenantID: "tenant-123",
				TopK:     0,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := service.validateRequest(tt.request)

			if tt.expectError {
				if err == nil {
					t.Error("should return error")
				}
				if tt.errorType != nil && err != tt.errorType {
					t.Errorf("should return expected error type %v, got %v", tt.errorType, err)
				}
			} else {
				if err != nil {
					t.Errorf("should not return error, got %v", err)
				}
				if tt.request != nil && tt.request.TopK == 0 {
					if tt.request.TopK != 10 {
						t.Errorf("TopK should be set to default value 10, got %d", tt.request.TopK)
					}
				}
			}
		})
	}
}

// TestShouldRewriteQuery tests query rewrite decision logic.
func TestShouldRewriteQuery(t *testing.T) {
	service := &RetrievalService{}

	tests := []struct {
		name     string
		query    string
		expected bool
	}{
		{
			name:     "short query",
			query:    "test",
			expected: false,
		},
		{
			name:     "empty query",
			query:    "",
			expected: false,
		},
		{
			name:     "Chinese 'how' query",
			query:    "如何使用这个功能",
			expected: true,
		},
		{
			name:     "Chinese 'what' query",
			query:    "什么是机器学习",
			expected: true,
		},
		{
			name:     "English 'how' query",
			query:    "how do I use this feature",
			expected: true,
		},
		{
			name:     "English 'what' query",
			query:    "what is machine learning",
			expected: true,
		},
		{
			name:     "simple statement",
			query:    "this is a simple statement about something",
			expected: false,
		},
		{
			name:     "why question",
			query:    "why does this happen",
			expected: true,
		},
		{
			name:     "explain query",
			query:    "explain the concept of recursion",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.shouldRewriteQuery(tt.query)
			if result != tt.expected {
				t.Errorf("query rewrite decision should be %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestCountResultsBySource tests result counting by source.
func TestCountResultsBySource(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Source: "knowledge"},
		{ID: "2", Source: "knowledge"},
		{ID: "3", Source: "experience"},
		{ID: "4", Source: "tool"},
		{ID: "5", Source: "knowledge"},
		{ID: "6", Source: "tool"},
	}

	counts := service.countResultsBySource(results)

	if counts["knowledge"] != 3 {
		t.Errorf("should count 3 knowledge results, got %d", counts["knowledge"])
	}
	if counts["experience"] != 1 {
		t.Errorf("should count 1 experience result, got %d", counts["experience"])
	}
	if counts["tool"] != 2 {
		t.Errorf("should count 2 tool results, got %d", counts["tool"])
	}
	if counts["task_result"] != 0 {
		t.Errorf("should count 0 task result results, got %d", counts["task_result"])
	}
}

// Helper functions

func findResultByID(results []*SearchResult, id string) *SearchResult {
	for _, result := range results {
		if result.ID == id {
			return result
		}
	}
	return nil
}

// COVERAGE GAP (deliberately NOT papered over with t.Skip):
// Search() — including query rewrite, trace, MinScore filter, TopK limit,
// nil-plan defaulting and the rate-limit error path — has no automated
// coverage. It is untestable without a live database: RetrievalService holds
// a concrete *postgres.Pool and concrete repositories, none of which are
// behind an interface, so no fake can be injected. Closing this gap means
// either (a) integration-tagged tests against a real PG, or (b) extracting
// Pool/repository interfaces in 0.4.x. Empty `t.Skip` shells previously stood
// in for these tests; they never executed even with TEST_POSTGRES_DSN set, so
// they inflated the test count and the coverage denominator while asserting
// nothing. They were removed rather than left as a false signal.

// TestGetEmbedding_EmptyQuery tests getEmbedding with empty query.
func TestGetEmbedding_EmptyQuery(t *testing.T) {
	service := &RetrievalService{
		embeddingClient: nil,
		logger:          slog.Default(),
	}

	embedding := service.getEmbedding(context.Background(), "")

	if embedding != nil {
		t.Error("embedding should be nil for empty query")
	}
}

// TestSearchKnowledgeVector_EmptyEmbedding tests knowledge vector search with empty embedding.
func TestSearchKnowledgeVector_EmptyEmbedding(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10,
	}

	results := service.searchKnowledgeVector(context.Background(), []float64{}, req)

	if results == nil {
		t.Error("results should not be nil")
	}
	if len(results) > 0 {
		t.Errorf("results should be empty for empty embedding, got %d", len(results))
	}
}

// TestTruncateForLog tests string truncation for logging.
func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{
			name:     "short string",
			input:    "hello",
			maxLen:   10,
			expected: "hello",
		},
		{
			name:     "exact length",
			input:    "hello world",
			maxLen:   11,
			expected: "hello world",
		},
		{
			name:     "truncate needed",
			input:    "hello world this is a long string",
			maxLen:   10,
			expected: "hello w...",
		},
		{
			name:     "unicode string",
			input:    "你好世界",
			maxLen:   2,
			expected: "你好",
		},
		{
			name:     "empty string",
			input:    "",
			maxLen:   10,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := truncate.WithEllipsis(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncate.WithEllipsis(%q, %d) = %q, expected %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestToLower tests lowercase conversion.
func TestToLower(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "uppercase",
			input:    "HELLO",
			expected: "hello",
		},
		{
			name:     "mixed case",
			input:    "HeLLo WoRLd",
			expected: "hello world",
		},
		{
			name:     "already lowercase",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "with numbers",
			input:    "ABC123",
			expected: "abc123",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toLower(tt.input)
			if result != tt.expected {
				t.Errorf("toLower(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestContains tests substring containment check.
func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "contains substring",
			s:        "hello world",
			substr:   "world",
			expected: true,
		},
		{
			name:     "does not contain",
			s:        "hello world",
			substr:   "foo",
			expected: false,
		},
		{
			name:     "empty substring",
			s:        "hello world",
			substr:   "",
			expected: true,
		},
		{
			name:     "case insensitive",
			s:        "HELLO WORLD",
			substr:   "world",
			expected: true,
		},
		{
			name:     "same string",
			s:        "hello",
			substr:   "hello",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expected {
				t.Errorf("contains(%q, %q) = %v, expected %v", tt.s, tt.substr, result, tt.expected)
			}
		})
	}
}

// TestMergeAndRank_WithTaskResultSource tests merge and rank with task_result source.
func TestMergeAndRank_WithTaskResultSource(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()

	results := make([]*SearchResult, 1)
	results[0] = &SearchResult{ID: "1", Score: 0.9, Source: "task_result", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 1 {
		t.Errorf("should have 1 result, got %d", len(merged))
	}
	if merged[0].Source != "task_result" {
		t.Errorf("source should be task_result, got %s", merged[0].Source)
	}
}

// TestMergeAndRank_WithUnknownSource tests merge and rank with unknown source.
func TestMergeAndRank_WithUnknownSource(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()

	results := make([]*SearchResult, 1)
	results[0] = &SearchResult{ID: "1", Score: 0.9, Source: "unknown", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 1 {
		t.Errorf("should have 1 result, got %d", len(merged))
	}
	// Unknown source should use weight 1.0
	if merged[0].Score <= 0 {
		t.Errorf("score should be positive for unknown source, got %f", merged[0].Score)
	}
}

// TestFilterByScore_WithNegativeMinScore tests filtering with negative min score (no filtering).
func TestFilterByScore_WithNegativeMinScore(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.9},
		{ID: "2", Score: 0.7},
		{ID: "3", Score: 0.5},
	}

	filtered := service.filterByScore(results, -0.1)

	if len(filtered) != 3 {
		t.Errorf("should return all results with negative min score, got %d", len(filtered))
	}
}

// TestNewRetrievalService tests NewRetrievalService constructor.
func TestNewRetrievalService(t *testing.T) {
	pool := &postgres.Pool{}
	embeddingClient := &embedding.EmbeddingClient{}
	retrievalGuard := &postgres.RetrievalGuard{}
	kbRepo := &repositories.KnowledgeRepository{}

	service := NewRetrievalService(pool, embeddingClient, nil, retrievalGuard, kbRepo,
		nil, /* expRepo */
		nil, /* tool_repo */
	)

	if service.db != pool {
		t.Error("db should be set")
	}
	if service.embeddingClient != embeddingClient {
		t.Error("embeddingClient should be set")
	}
	if service.retrievalGuard != retrievalGuard {
		t.Error("retrievalGuard should be set")
	}
	if service.kbRepo != kbRepo {
		t.Error("kbRepo should be set")
	}
	if service.logger == nil {
		t.Error("logger should be set")
	}
}

// TestTruncateForLog_WithUnicode tests truncate with unicode characters.
func TestTruncateForLog_WithUnicode(t *testing.T) {
	// Test with mixed unicode characters
	// Note: truncate.WithEllipsis counts runes, not bytes
	result := truncate.WithEllipsis("Hello 世界 🌍", 10)
	// The mixed-script sample has 9 runes: H-e-l-l-o-space-world-space-emoji
	// With maxLen=10, it should return the full string without truncation
	expected := "Hello 世界 🌍"
	if result != expected {
		t.Errorf("truncate.WithEllipsis with unicode failed: got %q, expected %q", result, expected)
	}
}

// TestTruncateForLog_WithExactLength tests truncate with exact length.
func TestTruncateForLog_WithExactLength(t *testing.T) {
	result := truncate.WithEllipsis("Hello", 5)
	if result != "Hello" {
		t.Errorf("truncate.WithEllipsis with exact length failed: got %q, expected 'Hello'", result)
	}
}

// TestTruncateForLog_WithZeroMaxLen tests truncate with zero max length.
func TestTruncateForLog_WithZeroMaxLen(t *testing.T) {
	result := truncate.WithEllipsis("Hello", 0)
	if result != "" {
		t.Errorf("truncate.WithEllipsis with zero max length failed: got %q, expected ''", result)
	}
}

// TestShouldRewriteQuery_WithSpecialCharacters tests query rewrite with special characters.
func TestShouldRewriteQuery_WithSpecialCharacters(t *testing.T) {
	service := &RetrievalService{}

	// Test with special characters that should trigger rewrite
	queries := []string{
		"what is this? explain it to me",
		"why does it fail?",
		"how can I fix the error?",
		"describe the process in detail",
	}

	for _, query := range queries {
		if !service.shouldRewriteQuery(query) {
			t.Errorf("query should be rewritten: %q", query)
		}
	}
}

// TestShouldRewriteQuery_WithNumbers tests query rewrite with numbers.
func TestShouldRewriteQuery_WithNumbers(t *testing.T) {
	service := &RetrievalService{}

	// Test with numbers - should still trigger rewrite if pattern matches
	query := "how do I calculate the sum of 2 numbers"
	if !service.shouldRewriteQuery(query) {
		t.Error("query with numbers should be rewritten if pattern matches")
	}
}

// TestShouldRewriteQuery_WithVeryLongQuery tests query rewrite with very long query.
func TestShouldRewriteQuery_WithVeryLongQuery(t *testing.T) {
	service := &RetrievalService{}

	// Very long query without rewrite patterns should not trigger
	// Note: avoid all trigger words: how, what, why, explain, describe, etc.
	longQuery := "this is a very long statement that does not contain any question words " +
		"it represents a detailed review of the system architecture and design " +
		"covering various components and their interactions within the platform"
	if service.shouldRewriteQuery(longQuery) {
		t.Error("long query without rewrite patterns should not trigger rewrite")
	}
}

// TestMergeAndRank_WithAllEmptyResults tests merge and rank with all empty results.
func TestMergeAndRank_WithAllEmptyResults(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	merged := service.mergeAndRerank(append([]*SearchResult{}, []*SearchResult{}...), plan)

	if merged == nil {
		t.Error("merged results should not be nil")
	}
	if len(merged) != 0 {
		t.Errorf("merged results should be empty, got %d", len(merged))
	}
}

// TestMergeAndRank_WithOnlyVectorResults tests merge and rank with only vector results.
func TestMergeAndRank_WithOnlyVectorResults(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	vectorResults := make([]*SearchResult, 2)
	vectorResults[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	vectorResults[1] = &SearchResult{ID: "2", Score: 0.8, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(vectorResults, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Results should be sorted by score
	if merged[0].Score < merged[1].Score {
		t.Error("results should be sorted by score in descending order")
	}
}

// TestMergeAndRank_WithOnlyKeywordResults tests merge and rank with only keyword results.
func TestMergeAndRank_WithOnlyKeywordResults(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	keywordResults := []*SearchResult{
		{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now},
		{ID: "2", Score: 0.8, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now},
	}

	merged := service.mergeAndRerank(append([]*SearchResult{}, keywordResults...), plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
}

// TestMergeAndRank_WithTimeDecayDisabled tests merge and rank with time decay disabled.
func TestMergeAndRank_WithTimeDecayDisabled(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()
	plan.EnableTimeDecay = false

	now := time.Now()
	oldTime := now.Add(-30 * 24 * time.Hour)

	results := make([]*SearchResult, 2)
	results[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: oldTime}
	results[1] = &SearchResult{ID: "2", Score: 0.8, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Without time decay, first result should still be higher due to original score
	if merged[0].ID != "1" {
		t.Error("first result should be the one with higher original score without time decay")
	}
}

// TestMergeAndRank_WithDifferentWeights tests merge and rank with different weights.
func TestMergeAndRank_WithDifferentWeights(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	// Modify weights to test different scenarios
	plan.KnowledgeWeight = 0.8
	plan.ExperienceWeight = 0.2

	now := time.Now()
	results := make([]*SearchResult, 2)
	results[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: 0.9, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Knowledge result should have higher score due to higher weight
	if merged[0].Source != "knowledge" {
		t.Error("knowledge result should be ranked higher due to higher weight")
	}
}

// TestFilterByScore_WithAllResultsFiltered tests filtering when all results are below threshold.
func TestFilterByScore_WithAllResultsFiltered(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.1},
		{ID: "2", Score: 0.2},
		{ID: "3", Score: 0.3},
	}

	filtered := service.filterByScore(results, 0.5)

	if len(filtered) != 0 {
		t.Errorf("should return empty results when all are below threshold, got %d", len(filtered))
	}
}

// TestFilterByScore_WithEmptyResults tests filtering with empty results.
func TestFilterByScore_WithEmptyResults(t *testing.T) {
	service := &RetrievalService{}

	filtered := service.filterByScore([]*SearchResult{}, 0.5)

	if filtered == nil {
		t.Error("filtered results should not be nil")
	}
	if len(filtered) != 0 {
		t.Errorf("filtered results should be empty, got %d", len(filtered))
	}
}

// TestFilterByScore_WithExactMatch tests filtering with exact score match.
func TestFilterByScore_WithExactMatch(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.5},
		{ID: "2", Score: 0.6},
		{ID: "3", Score: 0.4},
	}

	filtered := service.filterByScore(results, 0.5)

	if len(filtered) != 2 {
		t.Errorf("should return 2 results with score >= 0.5, got %d", len(filtered))
	}
}

// TestCountResultsBySource_WithMultipleSources tests counting with multiple sources.
func TestCountResultsBySource_WithMultipleSources(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Source: "knowledge"},
		{ID: "2", Source: "knowledge"},
		{ID: "3", Source: "knowledge"},
		{ID: "4", Source: "experience"},
		{ID: "5", Source: "experience"},
		{ID: "6", Source: "tool"},
		{ID: "7", Source: "tool"},
		{ID: "8", Source: "tool"},
		{ID: "9", Source: "tool"},
		{ID: "10", Source: "task_result"},
	}

	counts := service.countResultsBySource(results)

	if counts["knowledge"] != 3 {
		t.Errorf("knowledge count should be 3, got %d", counts["knowledge"])
	}
	if counts["experience"] != 2 {
		t.Errorf("experience count should be 2, got %d", counts["experience"])
	}
	if counts["tool"] != 4 {
		t.Errorf("tool count should be 4, got %d", counts["tool"])
	}
	if counts["task_result"] != 1 {
		t.Errorf("task_result count should be 1, got %d", counts["task_result"])
	}
}

// TestValidateRequest_WithNegativeTopK tests validation with negative TopK.
func TestValidateRequest_WithNegativeTopK(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     -5,
	}

	err := service.validateRequest(req)

	if err != nil {
		t.Errorf("should not return error for negative TopK (should auto-correct), got %v", err)
	}
	if req.TopK != 10 {
		t.Errorf("TopK should be set to default value 10, got %d", req.TopK)
	}
}

// TestValidateRequest_WithVeryLargeTopK tests validation with very large TopK.
func TestValidateRequest_WithVeryLargeTopK(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10000,
	}

	err := service.validateRequest(req)

	if err != nil {
		t.Errorf("should not return error for large TopK, got %v", err)
	}
}

// COVERAGE GAP (deliberately NOT papered over with t.Skip):
// searchKnowledgeVector / bm25SearchKnowledge — both the error paths and the
// result paths (including similarity and keyword-score metadata) — have no
// automated coverage. They talk to a concrete knowledge repository type, not
// an interface, so no fake can be injected. See the gap note above Search()
// for why empty skip shells were removed instead of retained.

// TestCalculateTimeDecay_WithZeroAge tests time decay with zero age.
func TestCalculateTimeDecay_WithZeroAge(t *testing.T) {
	service := &RetrievalService{}

	now := time.Now()
	decay := service.calculateTimeDecay(now)

	if decay <= 0.9 {
		t.Errorf("time decay for zero age should be close to 1.0, got %f", decay)
	}
}

// TestCalculateTimeDecay_WithFutureTime tests time decay with future time.
func TestCalculateTimeDecay_WithFutureTime(t *testing.T) {
	service := &RetrievalService{}

	futureTime := time.Now().Add(1 * time.Hour)
	decay := service.calculateTimeDecay(futureTime)

	// Future time may have decay > 1.0 due to negative age in exponential formula
	// The important thing is it's still reasonable (not extremely high)
	if decay < 0.9 {
		t.Errorf("time decay for future time should be high, got %f", decay)
	}
	if decay > 1.5 {
		t.Errorf("time decay for future time should not be extremely high, got %f", decay)
	}
}

// TestMergeAndRank_WithDuplicateIDs tests merge and rank with duplicate IDs.
func TestMergeAndRank_WithDuplicateIDs(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()

	// Same ID in both vector and keyword results
	keywordResults := []*SearchResult{
		{ID: "1", Score: 0.7, Source: "knowledge", SubSource: "keyword", QueryWeight: 1.0, CreatedAt: now},
	}

	vectorResults := make([]*SearchResult, 1, 1+len(keywordResults))
	vectorResults[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(append(vectorResults, keywordResults...), plan)

	if len(merged) != 1 {
		t.Errorf("should have 1 unique result, got %d", len(merged))
	}
	// Score calculation (new implementation):
	// Dedup: first result 0.9, second result adds 0.7 * 0.3 = 0.21, total = 1.11
	// Rerank: baseScore 1.11 * queryWeight 1.0 * sourceWeight 0.4 (knowledge) * subSourceWeight 1.0 (vector) = 0.444
	// The score should be > 0.3 (individual contributions after dedup but before source weight)
	if merged[0].Score <= 0.3 {
		t.Errorf("combined score should be higher than individual contributions, got %f", merged[0].Score)
	}
}

// TestMergeAndRank_WithMultipleDuplicates tests merge and rank with multiple duplicate IDs.
func TestMergeAndRank_WithMultipleDuplicates(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()

	keywordResults := []*SearchResult{
		{ID: "1", Score: 0.6, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now},
		{ID: "2", Score: 0.5, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now},
		{ID: "4", Score: 0.4, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now},
	}

	vectorResults := make([]*SearchResult, 3, 3+len(keywordResults))
	vectorResults[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	vectorResults[1] = &SearchResult{ID: "2", Score: 0.8, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	vectorResults[2] = &SearchResult{ID: "3", Score: 0.7, Source: "tool", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(append(vectorResults, keywordResults...), plan)

	if len(merged) != 4 {
		t.Errorf("should have 4 unique results, got %d", len(merged))
	}
}

// TestSearchRequest_PlanNotNil tests that validation passes with nil plan.
func TestSearchRequest_PlanNotNil(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10,
		Plan:     nil,
	}

	err := service.validateRequest(req)

	// validateRequest should pass even with nil plan
	// Setting default plan is done in Search function, not validateRequest
	if err != nil {
		t.Errorf("should not return error with nil plan, got %v", err)
	}
	// After validation, plan is still nil (default plan is set in Search)
	if req.Plan != nil {
		t.Error("plan should remain nil after validation (default is set in Search)")
	}
}

// TestDefaultRetrievalPlan_WeightsSum tests that weights sum to 1.0.
func TestDefaultRetrievalPlan_WeightsSum(t *testing.T) {
	plan := DefaultRetrievalPlan()

	totalWeight := plan.KnowledgeWeight + plan.ExperienceWeight + plan.ToolsWeight + plan.TaskResultsWeight

	// Use small epsilon for floating point comparison
	if math.Abs(totalWeight-1.0) > 0.0001 {
		t.Errorf("default weights should sum to 1.0, got %f", totalWeight)
	}
}

// TestRetrievalPlan_AllSourcesDisabled tests plan with all sources disabled.
func TestRetrievalPlan_AllSourcesDisabled(t *testing.T) {
	plan := DefaultRetrievalPlan()
	plan.SearchKnowledge = false
	plan.SearchExperience = false
	plan.SearchTools = false
	plan.SearchTaskResults = false

	if plan.SearchKnowledge || plan.SearchExperience || plan.SearchTools || plan.SearchTaskResults {
		t.Error("all sources should be disabled")
	}
}

// TestRetrievalPlan_AllSourcesEnabled tests plan with all sources enabled.
func TestRetrievalPlan_AllSourcesEnabled(t *testing.T) {
	plan := DefaultRetrievalPlan()
	plan.SearchKnowledge = true
	plan.SearchExperience = true
	plan.SearchTools = true
	plan.SearchTaskResults = true

	if !plan.SearchKnowledge || !plan.SearchExperience || !plan.SearchTools || !plan.SearchTaskResults {
		t.Error("all sources should be enabled")
	}
}

// TestContains_WithEmptyString tests contains with empty strings.
func TestContains_WithEmptyString(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		substr   string
		expected bool
	}{
		{
			name:     "empty string, empty substring",
			s:        "",
			substr:   "",
			expected: true,
		},
		{
			name:     "empty string, non-empty substring",
			s:        "",
			substr:   "hello",
			expected: false,
		},
		{
			name:     "non-empty string, empty substring",
			s:        "hello",
			substr:   "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expected {
				t.Errorf("contains(%q, %q) = %v, expected %v", tt.s, tt.substr, result, tt.expected)
			}
		})
	}
}

// TestGetEmbedding_WithNilClient tests getEmbedding with nil embedding client.
func TestGetEmbedding_WithNilClient(t *testing.T) {
	service := &RetrievalService{
		embeddingClient: nil,
		logger:          slog.Default(),
	}

	embedding := service.getEmbedding(context.Background(), "test query")

	if embedding != nil {
		t.Error("embedding should be nil when embeddingClient is nil")
	}
}

// COVERAGE GAP (deliberately NOT papered over with t.Skip):
// getEmbedding's happy path (valid client) is uncovered: embedding.EmbeddingClient
// is a concrete struct, so there is nothing to fake. The nil-client path IS
// covered by TestGetEmbedding_NilClient above.

// TestShouldRewriteQuery_WithExactly10Chars tests query rewrite with exactly 10 characters.
func TestShouldRewriteQuery_WithExactly10Chars(t *testing.T) {
	service := &RetrievalService{}

	// Exactly 10 characters without rewrite pattern should not trigger
	query := "justastmt"
	if service.shouldRewriteQuery(query) {
		t.Error("10-char query without pattern should not trigger rewrite")
	}

	// Exactly 10 characters with rewrite pattern should trigger
	queryWithPattern := "how todoit"
	if !service.shouldRewriteQuery(queryWithPattern) {
		t.Error("10-char query with pattern should trigger rewrite")
	}
}

// TestShouldRewriteQuery_WithChinesePatterns tests query rewrite with Chinese patterns.
func TestShouldRewriteQuery_WithChinesePatterns(t *testing.T) {
	service := &RetrievalService{}

	chineseQueries := []string{
		"如何解决这个问题",
		"怎么使用这个API",
		"什么是机器学习",
		"为什么会出现这个错误",
		"解释一下这个概念",
	}

	for _, query := range chineseQueries {
		if !service.shouldRewriteQuery(query) {
			t.Errorf("Chinese query should be rewritten: %q", query)
		}
	}
}

// TestShouldRewriteQuery_WithMixedCasePatterns tests query rewrite with mixed case patterns.
func TestShouldRewriteQuery_WithMixedCasePatterns(t *testing.T) {
	service := &RetrievalService{}

	mixedCaseQueries := []string{
		"How does this work",
		"WHAT IS THIS",
		"Why Do We Need This",
		"Explain The Process",
		"Describe The System",
	}

	for _, query := range mixedCaseQueries {
		if !service.shouldRewriteQuery(query) {
			t.Errorf("Mixed case query should be rewritten: %q", query)
		}
	}
}

// TestShouldRewriteQuery_WithPatternAtEnd tests query rewrite with pattern at end.
func TestShouldRewriteQuery_WithPatternAtEnd(t *testing.T) {
	service := &RetrievalService{}

	query := "this is a long statement about something why"
	if !service.shouldRewriteQuery(query) {
		t.Error("query with pattern at end should trigger rewrite")
	}
}

// TestShouldRewriteQuery_WithPatternAtBeginning tests query rewrite with pattern at beginning.
func TestShouldRewriteQuery_WithPatternAtBeginning(t *testing.T) {
	service := &RetrievalService{}

	query := "how does this system work with various components"
	if !service.shouldRewriteQuery(query) {
		t.Error("query with pattern at beginning should trigger rewrite")
	}
}

// TestShouldRewriteQuery_WithMultiplePatterns tests query rewrite with multiple patterns.
func TestShouldRewriteQuery_WithMultiplePatterns(t *testing.T) {
	service := &RetrievalService{}

	query := "how and why do we need to explain what this is"
	if !service.shouldRewriteQuery(query) {
		t.Error("query with multiple patterns should trigger rewrite")
	}
}

// TestSearchExperienceVector tests experience vector search (empty implementation).
func TestSearchExperienceVector_Empty(t *testing.T) {
	service := &RetrievalService{
		logger: slog.Default(),
	}

	embedding := []float64{0.1, 0.2, 0.3}
	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10,
	}

	results := service.searchExperienceVector(context.Background(), embedding, req)
	if results == nil {
		t.Error("results should not be nil")
	}
	if len(results) != 0 {
		t.Errorf("experience search should return empty results, got %d", len(results))
	}
}

// TestSearchToolsVector tests tools vector search (empty implementation).
func TestSearchToolsVector_Empty(t *testing.T) {
	service := &RetrievalService{
		logger: slog.Default(),
	}

	embedding := []float64{0.1, 0.2, 0.3}
	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10,
	}

	results := service.searchToolsVector(context.Background(), embedding, req)

	if results == nil {
		t.Error("results should not be nil")
	}
	if len(results) != 0 {
		t.Errorf("tools search should return empty results, got %d", len(results))
	}
}

// TestBm25SearchExperience tests experience BM25 search (empty implementation).
func TestBm25SearchExperience_Empty(t *testing.T) {
	service := &RetrievalService{}

	results := service.bm25SearchExperience(context.Background(), "test query", "tenant-123", 10)

	if results == nil {
		t.Error("results should not be nil")
	}
	if len(results) != 0 {
		t.Errorf("experience BM25 search should return empty results, got %d", len(results))
	}
}

// TestBm25SearchTools tests tools BM25 search (empty implementation).
func TestBm25SearchTools_Empty(t *testing.T) {
	service := &RetrievalService{}

	results := service.bm25SearchTools(context.Background(), "test query", "tenant-123", 10)

	if results == nil {
		t.Error("results should not be nil")
	}
	if len(results) != 0 {
		t.Errorf("tools BM25 search should return empty results, got %d", len(results))
	}
}

// TestMergeAndRank_WithZeroScores tests merge and rank with zero scores.
func TestMergeAndRank_WithZeroScores(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	results := make([]*SearchResult, 2)
	results[0] = &SearchResult{ID: "1", Score: 0.0, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: 0.0, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Both should have zero score
	if merged[0].Score != 0.0 || merged[1].Score != 0.0 {
		t.Error("results with zero scores should remain zero")
	}
}

// TestMergeAndRank_WithVeryHighScores tests merge and rank with very high scores.
func TestMergeAndRank_WithVeryHighScores(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	results := make([]*SearchResult, 2)
	results[0] = &SearchResult{ID: "1", Score: 100.0, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: 99.0, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Should still be sorted correctly
	if merged[0].Score < merged[1].Score {
		t.Error("results should be sorted by score in descending order")
	}
}

// TestMergeAndRank_WithNegativeScores tests merge and rank with negative scores.
func TestMergeAndRank_WithNegativeScores(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	results := make([]*SearchResult, 2)
	results[0] = &SearchResult{ID: "1", Score: -0.5, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: -0.3, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 2 {
		t.Errorf("should have 2 results, got %d", len(merged))
	}
	// Negative scores should be handled correctly
	if merged[0].Score < merged[1].Score {
		t.Error("negative scores should be sorted correctly")
	}
}

// TestFilterByScore_WithVeryHighThreshold tests filtering with very high threshold.
func TestFilterByScore_WithVeryHighThreshold(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.9},
		{ID: "2", Score: 0.95},
		{ID: "3", Score: 0.99},
	}

	filtered := service.filterByScore(results, 1.0)

	if len(filtered) != 0 {
		t.Errorf("should return empty results when threshold is above all scores, got %d", len(filtered))
	}
}

// TestFilterByScore_WithVeryLowThreshold tests filtering with very low threshold.
func TestFilterByScore_WithVeryLowThreshold(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.1},
		{ID: "2", Score: 0.2},
		{ID: "3", Score: 0.3},
	}

	filtered := service.filterByScore(results, 0.0)

	if len(filtered) != 3 {
		t.Errorf("should return all results when threshold is 0, got %d", len(filtered))
	}
}

// TestValidateRequest_WithWhitespaceQuery tests validation with whitespace-only query.
func TestValidateRequest_WithWhitespaceQuery(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "   ",
		TenantID: "tenant-123",
		TopK:     10,
	}

	err := service.validateRequest(req)

	// validateRequest only checks for empty string, not whitespace
	// Whitespace-only query will pass validation (this may be a bug in implementation)
	if err != nil {
		t.Errorf("whitespace-only query currently passes validation, got error: %v", err)
	}
}

// TestValidateRequest_WithWhitespaceTenantID tests validation with whitespace-only tenant ID.
func TestValidateRequest_WithWhitespaceTenantID(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "   ",
		TopK:     10,
	}

	err := service.validateRequest(req)

	// validateRequest only checks for empty string, not whitespace
	// Whitespace-only tenant ID will pass validation (this may be a bug in implementation)
	if err != nil {
		t.Errorf("whitespace-only tenant ID currently passes validation, got error: %v", err)
	}
}

// TestCalculateTimeDecay_WithLargeAge tests time decay with very old content.
func TestCalculateTimeDecay_WithLargeAge(t *testing.T) {
	service := &RetrievalService{}

	veryOldTime := time.Now().Add(-10 * 365 * 24 * time.Hour) // 10 years ago
	decay := service.calculateTimeDecay(veryOldTime)

	// Should hit minimum threshold
	if decay != 0.1 {
		t.Errorf("time decay for very old content should be minimum threshold 0.1, got %f", decay)
	}
}

// TestRetrievalPlan_CustomWeights tests custom retrieval plan weights.
func TestRetrievalPlan_CustomWeights(t *testing.T) {
	plan := &RetrievalPlan{
		SearchKnowledge:   true,
		SearchExperience:  true,
		SearchTools:       true,
		SearchTaskResults: false,

		KnowledgeWeight:   0.7,
		ExperienceWeight:  0.2,
		ToolsWeight:       0.1,
		TaskResultsWeight: 0.0,

		EnableQueryRewrite:  true,
		EnableKeywordSearch: true,
		EnableTimeDecay:     true,

		TopK: 20,
	}

	totalWeight := plan.KnowledgeWeight + plan.ExperienceWeight + plan.ToolsWeight + plan.TaskResultsWeight

	if math.Abs(totalWeight-1.0) > 0.0001 {
		t.Errorf("custom weights should sum to 1.0, got %f", totalWeight)
	}
	if plan.TopK != 20 {
		t.Errorf("TopK should be 20, got %d", plan.TopK)
	}
}

// TestTruncateForLog_WithVeryShortMaxLen tests truncate with very short max length.
func TestTruncateForLog_WithVeryShortMaxLen(t *testing.T) {
	result := truncate.WithEllipsis("Hello World", 1)

	// With maxLen=1 (<= ellipsis length), it truncates to 1 rune without ellipsis: "H"
	if result != "H" {
		t.Errorf("truncate.WithEllipsis with maxLen=1 should return 'H', got %q", result)
	}
}

// TestTruncateForLog_WithSingleChar tests truncate with single character.
func TestTruncateForLog_WithSingleChar(t *testing.T) {
	result := truncate.WithEllipsis("H", 1)

	if result != "H" {
		t.Errorf("truncate.WithEllipsis with single char should return the char, got %q", result)
	}
}

// TestToLower_WithSpecialChars tests lowercase conversion with special characters.
func TestToLower_WithSpecialChars(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"HELLO@WORLD", "hello@world"},
		{"123ABC", "123abc"},
		{"TEST_CASE", "test_case"},
		{"TEST-CASE", "test-case"},
		{"TEST CASE", "test case"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toLower(tt.input)
			if result != tt.expected {
				t.Errorf("toLower(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestContains_WithOverlappingPatterns tests contains with overlapping patterns.
func TestContains_WithOverlappingPatterns(t *testing.T) {
	tests := []struct {
		s        string
		substr   string
		expected bool
	}{
		{"aaa", "aa", true},
		{"ababab", "aba", true},
		{"testtest", "test", true},
		{"abc", "abcd", false},
	}

	for _, tt := range tests {
		t.Run(tt.s+"/"+tt.substr, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expected {
				t.Errorf("contains(%q, %q) = %v, expected %v", tt.s, tt.substr, result, tt.expected)
			}
		})
	}
}

// TestMergeAndRank_WithLargeResultSet tests merge and rank with many results.
func TestMergeAndRank_WithLargeResultSet(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	keywordResults := make([]*SearchResult, 50)
	for i := 0; i < 50; i++ {
		keywordResults[i] = &SearchResult{
			ID:        fmt.Sprintf("keyword-%d", i),
			Score:     float64(50-i) / 100.0,
			Source:    "knowledge",
			CreatedAt: now,
		}
	}

	vectorResults := make([]*SearchResult, 50, 50+len(keywordResults))
	for i := 0; i < 50; i++ {
		vectorResults[i] = &SearchResult{
			ID:        fmt.Sprintf("vector-%d", i),
			Score:     float64(50-i) / 100.0,
			Source:    "knowledge",
			CreatedAt: now,
		}
	}

	merged := service.mergeAndRerank(append(vectorResults, keywordResults...), plan)

	if len(merged) != 100 {
		t.Errorf("should have 100 unique results, got %d", len(merged))
	}
	// Results should be sorted
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Score < merged[i].Score {
			t.Errorf("results should be sorted by score at position %d", i)
		}
	}
}

// TestMergeAndRank_WithDifferentSourcesAll tests merge and rank with all different sources.
func TestMergeAndRank_WithDifferentSourcesAll(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	results := make([]*SearchResult, 4)
	results[0] = &SearchResult{ID: "1", Score: 0.9, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: 0.8, Source: "experience", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[2] = &SearchResult{ID: "3", Score: 0.7, Source: "tool", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[3] = &SearchResult{ID: "4", Score: 0.6, Source: "task_result", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 4 {
		t.Errorf("should have 4 results, got %d", len(merged))
	}
	// Results should be sorted by score (weighted by source)
	for i := 1; i < len(merged); i++ {
		if merged[i-1].Score < merged[i].Score {
			t.Error("results should be sorted by score")
		}
	}
}

// TestFilterByScore_WithExactThresholdMatch tests filtering with exact threshold matches.
func TestFilterByScore_WithExactThresholdMatch(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.5},
		{ID: "2", Score: 0.5},
		{ID: "3", Score: 0.5},
	}

	filtered := service.filterByScore(results, 0.5)

	if len(filtered) != 3 {
		t.Errorf("should return all results when scores match threshold exactly, got %d", len(filtered))
	}
}

// TestFilterByScore_WithMixedScores tests filtering with mixed scores.
func TestFilterByScore_WithMixedScores(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Score: 0.1},
		{ID: "2", Score: 0.3},
		{ID: "3", Score: 0.5},
		{ID: "4", Score: 0.7},
		{ID: "5", Score: 0.9},
	}

	filtered := service.filterByScore(results, 0.5)

	if len(filtered) != 3 {
		t.Errorf("should return 3 results with score >= 0.5, got %d", len(filtered))
	}
}

// TestCalculateTimeDecay_WithSpecificAges tests time decay with specific age values.
func TestCalculateTimeDecay_WithSpecificAges(t *testing.T) {
	service := &RetrievalService{}

	now := time.Now()

	tests := []struct {
		name     string
		age      time.Duration
		minDecay float64
		maxDecay float64
	}{
		{
			name:     "1 minute",
			age:      1 * time.Minute,
			minDecay: 0.99,
			maxDecay: 1.0,
		},
		{
			name:     "1 hour",
			age:      1 * time.Hour,
			minDecay: 0.9,
			maxDecay: 1.0,
		},
		{
			name:     "1 day",
			age:      24 * time.Hour,
			minDecay: 0.7,
			maxDecay: 0.9,
		},
		{
			name:     "1 week",
			age:      7 * 24 * time.Hour,
			minDecay: 0.15,
			maxDecay: 0.25,
		},
		{
			name:     "1 month",
			age:      30 * 24 * time.Hour,
			minDecay: 0.1,
			maxDecay: 0.15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decay := service.calculateTimeDecay(now.Add(-tt.age))
			if decay < tt.minDecay || decay > tt.maxDecay {
				t.Errorf("decay for %v should be in range [%.2f, %.2f], got %.2f",
					tt.age, tt.minDecay, tt.maxDecay, decay)
			}
		})
	}
}

// TestCountResultsBySource_WithAllSources tests counting with all source types.
func TestCountResultsBySource_WithAllSources(t *testing.T) {
	service := &RetrievalService{}

	results := []*SearchResult{
		{ID: "1", Source: "knowledge"},
		{ID: "2", Source: "experience"},
		{ID: "3", Source: "tool"},
		{ID: "4", Source: "task_result"},
		{ID: "5", Source: "knowledge"},
		{ID: "6", Source: "experience"},
		{ID: "7", Source: "tool"},
		{ID: "8", Source: "task_result"},
	}

	counts := service.countResultsBySource(results)

	if counts["knowledge"] != 2 {
		t.Errorf("knowledge count should be 2, got %d", counts["knowledge"])
	}
	if counts["experience"] != 2 {
		t.Errorf("experience count should be 2, got %d", counts["experience"])
	}
	if counts["tool"] != 2 {
		t.Errorf("tool count should be 2, got %d", counts["tool"])
	}
	if counts["task_result"] != 2 {
		t.Errorf("task_result count should be 2, got %d", counts["task_result"])
	}
}

// TestValidateRequest_WithAllFieldsValid tests validation with all valid fields.
func TestValidateRequest_WithAllFieldsValid(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     10,
		Plan:     DefaultRetrievalPlan(),
	}

	err := service.validateRequest(req)

	if err != nil {
		t.Errorf("should not return error for valid request, got %v", err)
	}
}

// TestValidateRequest_WithTopKOne tests validation with TopK = 1.
func TestValidateRequest_WithTopKOne(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     1,
	}

	err := service.validateRequest(req)

	if err != nil {
		t.Errorf("should not return error for TopK=1, got %v", err)
	}
	if req.TopK != 1 {
		t.Errorf("TopK should remain 1, got %d", req.TopK)
	}
}

// TestValidateRequest_WithTopKHuge tests validation with very large TopK.
func TestValidateRequest_WithTopKHuge(t *testing.T) {
	service := &RetrievalService{}

	req := &SearchRequest{
		Query:    "test query",
		TenantID: "tenant-123",
		TopK:     1000000,
	}

	err := service.validateRequest(req)

	if err != nil {
		t.Errorf("should not return error for huge TopK, got %v", err)
	}
}

// TestTruncateForLog_WithSpecialCharacters tests truncate with special characters.
func TestTruncateForLog_WithSpecialCharacters(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"Hello@World!", 5, "He..."},
		{"Test#123$%", 10, "Test#123$%"}, // 10 characters, no truncation
		{"Test#123$%", 8, "Test#..."},    // 8-3=5 chars: "Test#" + "..."
		{"&*()%$#@!", 3, "&*("},          // maxLen=3 equals ellipsis length, no ellipsis
	}

	for _, tt := range tests {
		t.Run(tt.input+"/"+fmt.Sprintf("%d", tt.maxLen), func(t *testing.T) {
			result := truncate.WithEllipsis(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncate.WithEllipsis(%q, %d) = %q, expected %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

// TestToLower_WithNumbers tests lowercase conversion with numbers.
func TestToLower_WithNumbers(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ABC123DEF", "abc123def"},
		{"A1B2C3", "a1b2c3"},
		{"123ABC", "123abc"},
		{"123", "123"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toLower(tt.input)
			if result != tt.expected {
				t.Errorf("toLower(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestContains_WithCaseSensitivity tests contains with case sensitivity.
func TestContains_WithCaseSensitivity(t *testing.T) {
	tests := []struct {
		s        string
		substr   string
		expected bool
	}{
		{"Hello World", "hello", true}, // Case insensitive
		{"Hello World", "WORLD", true}, // Case insensitive
		{"Hello World", "world", true}, // Case insensitive
		{"Hello World", "HELLO", true}, // Case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.s+"/"+tt.substr, func(t *testing.T) {
			result := contains(tt.s, tt.substr)
			if result != tt.expected {
				t.Errorf("contains(%q, %q) = %v, expected %v", tt.s, tt.substr, result, tt.expected)
			}
		})
	}
}

// TestMergeAndRank_WithIdenticalScores tests merge and rank with identical scores.
func TestMergeAndRank_WithIdenticalScores(t *testing.T) {
	service := &RetrievalService{}
	plan := DefaultRetrievalPlan()

	now := time.Now()
	results := make([]*SearchResult, 3)
	results[0] = &SearchResult{ID: "1", Score: 0.5, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[1] = &SearchResult{ID: "2", Score: 0.5, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}
	results[2] = &SearchResult{ID: "3", Score: 0.5, Source: "knowledge", SubSource: "vector", QueryWeight: 1.0, CreatedAt: now}

	merged := service.mergeAndRerank(results, plan)

	if len(merged) != 3 {
		t.Errorf("should have 3 results, got %d", len(merged))
	}
	// All should have the same score (normalized by position)
	for i, result := range merged {
		if result.Score <= 0 {
			t.Errorf("result %d should have positive score, got %f", i, result.Score)
		}
	}
}

// TestIsQueryInCache tests the query cache hit/miss/expiry behavior.
func TestIsQueryInCache(t *testing.T) {
	s := &RetrievalService{
		queryCache:       make(map[string]time.Time),
		queryCacheTTL:    100 * time.Millisecond,
		queryCacheMaxLen: 10,
	}

	// Empty query returns false.
	if s.isQueryInCache("") {
		t.Error("empty query should return false")
	}

	// Miss returns false.
	if s.isQueryInCache("hello world") {
		t.Error("uncached query should return false")
	}

	// Hit returns true (use markQueryCached for normalization consistency).
	s.markQueryCached("hello world")
	if !s.isQueryInCache("hello world") {
		t.Error("cached query should return true")
	}

	// Case-insensitive normalization.
	s.markQueryCached("Hello World")
	if !s.isQueryInCache("hello world") {
		t.Error("cached query should match case-insensitively")
	}

	// Trim normalization.
	s.markQueryCached("  spaced  ")
	if !s.isQueryInCache("spaced") {
		t.Error("cached query should match after trim normalization")
	}
}

// TestIsQueryInCache_Expiry tests that expired entries return false.
// Expired entry cleanup is deferred to markQueryCached to avoid write locks on the read path.
func TestIsQueryInCache_Expiry(t *testing.T) {
	s := &RetrievalService{
		queryCache:       make(map[string]time.Time),
		queryCacheTTL:    50 * time.Millisecond,
		queryCacheMaxLen: 10,
	}

	s.queryCache["test"] = time.Now().Add(-1 * time.Second)

	if s.isQueryInCache("test") {
		t.Error("expired entry should return false")
	}

	// Expired entries remain in the map until markQueryCached cleans them up.
	// isQueryInCache only reads and reports status.
	if _, exists := s.queryCache["test"]; !exists {
		t.Error("expired entry should still exist in map until lazy cleanup")
	}
}

// TestMarkQueryCached tests marking queries as cached.
func TestMarkQueryCached(t *testing.T) {
	s := &RetrievalService{
		queryCache:       make(map[string]time.Time),
		queryCacheTTL:    10 * time.Minute,
		queryCacheMaxLen: 3,
	}

	// Empty query is no-op.
	s.markQueryCached("")
	if len(s.queryCache) != 0 {
		t.Error("empty query should not be cached")
	}

	// Normal caching works.
	s.markQueryCached("query1")
	if len(s.queryCache) != 1 {
		t.Errorf("expected 1 entry, got %d", len(s.queryCache))
	}

	// Overwrite existing entry.
	s.markQueryCached("query1")
	if len(s.queryCache) != 1 {
		t.Errorf("overwrite should keep 1 entry, got %d", len(s.queryCache))
	}
}

// TestMarkQueryCached_Eviction tests cache eviction when at capacity.
func TestMarkQueryCached_Eviction(t *testing.T) {
	s := &RetrievalService{
		queryCache:       make(map[string]time.Time),
		queryCacheTTL:    10 * time.Minute,
		queryCacheMaxLen: 3,
	}

	s.markQueryCached("query1")
	s.markQueryCached("query2")
	s.markQueryCached("query3")

	if len(s.queryCache) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(s.queryCache))
	}

	// Adding a 4th entry should trigger eviction of the oldest.
	s.markQueryCached("query4")

	if len(s.queryCache) != 3 {
		t.Errorf("after eviction expected 3 entries, got %d", len(s.queryCache))
	}

	// Oldest entry ("query1") should have been evicted.
	if _, exists := s.queryCache["query1"]; exists {
		t.Error("oldest entry should have been evicted")
	}

	// Newer entries should still exist.
	for _, key := range []string{"query2", "query3", "query4"} {
		if _, exists := s.queryCache[key]; !exists {
			t.Errorf("entry %q should still exist", key)
		}
	}
}

// TestNormalizeQueryForCache tests query normalization for cache keys.
func TestNormalizeQueryForCache(t *testing.T) {
	s := &RetrievalService{}

	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello world"},
		{"  spaces  ", "spaces"},
		{"", ""},
		{"如何", "如何"},
		{"UPPER", "upper"},
	}

	for _, tt := range tests {
		result := s.normalizeQueryForCache(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeQueryForCache(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

// TestRetrievalService_ExperienceRankingWired is a contract test for the
// wiring seam that ProductionMemoryManager relies on: after SetExperienceServices
// is called with a ranking service and a conflict resolver, applyExperienceRanking
// must actually rank and conflict-resolve the experiences instead of falling back
// to plain conversion. Previously nothing called SetExperienceServices in
// production, so rankingService/conflictResolver stayed nil and this path never ran.
func TestRetrievalService_ExperienceRankingWired(t *testing.T) {
	s := &RetrievalService{}
	s.SetExperienceServices(nil, experience.NewRankingService(), experience.NewConflictResolver())

	plan := DefaultRetrievalPlan()
	plan.TopK = 20

	exp1 := &storage_models.Experience{
		ID:         "exp-1",
		TenantID:   "tenant-1",
		Type:       "task",
		Problem:    "config parse failed",
		Solution:   "use strict YAML parser",
		Metadata:   map[string]interface{}{"similarity": 0.9},
		UsageCount: 5,
	}
	exp2 := &storage_models.Experience{
		ID:         "exp-2",
		TenantID:   "tenant-1",
		Type:       "task",
		Problem:    "config parse failed",
		Solution:   "fallback to defaults on error",
		Metadata:   map[string]interface{}{"similarity": 0.8},
		UsageCount: 1,
	}

	req := &SearchRequest{Query: "config parse", Plan: plan}
	results := s.applyExperienceRanking(context.Background(), []*storage_models.Experience{exp1, exp2}, req)

	require.NotEmpty(t, results, "wired experience ranking should return ranked results")
	for _, r := range results {
		assert.Equal(t, "experience", r.Source, "result should be attributed to experience source")
	}
	assert.LessOrEqual(t, len(results), plan.TopK, "results must respect TopK cap")
}

// TestRetrievalService_ExperienceRankingConflictResolve verifies that when the
// conflict resolver is wired and ExperienceConflictResolve is enabled, duplicate
// problems are collapsed into a single resolved result (dedup via grouping).
func TestRetrievalService_ExperienceRankingConflictResolve(t *testing.T) {
	s := &RetrievalService{}
	s.SetExperienceServices(nil, experience.NewRankingService(), experience.NewConflictResolver())

	plan := DefaultRetrievalPlan()
	plan.TopK = 20
	plan.ExperienceConflictResolve = true

	// Three experiences with the same problem and identical non-zero embeddings
	// must collapse to one resolved entry: cosine similarity = 1.0 exceeds the
	// default 0.9 threshold, so DetectConflictGroups forms a single group and
	// Resolve keeps only its best-ranked representative.
	experiences := make([]*storage_models.Experience, 0, 3)
	for i := 0; i < 3; i++ {
		experiences = append(experiences, &storage_models.Experience{
			ID:         fmt.Sprintf("exp-%d", i),
			TenantID:   "tenant-1",
			Type:       "task",
			Problem:    "duplicate problem",
			Solution:   fmt.Sprintf("solution %d", i),
			Embedding:  []float64{0.1, 0.2, 0.3},
			Metadata:   map[string]interface{}{"similarity": 0.9},
			UsageCount: 1,
		})
	}

	req := &SearchRequest{Query: "duplicate problem", Plan: plan}
	results := s.applyExperienceRanking(context.Background(), experiences, req)

	// All three share one conflict group, so exactly one representative must
	// survive; with identical inputs the group's first member wins the tie.
	require.Len(t, results, 1, "duplicate experiences must collapse to one resolved entry")
	assert.Equal(t, "exp-0", results[0].ID, "best-ranked experience should win the conflict group")
}
