package evolution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// mockGenomeMutator implements genome.MutatorInterface for testing.
type mockGenomeMutator struct{}

func (m *mockGenomeMutator) Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	result := make([]*mutation.Strategy, n)
	for i := range result {
		result[i] = &mutation.Strategy{
			ID:       parent.ID + "-mut",
			ParentID: parent.ID,
			Version:  parent.Version + 1,
			Params:   make(map[string]any),
			Score:    -1,
		}
	}
	return result, nil
}

func TestWireAfterRunReport_SavesReport(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.txt")

	base := &mutation.Strategy{
		ID: "base", Params: map[string]any{"t": 0.7},
		Score: 50.0,
	}
	pop, err := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
	if err != nil {
		t.Fatal(err)
	}

	system := &evolution.WiredEvolutionSystem{
		Population: pop,
	}

	cfg := &SystemConfig{
		ReportPath: reportPath,
	}

	wireAfterRunReport(system, cfg, nil, nil)

	if system.AfterRun == nil {
		t.Fatal("expected AfterRun to be set")
	}

	err = system.AfterRun(context.Background(), system)
	if err != nil {
		t.Fatalf("AfterRun failed: %v", err)
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	if !strings.Contains(string(data), "=== Evolution Report ===") {
		t.Error("expected report content in saved file")
	}
}

func TestWireAfterRunReport_NoPath(t *testing.T) {
	base := &mutation.Strategy{
		ID: "base", Params: map[string]any{"t": 0.7},
		Score: 50.0,
	}
	pop, err := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
	if err != nil {
		t.Fatal(err)
	}

	system := &evolution.WiredEvolutionSystem{
		Population: pop,
	}

	cfg := &SystemConfig{
		ReportPath: "",
	}

	wireAfterRunReport(system, cfg, nil, nil)

	if system.AfterRun != nil {
		t.Fatal("expected AfterRun to be nil when ReportPath is empty")
	}
}

func TestWireAfterRunReport_WithEvidence(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.txt")

	base := &mutation.Strategy{
		ID: "base", Params: map[string]any{"t": 0.7},
		Score: 50.0,
	}
	pop, err := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{})
	if err != nil {
		t.Fatal(err)
	}

	system := &evolution.WiredEvolutionSystem{
		Population: pop,
	}

	cfg := &SystemConfig{
		ReportPath: reportPath,
	}

	evidenceAgg := func(ctx context.Context, strategyID string) (Evidence, error) {
		return Evidence{
			StrategyID:  strategyID,
			SuccessRate: 0.9,
			SampleCount: 10,
			Confidence:  0.8,
		}, nil
	}
	promoter := func(ctx context.Context, strategyID string, ev Evidence) (string, string, error) {
		return "champion", "exceeds all thresholds", nil
	}

	wireAfterRunReport(system, cfg, evidenceAgg, promoter)

	if system.AfterRun == nil {
		t.Fatal("expected AfterRun to be set")
	}

	err = system.AfterRun(context.Background(), system)
	if err != nil {
		t.Fatalf("AfterRun failed: %v", err)
	}

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "champion") {
		t.Error("expected promotion state in report")
	}
	if !strings.Contains(content, "exceeds all thresholds") {
		t.Error("expected promotion reason in report")
	}
	if !strings.Contains(content, "Success Rate:  90.00%") {
		t.Error("expected success rate in report")
	}
}
