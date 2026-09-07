package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ReportGenerator generates evaluation reports.
type ReportGenerator struct{}

// NewReportGenerator creates a new report generator.
func NewReportGenerator() *ReportGenerator {
	return &ReportGenerator{}
}

// GenerateMarkdown generates a markdown summary report.
func (g *ReportGenerator) GenerateMarkdown(suite TestSuite, results []TestResult, scores [][]EvalScore) (string, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "# Evaluation Report: %s\n\n", suite.Name)
	fmt.Fprintf(&sb, "Generated: %s\n\n", time.Now().Format(time.RFC3339))

	if suite.Description != "" {
		fmt.Fprintf(&sb, "## Description\n\n%s\n\n", suite.Description)
	}

	// Summary statistics
	sb.WriteString("## Summary\n\n")
	totalTests := len(results)
	passedTests := 0
	totalDuration := time.Duration(0)
	totalTokens := 0

	for _, result := range results {
		if result.Error == "" {
			passedTests++
		}
		totalDuration += result.Duration
		totalTokens += result.TokensUsed
	}

	fmt.Fprintf(&sb, "- Total Tests: %d\n", totalTests)
	fmt.Fprintf(&sb, "- Passed: %d\n", passedTests)
	fmt.Fprintf(&sb, "- Failed: %d\n", totalTests-passedTests)
	fmt.Fprintf(&sb, "- Total Duration: %v\n", totalDuration)
	fmt.Fprintf(&sb, "- Total Tokens: %d\n\n", totalTokens)

	// Per-test results
	sb.WriteString("## Test Results\n\n")
	sb.WriteString("| Test ID | Name | Duration | Tokens | Status |\n")
	sb.WriteString("|---------|------|----------|--------|--------|\n")

	// Build a lookup map from TestCaseID to TestCase name for safe access.
	tcNames := make(map[string]string, len(suite.TestCases))
	for _, tc := range suite.TestCases {
		tcNames[tc.ID] = tc.Name
	}

	for _, result := range results {
		status := "✓ Pass"
		if result.Error != "" {
			status = "✗ Fail"
		}
		name := tcNames[result.TestCaseID]
		if name == "" {
			name = "unknown"
		}
		fmt.Fprintf(&sb, "| %s | %s | %v | %d | %s |\n",
			result.TestCaseID, name, result.Duration, result.TokensUsed, status)
	}

	// Metric scores
	sb.WriteString("\n## Metric Scores\n\n")

	// Aggregate metrics
	metricScores := make(map[string][]float64)
	for _, testScores := range scores {
		for _, score := range testScores {
			metricScores[score.Metric] = append(metricScores[score.Metric], score.Score)
		}
	}

	sb.WriteString("| Metric | Average | Min | Max |\n")
	sb.WriteString("|--------|---------|-----|-----|\n")

	metrics := make([]string, 0, len(metricScores))
	for metric := range metricScores {
		metrics = append(metrics, metric)
	}
	sort.Strings(metrics)

	for _, metric := range metrics {
		values := metricScores[metric]
		if len(values) == 0 {
			// No samples for this metric: report a zeroed row instead of an
			// all-zero-run that keeps minVal at its 1.0 sentinel and divides by
			// zero for the average.
			fmt.Fprintf(&sb, "| %s | 0.00 | 0.00 | 0.00 |\n", metric)
			continue
		}
		avg := 0.0
		minVal := values[0]
		maxVal := values[0]
		for _, v := range values {
			avg += v
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}
		avg /= float64(len(values))

		fmt.Fprintf(&sb, "| %s | %.2f | %.2f | %.2f |\n", metric, avg, minVal, maxVal)
	}

	return sb.String(), nil
}

// GenerateJSON generates a JSON report for CI consumption.
func (g *ReportGenerator) GenerateJSON(suite TestSuite, results []TestResult, scores [][]EvalScore) (string, error) {
	report := struct {
		SuiteName   string        `json:"suite_name"`
		GeneratedAt string        `json:"generated_at"`
		Summary     interface{}   `json:"summary"`
		Results     []TestResult  `json:"results"`
		Scores      [][]EvalScore `json:"scores"`
	}{
		SuiteName:   suite.Name,
		GeneratedAt: time.Now().Format(time.RFC3339),
		Results:     results,
		Scores:      scores,
	}

	// Calculate summary
	passedTests := 0
	totalDuration := time.Duration(0)
	totalTokens := 0

	for _, result := range results {
		if result.Error == "" {
			passedTests++
		}
		totalDuration += result.Duration
		totalTokens += result.TokensUsed
	}

	report.Summary = struct {
		TotalTests  int    `json:"total_tests"`
		PassedTests int    `json:"passed_tests"`
		FailedTests int    `json:"failed_tests"`
		Duration    string `json:"duration"`
		TotalTokens int    `json:"total_tokens"`
	}{
		TotalTests:  len(results),
		PassedTests: passedTests,
		FailedTests: len(results) - passedTests,
		Duration:    totalDuration.String(),
		TotalTokens: totalTokens,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// SaveReport saves a report to a file.
func (g *ReportGenerator) SaveReport(path string, content string) error {
	return os.WriteFile(path, []byte(content), 0600)
}

// RunEvaluation runs a complete evaluation pipeline.
func RunEvaluation(ctx context.Context, loader *Loader, runner TestRunner, evaluator Evaluator, suitePath string) ([]TestResult, [][]EvalScore, error) {
	// Load test suite
	suite, err := loader.Load(suitePath)
	if err != nil {
		return nil, nil, fmt.Errorf("load test suite: %w", err)
	}

	// Run tests
	results, err := runner.RunSuite(ctx, *suite)
	if err != nil {
		return nil, nil, fmt.Errorf("run tests: %w", err)
	}

	// Evaluate results — use TestCaseID lookup instead of index alignment.
	tcMap := make(map[string]TestCase, len(suite.TestCases))
	for _, tc := range suite.TestCases {
		tcMap[tc.ID] = tc
	}

	scores := make([][]EvalScore, len(results))
	for i, result := range results {
		tc, ok := tcMap[result.TestCaseID]
		if !ok {
			return nil, nil, fmt.Errorf("evaluate result: no test case found for result %q", result.TestCaseID)
		}
		evalScores, err := evaluator.Evaluate(ctx, tc, result)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluate result: %w", err)
		}
		scores[i] = evalScores
	}

	return results, scores, nil
}
