// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"testing"
)

func TestNoiseFilter_IsNoise(t *testing.T) {
	filter := NewNoiseFilter()

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "empty string",
			text:     "",
			expected: true,
		},
		{
			name:     "too short",
			text:     "hi",
			expected: true,
		},
		{
			name:     "casual acknowledgment",
			text:     "ok",
			expected: true,
		},
		{
			name:     "thanks",
			text:     "thanks for your help",
			expected: true,
		},
		{
			name:     "got it",
			text:     "got it",
			expected: true,
		},
		{
			name:     "valid problem",
			text:     "I have an error in my code that needs to be fixed",
			expected: false,
		},
		{
			name:     "code block",
			text:     "```go func main() {} ```",
			expected: true,
		},
		{
			name:     "stacktrace",
			text:     "panic: runtime error: index out of range",
			expected: true,
		},
		{
			name:     "log message",
			text:     "[INFO] Starting application",
			expected: true,
		},
		{
			name:     "markdown table",
			text:     "| Name | Value |\n|------|-------|\n| key  | val   |",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.IsNoise(tt.text)
			if result != tt.expected {
				t.Errorf("IsNoise(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestSecurityFilter(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "normal text",
			text:     "I need help with my code",
			expected: true,
		},
		{
			name:     "contains password",
			text:     "my password is secret123",
			expected: false,
		},
		{
			name:     "contains api key",
			text:     "use this api key: abc123",
			expected: false,
		},
		{
			name:     "contains secret",
			text:     "this is a secret code",
			expected: false,
		},
		{
			name:     "contains token",
			text:     "your access token is xyz789",
			expected: false,
		},
		{
			name:     "empty string",
			text:     "",
			expected: false,
		},
		{
			name:     "case insensitive",
			text:     "PASSWORD: mypass",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SecurityFilter(tt.text)
			if result != tt.expected {
				t.Errorf("SecurityFilter(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestCodeBlockFilter(t *testing.T) {
	filter := NewNoiseFilter()

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "markdown code block",
			text:     "```go func main() {} ```",
			expected: true,
		},
		{
			name:     "go function",
			text:     "func main() { println(\"hello\") }",
			expected: true,
		},
		{
			name:     "package declaration",
			text:     "package main",
			expected: true,
		},
		{
			name:     "import statement",
			text:     "import \"fmt\"",
			expected: true,
		},
		{
			name:     "normal text",
			text:     "I need help with my code",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.CodeBlockFilter(tt.text)
			if result != tt.expected {
				t.Errorf("CodeBlockFilter(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestStacktraceFilter(t *testing.T) {
	filter := NewNoiseFilter()

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "panic",
			text:     "panic: runtime error",
			expected: true,
		},
		{
			name:     "exception",
			text:     "java.lang.Exception",
			expected: true,
		},
		{
			name:     "traceback",
			text:     "Traceback (most recent call last)",
			expected: true,
		},
		{
			name:     "go file line",
			text:     "main.go:123",
			expected: true,
		},
		{
			name:     "normal text",
			text:     "I need help with my code",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.StacktraceFilter(tt.text)
			if result != tt.expected {
				t.Errorf("StacktraceFilter(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestMarkdownTableFilter(t *testing.T) {
	filter := NewNoiseFilter()

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "markdown table",
			text:     "| Name | Value |\n|------|-------|",
			expected: true,
		},
		{
			name:     "pipe without separator",
			text:     "this | that",
			expected: false,
		},
		{
			name:     "separator without pipe",
			text:     "---",
			expected: false,
		},
		{
			name:     "normal text",
			text:     "I need help with my code",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filter.MarkdownTableFilter(tt.text)
			if result != tt.expected {
				t.Errorf("MarkdownTableFilter(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}
