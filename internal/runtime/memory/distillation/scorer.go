// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"sort"
	"strings"
)

// ImportanceScorer calculates importance scores for memories.
type ImportanceScorer struct {
	minImportance     float64
	enableLengthBonus bool
	lengthThreshold   int
	lengthBonus       float64
}

// NewImportanceScorer creates a new ImportanceScorer instance with default settings.
func NewImportanceScorer() *ImportanceScorer {
	return &ImportanceScorer{
		minImportance:     0.6,
		enableLengthBonus: true,
		lengthThreshold:   60,
		lengthBonus:       0.1,
	}
}

// NewImportanceScorerWithConfig creates a new ImportanceScorer instance with custom configuration.
func NewImportanceScorerWithConfig(minImportance float64, enableLengthBonus bool) *ImportanceScorer {
	return &ImportanceScorer{
		minImportance:     minImportance,
		enableLengthBonus: enableLengthBonus,
		lengthThreshold:   60,
		lengthBonus:       0.1,
	}
}

// ScoreMemory calculates the importance score for a memory based on its content and type.
// The score ranges from 0 to 1, with higher scores indicating more important memories.
//
// Args:
//
//	memoryType - the type of memory.
//	problem - the problem description.
//	solution - the solution description.
//
// Returns:
//
//	float64 - the importance score between 0 and 1.
func (s *ImportanceScorer) ScoreMemory(memoryType MemoryType, problem, solution string) float64 {
	score := 0.4 // Start with moderate base score

	content := strings.ToLower(problem + " " + solution)
	totalLength := len(problem) + len(solution)

	// Keyword-based scoring - increased bonuses for important keywords
	keywordScores := map[string]float64{
		"error":    0.3,
		"fix":      0.3,
		"solution": 0.3,
		"prefer":   0.2,
		"issue":    0.25,
		"problem":  0.25,
		"debug":    0.2,
		"won't":    0.2,
		"can't":    0.2,
		"cannot":   0.2,
		"broken":   0.2,
		"fail":     0.2,
		// Chinese keywords
		"错误": 0.3,
		"修复": 0.3,
		"解决": 0.3,
		"问题": 0.25,
		"故障": 0.25,
		"配置": 0.2,
		"规范": 0.2,
		"编码": 0.2,
		"框架": 0.2,
		"架构": 0.2,
		"方案": 0.2,
		"方法": 0.2,
		"实现": 0.2,
		"支持": 0.15,
		"特点": 0.2,
		"功能": 0.2,
		"区别": 0.2,
		"推荐": 0.2,
		"最佳": 0.2,
		"优化": 0.25,
		"性能": 0.2,
		"安全": 0.2,
		"部署": 0.2,
		"测试": 0.2,
		"调试": 0.2,
	}

	hasKeywords := false
	for keyword, bonus := range keywordScores {
		if strings.Contains(content, keyword) {
			score += bonus
			hasKeywords = true
		}
	}

	// Type-based adjustment
	switch memoryType {
	case MemoryInteraction:
		score += 0.15 // Solutions are inherently more valuable
	case MemoryPreference:
		score += 0.2 // Preferences are valuable for personalization
	case MemoryKnowledge:
		score += 0.25 // Facts are useful for context (increased for short content)
	case MemoryProfile:
		score += 0.15 // Rules are important for consistency
	}

	// Length bonus (more complete experiences are more valuable)
	if s.enableLengthBonus && totalLength > s.lengthThreshold {
		score += s.lengthBonus
	}

	// Length penalty for very short content without strong keywords
	switch {
	case totalLength < 15:
		// Extremely short content - severe penalty
		if !hasKeywords {
			score = 0.2 // Very low score for very short content without keywords
		} else {
			// Even with keywords, extremely short content is not useful
			score *= 0.4
		}
	case totalLength < 30:
		if !hasKeywords {
			score = 0.25 // Very low score for very short content without keywords
		} else {
			// Apply moderate penalty for short content even with keywords
			// But don't penalize preference and fact types as heavily
			if memoryType == MemoryPreference || memoryType == MemoryKnowledge {
				score *= 0.9
			} else {
				score *= 0.7
			}
		}
	case totalLength < 60:
		// Mild penalty for moderately short content
		score *= 0.9
	}

	// Cap the score at 1.0
	if score > 1.0 {
		score = 1.0
	}

	// Ensure minimum score
	if score < 0.0 {
		score = 0.0
	}

	return score
}

// ShouldKeep determines if a memory should be kept based on its importance score.
//
// Args:
//
//	score - the importance score.
//
// Returns:
//
//	true if the memory should be kept, false otherwise.
func (s *ImportanceScorer) ShouldKeep(score float64) bool {
	return score >= s.minImportance
}

// TopNFilter filters memories by importance and returns the top N most important ones.
// This is performed before conflict detection for performance optimization.
//
// Args:
//
//	experiences - the experiences to filter.
//	maxCount - the maximum number of experiences to return.
//
// Returns:
//
//	[]Experience - the top N experiences sorted by importance.
func (s *ImportanceScorer) TopNFilter(experiences []Experience, maxCount int) []Experience {
	if len(experiences) == 0 {
		return experiences
	}

	// Filter by minimum importance
	var filtered []Experience
	for _, exp := range experiences {
		if exp.Confidence >= s.minImportance {
			filtered = append(filtered, exp)
		}
	}

	if len(filtered) == 0 {
		return filtered
	}

	// Sort by confidence (importance) in descending order
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Confidence > filtered[j].Confidence
	})

	// Return top N
	if len(filtered) > maxCount {
		return filtered[:maxCount]
	}

	return filtered
}

// SortByImportance sorts memories by importance in descending order.
//
// Args:
//
//	memories - the memories to sort.
func (s *ImportanceScorer) SortByImportance(memories []Experience) {
	sort.Slice(memories, func(i, j int) bool {
		return memories[i].Confidence > memories[j].Confidence
	})
}

// GetMinImportance returns the minimum importance threshold.
//
// Returns:
//
//	float64 - the minimum importance score.
func (s *ImportanceScorer) GetMinImportance() float64 {
	return s.minImportance
}
