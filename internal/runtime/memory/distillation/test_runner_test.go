// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"fmt"
	"strings"
)

// TestRunner executes and reports on the distillation test set.
type TestRunner struct {
	extractor  *ExperienceExtractor
	classifier *MemoryClassifier
	scorer     *ImportanceScorer
}

// NewTestRunner creates a new test runner.
func NewTestRunner() *TestRunner {
	return &TestRunner{
		extractor:  NewExperienceExtractor(),
		classifier: NewMemoryClassifier(),
		scorer:     NewImportanceScorer(),
	}
}

// RunAllTests executes all tests in the test set and returns a summary.
func (tr *TestRunner) RunAllTests() *TestSummary {
	results := RunTestSet(tr.extractor, tr.classifier, tr.scorer)

	summary := &TestSummary{
		Total:   len(results),
		Passed:  0,
		Failed:  0,
		Results: results,
	}

	for _, result := range results {
		if result.Match {
			summary.Passed++
		} else {
			summary.Failed++
		}
	}

	return summary
}

// TestSummary provides a summary of test execution results.
type TestSummary struct {
	Total   int
	Passed  int
	Failed  int
	Results []TestResult
}

// PrintSummary prints the test summary to stdout.
func (s *TestSummary) PrintSummary() {
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("MEMORY DISTILLATION TEST RESULTS")
	fmt.Println(strings.Repeat("=", 80))

	fmt.Printf("\nTotal Tests: %d\n", s.Total)
	fmt.Printf("Passed: %d (%.1f%%)\n", s.Passed, float64(s.Passed)*100/float64(s.Total))
	fmt.Printf("Failed: %d (%.1f%%)\n", s.Failed, float64(s.Failed)*100/float64(s.Total))

	fmt.Println("\n" + strings.Repeat("-", 80))
	fmt.Println("DETAILED RESULTS")
	fmt.Println(strings.Repeat("-", 80))

	for _, result := range s.Results {
		status := "✅ PASS"
		if !result.Match {
			status = "❌ FAIL"
		}

		fmt.Printf("\n%s - %s\n", status, result.Name)
		fmt.Printf("  Expected: %v (should extract: %v)\n", result.Expected, result.ShouldPass)
		fmt.Printf("  Actual:   %v (actually extracted: %v)\n", result.Actual, result.ActualPass)

		if !result.Match {
			fmt.Printf("  ⚠️  MISMATCH: Expected %v but got %v\n", result.ShouldPass, result.ActualPass)
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 80))

	if s.Failed == 0 {
		fmt.Println("🎉 ALL TESTS PASSED!")
	} else {
		fmt.Printf("⚠️  %d TEST(S) FAILED\n", s.Failed)
	}

	fmt.Println(strings.Repeat("=", 80))
}

// GetPassRate returns the pass rate as a percentage.
func (s *TestSummary) GetPassRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) * 100 / float64(s.Total)
}
