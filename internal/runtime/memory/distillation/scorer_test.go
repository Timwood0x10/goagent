// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"testing"
)

func TestImportanceScorer_ScoreMemory(t *testing.T) {
	scorer := NewImportanceScorer()

	tests := []struct {
		name       string
		memoryType MemoryType
		problem    string
		solution   string
		minScore   float64
		maxScore   float64
	}{
		{
			name:       "error keyword",
			memoryType: MemoryInteraction,
			problem:    "I have an error",
			solution:   "Fix the syntax",
			minScore:   0.7,
			maxScore:   1.0,
		},
		{
			name:       "solution keyword",
			memoryType: MemoryInteraction,
			problem:    "I need a solution",
			solution:   "Use this approach",
			minScore:   0.7,
			maxScore:   1.0,
		},
		{
			name:       "preference keyword",
			memoryType: MemoryPreference,
			problem:    "I prefer Go",
			solution:   "Use Go",
			minScore:   0.6,
			maxScore:   0.8,
		},
		{
			name:       "short solution",
			memoryType: MemoryInteraction,
			problem:    "error",
			solution:   "fix",
			minScore:   0.3,
			maxScore:   0.5,
		},
		{
			name:       "long solution with bonus",
			memoryType: MemoryInteraction,
			problem:    "I have a complex error that needs detailed analysis",
			solution:   "First check the logs, then identify the root cause, and finally implement a fix that addresses both the symptoms and the underlying issue",
			minScore:   0.7,
			maxScore:   1.0,
		},
		{
			name:       "fact type",
			memoryType: MemoryKnowledge,
			problem:    "What is my OS?",
			solution:   "You are using macOS",
			minScore:   0.5,
			maxScore:   0.7,
		},
		// Chinese keyword tests
		{
			name:       "chinese problem keyword",
			memoryType: MemoryInteraction,
			problem:    "我有一个错误",
			solution:   "修复方法如下",
			minScore:   0.7,
			maxScore:   1.0,
		},
		{
			name:       "chinese framework keyword",
			memoryType: MemoryKnowledge,
			problem:    "介绍一下go框架",
			solution:   "GoFrame是一个企业级Go框架",
			minScore:   0.6,
			maxScore:   0.9,
		},
		{
			name:       "chinese config keyword",
			memoryType: MemoryPreference,
			problem:    "怎么配置数据库",
			solution:   "在config.yaml中设置",
			minScore:   0.6,
			maxScore:   0.9,
		},
		{
			name:       "chinese no keyword",
			memoryType: MemoryKnowledge,
			problem:    "你好",
			solution:   "今天天气不错",
			minScore:   0.2,
			maxScore:   0.3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scorer.ScoreMemory(tt.memoryType, tt.problem, tt.solution)

			if score < tt.minScore {
				t.Errorf("ScoreMemory() = %v, want >= %v", score, tt.minScore)
			}
			if score > tt.maxScore {
				t.Errorf("ScoreMemory() = %v, want <= %v", score, tt.maxScore)
			}
			if score < 0 || score > 1 {
				t.Errorf("ScoreMemory() = %v, want in range [0,1]", score)
			}
		})
	}
}

func TestImportanceScorer_ShouldKeep(t *testing.T) {
	scorer := NewImportanceScorer()

	tests := []struct {
		name     string
		score    float64
		expected bool
	}{
		{
			name:     "above threshold",
			score:    0.7,
			expected: true,
		},
		{
			name:     "at threshold",
			score:    0.6,
			expected: true,
		},
		{
			name:     "below threshold",
			score:    0.5,
			expected: false,
		},
		{
			name:     "zero",
			score:    0.0,
			expected: false,
		},
		{
			name:     "one",
			score:    1.0,
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scorer.ShouldKeep(tt.score)
			if result != tt.expected {
				t.Errorf("ShouldKeep(%v) = %v, want %v", tt.score, result, tt.expected)
			}
		})
	}
}

func TestImportanceScorer_TopNFilter(t *testing.T) {
	scorer := NewImportanceScorer()

	experiences := []Experience{
		{Problem: "error1", Solution: "fix1", Confidence: 0.5},
		{Problem: "error2", Solution: "fix2", Confidence: 0.8},
		{Problem: "error3", Solution: "fix3", Confidence: 0.9},
		{Problem: "error4", Solution: "fix4", Confidence: 0.4},
		{Problem: "error5", Solution: "fix5", Confidence: 0.7},
	}

	tests := []struct {
		name         string
		maxCount     int
		expectedSize int
	}{
		{
			name:         "filter to top 3",
			maxCount:     3,
			expectedSize: 3,
		},
		{
			name:         "filter to top 2",
			maxCount:     2,
			expectedSize: 2,
		},
		{
			name:         "all below threshold",
			maxCount:     10,
			expectedSize: 3, // only 3 above 0.6
		},
		{
			name:         "more than available",
			maxCount:     100,
			expectedSize: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scorer.TopNFilter(experiences, tt.maxCount)

			if len(result) != tt.expectedSize {
				t.Errorf("TopNFilter() returned %d experiences, want %d", len(result), tt.expectedSize)
			}

			// Verify descending order
			for i := 1; i < len(result); i++ {
				if result[i].Confidence > result[i-1].Confidence {
					t.Errorf("TopNFilter() not sorted: experience %d has confidence %v > experience %d with %v",
						i, result[i].Confidence, i-1, result[i-1].Confidence)
				}
			}
		})
	}
}

func TestImportanceScorer_SortByImportance(t *testing.T) {
	scorer := NewImportanceScorer()

	experiences := []Experience{
		{Problem: "error1", Solution: "fix1", Confidence: 0.5},
		{Problem: "error2", Solution: "fix2", Confidence: 0.8},
		{Problem: "error3", Solution: "fix3", Confidence: 0.9},
	}

	scorer.SortByImportance(experiences)

	// Verify descending order
	for i := 1; i < len(experiences); i++ {
		if experiences[i].Confidence > experiences[i-1].Confidence {
			t.Errorf("SortByImportance() not sorted: experience %d has confidence %v > experience %d with %v",
				i, experiences[i].Confidence, i-1, experiences[i-1].Confidence)
		}
	}
}
