package evolution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveReport_NilReport(t *testing.T) {
	err := SaveReport(context.Background(), nil, "/tmp/report.txt")
	if err == nil {
		t.Fatal("expected error for nil report")
	}
}

func TestSaveReport_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reports", "latest.txt")

	report := &EvolutionReport{
		TotalGenerations:   10,
		BestEverScore:      95.5,
		BestEverGeneration: 8,
		FinalBestScore:     92.0,
		WinnerStrategyID:   "s1",
		WinnerScore:        92.0,
		PromotionState:     "champion",
		PromotionReason:    "exceeds threshold",
		SuccessRate:        0.85,
		SampleCount:        25,
		Confidence:         0.72,
	}

	err := SaveReport(context.Background(), report, path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "=== Evolution Report ===") {
		t.Error("expected report header")
	}
	if !strings.Contains(content, "champion") {
		t.Error("expected promotion state in report")
	}
	if !strings.Contains(content, "exceeds threshold") {
		t.Error("expected promotion reason in report")
	}
	if !strings.Contains(content, "Success Rate:  85.00%") {
		t.Error("expected success rate in report")
	}
	if !strings.Contains(content, "Samples:       25") {
		t.Error("expected sample count in report")
	}
	if !strings.Contains(content, "Confidence:    72.00%") {
		t.Error("expected confidence in report")
	}
	if !strings.Contains(content, "Winner:        s1") {
		t.Error("expected winner in report")
	}
	if !strings.Contains(content, "Winner Score:  92.0000") {
		t.Error("expected winner score in report")
	}
}

func TestSaveReport_CreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	deepPath := filepath.Join(dir, "a", "b", "c", "report.txt")

	report := &EvolutionReport{
		TotalGenerations: 5,
		FinalBestScore:   80.0,
	}

	err := SaveReport(context.Background(), report, deepPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(deepPath); os.IsNotExist(err) {
		t.Error("expected report file to exist")
	}
}

func TestSaveReport_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")

	// Write initial content.
	initial := &EvolutionReport{TotalGenerations: 1, FinalBestScore: 10.0}
	if err := SaveReport(context.Background(), initial, path); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Overwrite with new content.
	updated := &EvolutionReport{TotalGenerations: 2, FinalBestScore: 20.0}
	if err := SaveReport(context.Background(), updated, path); err != nil {
		t.Fatalf("updated save: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "Total Generations:    2") {
		t.Error("expected overwritten content")
	}
}

func TestReportString_WithoutPromotion(t *testing.T) {
	report := &EvolutionReport{
		TotalGenerations:   3,
		BestEverScore:      90.0,
		BestEverGeneration: 2,
		FinalBestScore:     85.0,
	}

	str := ReportString(report)
	if !strings.Contains(str, "Total Generations:    3") {
		t.Error("expected generation count")
	}
	if strings.Contains(str, "Promotion Summary") {
		t.Error("expected no promotion section when fields are empty")
	}
}

func TestReportString_NilReport(t *testing.T) {
	str := ReportString(nil)
	if str != "(nil report)" {
		t.Errorf("expected '(nil report)', got %s", str)
	}
}
