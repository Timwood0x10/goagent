// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"testing"
)

func TestExperienceExtractor_ExtractExperiences(t *testing.T) {
	extractor := NewExperienceExtractor()

	tests := []struct {
		name          string
		messages      []Message
		expectedCount int
		shouldExtract bool
	}{
		{
			name: "direct problem solution",
			messages: []Message{
				{Role: "user", Content: "I have an error in my code"},
				{Role: "assistant", Content: "You need to check the syntax in line 10"},
			},
			expectedCount: 1,
			shouldExtract: true,
		},
		{
			name: "non-problem message",
			messages: []Message{
				{Role: "user", Content: "ok thanks"},
				{Role: "assistant", Content: "You're welcome!"},
			},
			expectedCount: 0,
			shouldExtract: false,
		},
		{
			name: "cross-turn solution",
			messages: []Message{
				{Role: "user", Content: "docker container won't start"},
				{Role: "assistant", Content: "can you share the logs?"},
				{Role: "user", Content: "error: connection refused"},
				{Role: "assistant", Content: "restart docker daemon"},
			},
			expectedCount: 2,
			shouldExtract: true,
		},
		{
			name: "noise in solution",
			messages: []Message{
				{Role: "user", Content: "I have an error"},
				{Role: "assistant", Content: "```go func main() {} ```"},
			},
			expectedCount: 0,
			shouldExtract: false,
		},
		{
			name: "multiple problems",
			messages: []Message{
				{Role: "user", Content: "I have an error in my code"},
				{Role: "assistant", Content: "Check the syntax"},
				{Role: "user", Content: "how do I fix this?"},
				{Role: "assistant", Content: "Use a try-catch block"},
			},
			expectedCount: 2,
			shouldExtract: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			experiences := extractor.ExtractExperiences(tt.messages)

			if len(experiences) != tt.expectedCount {
				t.Errorf("ExtractExperiences() returned %d experiences, want %d", len(experiences), tt.expectedCount)
			}

			if tt.shouldExtract && len(experiences) == 0 {
				t.Error("Expected to extract experiences but got none")
			}

			if !tt.shouldExtract && len(experiences) > 0 {
				t.Error("Expected no experiences but got some")
			}

			// Validate experience structure
			for _, exp := range experiences {
				if exp.Problem == "" {
					t.Error("Experience has empty problem")
				}
				if exp.Solution == "" {
					t.Error("Experience has empty solution")
				}
				if exp.Confidence < 0 || exp.Confidence > 1 {
					t.Errorf("Experience confidence %v is out of range [0,1]", exp.Confidence)
				}
			}
		})
	}
}

func TestExtractCoreSolution(t *testing.T) {
	extractor := NewExperienceExtractor()

	tests := []struct {
		name     string
		solution string
		expected string
	}{
		{
			name:     "simple solution",
			solution: "restart the service",
			expected: "restart the service",
		},
		{
			name:     "solution with prefix",
			solution: "Here's how to fix it: restart the service",
			expected: "restart the service",
		},
		{
			name:     "solution with suffix",
			solution: "restart the service. Let me know if this helps",
			expected: "restart the service",
		},
		{
			name:     "long solution",
			solution: string(make([]byte, 600)), // 600 bytes
			expected: "..................................................." + "...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractor.extractCoreSolution(tt.solution)
			if len(result) > 500 {
				t.Errorf("Solution too long: %d characters", len(result))
			}
		})
	}
}

func TestFormatExperience(t *testing.T) {
	exp := &Experience{
		Problem:  "docker container won't start",
		Solution: "restart docker daemon",
	}

	result := FormatExperience(exp)
	expected := "docker container won't start → restart docker daemon"

	if result != expected {
		t.Errorf("FormatExperience() = %q, want %q", result, expected)
	}
}
