// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"testing"
)

func TestMemoryClassifier_ClassifyMemory(t *testing.T) {
	classifier := NewMemoryClassifier()

	tests := []struct {
		name         string
		problem      string
		solution     string
		expectedType MemoryType
	}{
		{
			name:         "solution type",
			problem:      "I have an error in my code",
			solution:     "Fix the syntax error on line 10",
			expectedType: MemoryInteraction,
		},
		{
			name:         "preference type",
			problem:      "I prefer Go over Python",
			solution:     "Use Go for performance",
			expectedType: MemoryPreference,
		},
		{
			name:         "fact type",
			problem:      "What platform am I using?",
			solution:     "You are using macOS",
			expectedType: MemoryKnowledge,
		},
		{
			name:         "rule type",
			problem:      "What are the coding standards?",
			solution:     "Follow the Google Go style guide",
			expectedType: MemoryKnowledge,
		},
		{
			name:         "default to fact",
			problem:      "Tell me something",
			solution:     "This is a generic response",
			expectedType: MemoryKnowledge,
		},
		{
			name:         "debug keyword",
			problem:      "Help me debug this",
			solution:     "Check the logs for errors",
			expectedType: MemoryInteraction,
		},
		{
			name:         "troubleshoot keyword",
			problem:      "I need to troubleshoot",
			solution:     "Restart the service",
			expectedType: MemoryInteraction,
		},
		{
			name:         "like keyword",
			problem:      "I like verbose responses",
			solution:     "Will provide detailed answers",
			expectedType: MemoryPreference,
		},
		{
			name:         "usually keyword",
			problem:      "I usually prefer Go",
			solution:     "Noted",
			expectedType: MemoryPreference,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := &Experience{
				Problem:  tt.problem,
				Solution: tt.solution,
			}

			result := classifier.ClassifyMemory(exp)
			if result != tt.expectedType {
				t.Errorf("ClassifyMemory() = %v, want %v", result, tt.expectedType)
			}
		})
	}
}

func TestGetMemoryTypeFromString(t *testing.T) {
	tests := []struct {
		name     string
		typeStr  string
		expected MemoryType
	}{
		{
			name:     "fact",
			typeStr:  "fact",
			expected: MemoryKnowledge,
		},
		{
			name:     "preference",
			typeStr:  "preference",
			expected: MemoryPreference,
		},
		{
			name:     "solution",
			typeStr:  "solution",
			expected: MemoryInteraction,
		},
		{
			name:     "rule",
			typeStr:  "rule",
			expected: MemoryKnowledge,
		},
		{
			name:     "profile",
			typeStr:  "profile",
			expected: MemoryProfile,
		},
		{
			name:     "case insensitive",
			typeStr:  "FACT",
			expected: MemoryKnowledge,
		},
		{
			name:     "invalid defaults to fact",
			typeStr:  "invalid",
			expected: MemoryKnowledge,
		},
		{
			name:     "empty defaults to fact",
			typeStr:  "",
			expected: MemoryKnowledge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetMemoryTypeFromString(tt.typeStr)
			if result != tt.expected {
				t.Errorf("GetMemoryTypeFromString(%q) = %v, want %v", tt.typeStr, result, tt.expected)
			}
		})
	}
}

func TestMemoryType_String(t *testing.T) {
	tests := []struct {
		name     string
		memType  MemoryType
		expected string
	}{
		{
			name:     "fact",
			memType:  MemoryKnowledge,
			expected: "fact",
		},
		{
			name:     "preference",
			memType:  MemoryPreference,
			expected: "preference",
		},
		{
			name:     "solution",
			memType:  MemoryInteraction,
			expected: "solution",
		},
		{
			name:     "profile",
			memType:  MemoryProfile,
			expected: "profile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.memType.String()
			if result != tt.expected {
				t.Errorf("MemoryType.String() = %q, want %q", result, tt.expected)
			}
		})
	}
}
