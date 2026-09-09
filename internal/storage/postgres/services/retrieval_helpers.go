// Package services provides retrieval services for the storage system.
//
// This file holds debug-info generation, time-decay, and shared string utilities
// for the retrieval pipeline. The rest of the helpers are split across:
//   - retrieval_embedding.go  — embedding acquisition + cache
//   - retrieval_rewrite.go     — query rewriting (rule + LLM) + similarity
//   - retrieval_search.go      — vector/keyword search + merge/rerank
package services

import (
	"math"
	"strings"
	"time"
	"unicode"
)

// GenerateDebugInfo generates detailed debugging information for a search result.
// This helps answer "why this result is ranked first?" and supports observability.
// Args:
// result - search result to generate debug info for.
// plan - retrieval plan with weight configuration (optional, can be nil for default weights).
// Returns ResultDebugInfo with scoring breakdown and signals.
func (s *RetrievalService) GenerateDebugInfo(result *SearchResult, plan *RetrievalPlan) *ResultDebugInfo {
	sourceWeight := 1.0
	if plan != nil {
		switch result.Source {
		case "experience":
			sourceWeight = plan.ExperienceWeight
		case "tool":
			sourceWeight = plan.ToolsWeight
		case "knowledge":
			sourceWeight = plan.KnowledgeWeight
		case "task_result":
			sourceWeight = plan.TaskResultsWeight
		}
	} else {
		// Use default weights when plan is not provided
		switch result.Source {
		case "experience":
			sourceWeight = 1.2
		case "tool":
			sourceWeight = 1.1
		case "knowledge":
			sourceWeight = 1.0
		default:
			sourceWeight = 1.0
		}
	}

	info := &ResultDebugInfo{
		ID:           result.ID,
		Score:        result.Score,
		Query:        result.Query,
		QueryWeight:  result.QueryWeight,
		Source:       result.Source,
		SubSource:    result.SubSource,
		SourceWeight: sourceWeight,
		SubWeight:    s.subSourceWeight(result.SubSource),
		Signals:      make(map[string]interface{}),
		Breakdown:    make(map[string]float64),
	}

	// Collect source-specific signals
	if result.Source == "experience" {
		if success, ok := result.Metadata["success"].(bool); ok {
			info.Signals["success"] = success
		}
		if reuseCount, ok := result.Metadata["reuse_count"].(int); ok {
			info.Signals["reuse_count"] = reuseCount
		}
		if executionTime, ok := result.Metadata["execution_time"].(float64); ok {
			info.Signals["execution_time"] = executionTime
		}
		if lessons, ok := result.Metadata["lessons"].(string); ok {
			info.Signals["lessons"] = lessons
		}
	}

	if result.Source == "tool" {
		if requiresAuth, ok := result.Metadata["requires_auth"].(bool); ok {
			info.Signals["requires_auth"] = requiresAuth
		}
		if successRate, ok := result.Metadata["success_rate"].(float64); ok {
			info.Signals["success_rate"] = successRate
		}
	}

	// Score breakdown for analysis
	info.Breakdown["query"] = result.QueryWeight
	info.Breakdown["source"] = info.SourceWeight
	info.Breakdown["sub_source"] = info.SubWeight

	return info
}

// calculateTimeDecay calculates time-based decay factor for scoring.
// Newer content gets higher scores to prevent old data from dominating.
func (s *RetrievalService) calculateTimeDecay(createdAt time.Time) float64 {
	ageHours := time.Since(createdAt).Hours()
	lambda := 0.01 // Decay coefficient (configurable)

	// Exponential decay: older content has lower weight
	decay := math.Exp(-lambda * ageHours)

	// Ensure minimum decay factor to avoid completely ignoring old data
	if decay < 0.1 {
		decay = 0.1
	}

	return decay
}

// ---- String manipulation helpers ----

func toLower(s string) string {
	return strings.ToLower(s)
}

// contains reports whether substr is within s, using case-insensitive matching.
// Uses strings.ToLower for Unicode-safe comparison.
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// normalizeEnglishQuery normalizes English queries by expanding contractions and standardizing format.
// This improves query matching by converting common contractions to their full forms.
// Args:
// query - original query text.
// Returns normalized query text.
func normalizeEnglishQuery(query string) string {
	// Define common English contractions and their expansions
	contractions := map[string]string{
		"i'm":       "i am",
		"you're":    "you are",
		"he's":      "he is",
		"she's":     "she is",
		"it's":      "it is",
		"we're":     "we are",
		"they're":   "they are",
		"don't":     "do not",
		"doesn't":   "does not",
		"didn't":    "did not",
		"won't":     "will not",
		"wouldn't":  "would not",
		"shouldn't": "should not",
		"can't":     "cannot",
		"couldn't":  "could not",
		"mightn't":  "might not",
		"mustn't":   "must not",
		"let's":     "let us",
		"that's":    "that is",
		"what's":    "what is",
		"where's":   "where is",
		"who's":     "who is",
		"how's":     "how is",
	}

	// Normalize to lowercase for matching
	queryLower := toLower(query)

	// Replace contractions with their full forms
	for contraction, expansion := range contractions {
		queryLower = replaceAllIgnoreCase(queryLower, contraction, expansion)
	}

	// Trim extra spaces
	queryLower = trimSpaces(queryLower)

	return queryLower
}

// replaceAllIgnoreCase replaces all occurrences of a substring case-insensitively.
// Operates on runes to avoid corrupting multi-byte UTF-8 characters (e.g. CJK).
func replaceAllIgnoreCase(s, old, new string) string {
	if old == "" {
		return s
	}
	sRunes := []rune(s)
	oldRunes := []rune(old)
	oldLower := make([]rune, len(oldRunes))
	for i, r := range oldRunes {
		oldLower[i] = toLowerRune(r)
	}

	var result []rune
	i := 0
	for i < len(sRunes) {
		if i+len(oldLower) <= len(sRunes) && matchRunesFold(sRunes[i:i+len(oldLower)], oldLower) {
			result = append(result, []rune(new)...)
			i += len(oldLower)
		} else {
			result = append(result, sRunes[i])
			i++
		}
	}
	return string(result)
}

// toLowerRune lowercases a single rune (ASCII fast-path + unicode fallback).
func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return unicode.ToLower(r)
}

// matchRunesFold compares two rune slices case-insensitively.
func matchRunesFold(a, b []rune) bool {
	for i := range a {
		if toLowerRune(a[i]) != b[i] {
			return false
		}
	}
	return true
}

// trimSpaces removes extra spaces from a string, keeping only single spaces.
// Args:
// s - string to trim.
// Returns string with normalized spacing.
func trimSpaces(s string) string {
	// Trim leading and trailing spaces
	s = strings.TrimSpace(s)

	// Replace multiple spaces with single space
	var result strings.Builder
	prevSpace := false

	for _, ch := range s {
		if ch == ' ' {
			if !prevSpace {
				result.WriteRune(ch)
				prevSpace = true
			}
		} else {
			result.WriteRune(ch)
			prevSpace = false
		}
	}

	return result.String()
}
