// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"context"
	"fmt"
	"testing"
)

// TestDistillationSuite runs the complete distillation test suite.
func TestDistillationSuite(t *testing.T) {
	runner := NewTestRunner()
	summary := runner.RunAllTests()

	// Print detailed summary
	summary.PrintSummary()

	// Check pass rate
	passRate := summary.GetPassRate()
	minPassRate := 80.0 // Require at least 80% pass rate

	if passRate < minPassRate {
		t.Errorf("Pass rate %.1f%% is below minimum %.1f%%", passRate, minPassRate)
	}

	// Report detailed failures
	for _, result := range summary.Results {
		if !result.Match {
			t.Errorf("Test '%s' failed: expected %v, got %v", result.Name, result.ShouldPass, result.ActualPass)
		}
	}
}

// ExampleTestSet demonstrates how to use the test set.
func ExampleTestSet() {
	testSet := GetTestSet()

	fmt.Printf("Total test cases: %d\n", len(testSet))
	fmt.Printf("Should extract: %d\n", countShouldExtract(testSet))
	fmt.Printf("Should not extract: %d\n", countShouldNotExtract(testSet))
}

func countShouldExtract(tests []TestSet) int {
	count := 0
	for _, test := range tests {
		if test.ShouldExtract {
			count++
		}
	}
	return count
}

func countShouldNotExtract(tests []TestSet) int {
	count := 0
	for _, test := range tests {
		if !test.ShouldExtract {
			count++
		}
	}
	return count
}

// BenchmarkDistillation benchmarks the distillation performance.
func BenchmarkDistillation(b *testing.B) {
	config := DefaultDistillationConfig()
	embedder := NewMockEmbeddingService()
	repo := NewMockExperienceRepository([]Experience{})

	distiller := NewDistiller(config, embedder, repo)

	messages := []Message{
		{Role: "user", Content: "I have an error in my code"},
		{Role: "assistant", Content: "Fix the syntax error on line 10"},
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = distiller.DistillConversation(ctx, "bench-conv", messages, "default", "user1")
	}
}
