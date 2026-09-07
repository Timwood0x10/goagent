// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"context"
	stderrors "errors"
	"testing"
)

func TestConflictResolver_ResolveConflict(t *testing.T) {
	repo := NewMockExperienceRepository([]Experience{})
	resolver := NewConflictResolver(repo)

	tests := []struct {
		name           string
		newMemory      *Experience
		existingMemory *Experience
		expected       ResolutionStrategy
	}{
		{
			name:           "nil old memory",
			newMemory:      &Experience{Problem: "test", Solution: "fix", Confidence: 0.5},
			existingMemory: nil,
			expected:       ReplaceOld,
		},
		{
			name:           "new memory higher confidence",
			newMemory:      &Experience{Problem: "test", Solution: "fix", Confidence: 0.9},
			existingMemory: &Experience{Problem: "test", Solution: "old fix", Confidence: 0.5},
			expected:       ReplaceOld,
		},
		{
			// A lower-confidence duplicate must not overwrite a better fact.
			name:           "old memory higher confidence",
			newMemory:      &Experience{Problem: "test", Solution: "fix", Confidence: 0.5},
			existingMemory: &Experience{Problem: "test", Solution: "old fix", Confidence: 0.9},
			expected:       KeepOld,
		},
		{
			// Tie is broken in favour of the more recent observation.
			name:           "equal confidence",
			newMemory:      &Experience{Problem: "test", Solution: "fix", Confidence: 0.7},
			existingMemory: &Experience{Problem: "test", Solution: "old fix", Confidence: 0.7},
			expected:       ReplaceOld,
		},
		{
			name:           "nil new memory keeps existing",
			newMemory:      nil,
			existingMemory: &Experience{Problem: "test", Solution: "old fix", Confidence: 0.9},
			expected:       KeepOld,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolver.ResolveConflict(tt.newMemory, tt.existingMemory)
			if result != tt.expected {
				t.Errorf("ResolveConflict() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestConflictResolver_cosineSimilarity(t *testing.T) {
	repo := NewMockExperienceRepository([]Experience{})
	resolver := NewConflictResolver(repo)

	tests := []struct {
		name     string
		v1       []float64
		v2       []float64
		expected float64
	}{
		{
			name:     "identical vectors",
			v1:       []float64{1.0, 0.0, 0.0},
			v2:       []float64{1.0, 0.0, 0.0},
			expected: 1.0,
		},
		{
			name:     "orthogonal vectors",
			v1:       []float64{1.0, 0.0},
			v2:       []float64{0.0, 1.0},
			expected: 0.0,
		},
		{
			name:     "different dimensions",
			v1:       []float64{1.0, 0.0},
			v2:       []float64{1.0, 0.0, 0.0},
			expected: 0.0,
		},
		{
			name:     "empty vectors",
			v1:       []float64{},
			v2:       []float64{},
			expected: 0.0,
		},
		{
			name:     "normalized vectors",
			v1:       []float64{0.707, 0.707},
			v2:       []float64{0.707, 0.707},
			expected: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolver.cosineSimilarity(tt.v1, tt.v2)
			if result < tt.expected-0.01 || result > tt.expected+0.01 {
				t.Errorf("cosineSimilarity() = %v, want ~%v", result, tt.expected)
			}
		})
	}
}

func TestConflictResolver_DetectConflict(t *testing.T) {
	existingExperiences := []Experience{
		{
			Problem:  "docker container won't start",
			Solution: "restart docker daemon",
			Vector:   []float64{0.9, 0.1, 0.1, 0.1},
		},
		{
			Problem:  "I prefer Go",
			Solution: "Use Go examples",
			Vector:   []float64{0.1, 0.9, 0.1, 0.1},
		},
	}

	repo := NewMockExperienceRepository(existingExperiences)
	resolver := NewConflictResolver(repo)

	tests := []struct {
		name           string
		vector         []float64
		tenantID       string
		expectConflict bool
	}{
		{
			name:           "empty vector returns no conflict",
			vector:         []float64{},
			tenantID:       "test",
			expectConflict: false,
		},
		{
			name:           "high similarity vector finds conflict",
			vector:         []float64{0.95, 0.05, 0.05, 0.05},
			tenantID:       "test",
			expectConflict: true,
		},
		{
			name:           "low similarity vector no conflict",
			vector:         []float64{0.1, 0.1, 0.9, 0.1},
			tenantID:       "test",
			expectConflict: false,
		},
		{
			name:           "nil vector returns no conflict",
			vector:         nil,
			tenantID:       "test",
			expectConflict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			conflict, err := resolver.DetectConflict(ctx, tt.vector, tt.tenantID)

			if tt.expectConflict && err != nil {
				t.Errorf("DetectConflict() returned error: %v", err)
			}

			if tt.expectConflict && conflict == nil {
				t.Error("Expected conflict but got none")
			}

			if !tt.expectConflict && err != nil && !stderrors.Is(err, ErrNoConflict) {
				t.Errorf("Expected no conflict but got unexpected error: %v", err)
			}

			if !tt.expectConflict && conflict != nil {
				t.Error("Expected no conflict but got one")
			}
		})
	}
}

func TestConflictResolver_DetectConflictByExperience(t *testing.T) {
	existingExperiences := []Experience{
		{
			Problem:  "docker container won't start",
			Solution: "restart docker daemon",
			Vector:   []float64{0.9, 0.1, 0.1, 0.1},
		},
	}

	repo := NewMockExperienceRepository(existingExperiences)
	resolver := NewConflictResolver(repo)

	tests := []struct {
		name           string
		experience     *Experience
		tenantID       string
		expectConflict bool
	}{
		{
			name:           "nil experience returns no conflict",
			experience:     nil,
			tenantID:       "test",
			expectConflict: false,
		},
		{
			name: "experience without vector returns no conflict",
			experience: &Experience{
				Problem:  "test problem",
				Solution: "test solution",
				Vector:   nil,
			},
			tenantID:       "test",
			expectConflict: false,
		},
		{
			name: "high similarity experience finds conflict",
			experience: &Experience{
				Problem:  "docker error",
				Solution: "fix it",
				Vector:   []float64{0.95, 0.05, 0.05, 0.05},
			},
			tenantID:       "test",
			expectConflict: true,
		},
		{
			name: "low similarity experience no conflict",
			experience: &Experience{
				Problem:  "unrelated issue",
				Solution: "different solution",
				Vector:   []float64{0.1, 0.1, 0.9, 0.1},
			},
			tenantID:       "test",
			expectConflict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			conflict, err := resolver.DetectConflictByExperience(ctx, tt.experience, tt.tenantID)

			if tt.expectConflict && err != nil {
				t.Errorf("DetectConflictByExperience() returned error: %v", err)
			}

			if tt.expectConflict && conflict == nil {
				t.Error("Expected conflict but got none")
			}

			if !tt.expectConflict && err != nil && !stderrors.Is(err, ErrNoConflict) {
				t.Errorf("Expected no conflict but got unexpected error: %v", err)
			}

			if !tt.expectConflict && conflict != nil {
				t.Error("Expected no conflict but got one")
			}
		})
	}
}
