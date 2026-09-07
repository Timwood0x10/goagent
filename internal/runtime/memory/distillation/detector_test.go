// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"testing"
)

func TestIsProblem(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "error keyword",
			text:     "I have an error in my code",
			expected: true,
		},
		{
			name:     "issue keyword",
			text:     "There is an issue with the database",
			expected: true,
		},
		{
			name:     "how question",
			text:     "How do I fix this problem?",
			expected: true,
		},
		{
			name:     "why question",
			text:     "Why is this happening?",
			expected: true,
		},
		{
			name:     "question mark",
			text:     "Can you help me?",
			expected: true,
		},
		{
			name:     "fix keyword",
			text:     "I need to fix the connection",
			expected: true,
		},
		{
			name:     "help keyword",
			text:     "Please help me",
			expected: true,
		},
		{
			name:     "casual acknowledgment",
			text:     "ok",
			expected: false,
		},
		{
			name:     "thanks",
			text:     "thanks for the help",
			expected: false,
		},
		{
			name:     "got it",
			text:     "got it, thanks",
			expected: false,
		},
		{
			name:     "empty string",
			text:     "",
			expected: false,
		},
		{
			name:     "statement without problem",
			text:     "I'm working on a new project",
			expected: false,
		},
		{
			name:     "case insensitive",
			text:     "ERROR in my code",
			expected: true,
		},
		// Chinese question detection tests
		{
			name:     "chinese introduce question",
			text:     "介绍一下go-agent",
			expected: true,
		},
		{
			name:     "chinese what-is question",
			text:     "go-agent 是什么",
			expected: true,
		},
		{
			name:     "chinese how-to question",
			text:     "怎么配置数据库",
			expected: true,
		},
		{
			name:     "chinese features question",
			text:     "ARES有哪些功能",
			expected: true,
		},
		{
			name:     "chinese difference question",
			text:     "A和B有什么区别",
			expected: true,
		},
		{
			name:     "chinese tell-me question",
			text:     "说说你的特点",
			expected: true,
		},
		{
			name:     "chinese recommendation",
			text:     "推荐一个Go框架",
			expected: true,
		},
		{
			name:     "chinese casual ack",
			text:     "好的",
			expected: false,
		},
		{
			name:     "chinese thanks",
			text:     "谢谢",
			expected: false,
		},
		{
			name:     "chinese got-it",
			text:     "明白了",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsProblem(tt.text)
			if result != tt.expected {
				t.Errorf("IsProblem(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}

func TestQuestionDetector_Detect(t *testing.T) {
	detector := NewQuestionDetector()

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{
			name:     "problem",
			text:     "I have a problem with my code",
			expected: true,
		},
		{
			name:     "question",
			text:     "What is the best way to do this?",
			expected: true,
		},
		{
			name:     "non-problem",
			text:     "ok thanks",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detector.Detect(tt.text)
			if result != tt.expected {
				t.Errorf("Detect(%q) = %v, want %v", tt.text, result, tt.expected)
			}
		})
	}
}
