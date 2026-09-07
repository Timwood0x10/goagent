package evaluation

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestEvaluatorNew(t *testing.T) {
	e := New("test")
	if e == nil {
		t.Fatal("New returned nil")
	}
	if e.name != "test" {
		t.Fatalf("expected name 'test', got %q", e.name)
	}
}

func TestRegisterAndRun(t *testing.T) {
	e := New("test")
	err := e.Register(&Scenario{
		Name: "ping",
		Runs: 3,
		Runner: RunnerFunc(func(_ context.Context, task string) (*Metrics, error) {
			return &Metrics{
				Scenario:   "ping",
				Task:       task,
				Success:    true,
				Score:      1.0,
				Latency:    10 * time.Millisecond,
				TokenCount: 5,
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("Register error: %v", err)
	}

	report, err := e.RunScenario(context.Background(), "ping")
	if err != nil {
		t.Fatalf("RunScenario error: %v", err)
	}
	if report.Runs != 3 {
		t.Fatalf("expected 3 runs, got %d", report.Runs)
	}
	if report.Passed != 3 {
		t.Fatalf("expected 3 passed, got %d", report.Passed)
	}
	if report.PassRate != 100.0 {
		t.Fatalf("expected 100%% pass rate, got %.0f", report.PassRate)
	}
}

func TestRegisterValidation(t *testing.T) {
	e := New("test")

	tests := []struct {
		name string
		sc   *Scenario
	}{
		{"empty name", &Scenario{Runner: RunnerFunc(func(_ context.Context, _ string) (*Metrics, error) { return &Metrics{}, nil })}},
		{"nil runner", &Scenario{Name: "no-runner"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := e.Register(tt.sc); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRunScenarioNotFound(t *testing.T) {
	e := New("test")
	_, err := e.RunScenario(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown scenario")
	}
}

func TestRunAll(t *testing.T) {
	e := New("multi")
	for _, s := range []string{"a", "b"} {
		_ = e.Register(&Scenario{
			Name: s,
			Runner: RunnerFunc(func(_ context.Context, task string) (*Metrics, error) {
				return &Metrics{Task: task, Success: true, Score: 1.0}, nil
			}),
		})
	}

	reports, err := e.RunAll(context.Background())
	if err != nil {
		t.Fatalf("RunAll error: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reports))
	}
}

func TestListScenarios(t *testing.T) {
	e := New("test")
	_ = e.Register(&Scenario{Name: "s1", Runner: RunnerFunc(func(_ context.Context, _ string) (*Metrics, error) {
		return &Metrics{}, nil
	})})
	_ = e.Register(&Scenario{Name: "s2", Runner: RunnerFunc(func(_ context.Context, _ string) (*Metrics, error) {
		return &Metrics{}, nil
	})})

	names := e.ListScenarios()
	if len(names) != 2 {
		t.Fatalf("expected 2 scenarios, got %d", len(names))
	}
}

func TestRunTimeout(t *testing.T) {
	e := New("timeout")
	_ = e.Register(&Scenario{
		Name:    "slow",
		Runs:    1,
		Timeout: 10 * time.Millisecond,
		Runner: RunnerFunc(func(_ context.Context, task string) (*Metrics, error) {
			time.Sleep(100 * time.Millisecond)
			return &Metrics{Task: task, Success: true}, nil
		}),
	})

	report, err := e.RunScenario(context.Background(), "slow")
	if err != nil {
		t.Fatalf("RunScenario error: %v", err)
	}
	if report.Failed != 1 {
		t.Fatalf("expected 1 failure due to timeout, got %d passed", report.Passed)
	}
}

func TestAggregate(t *testing.T) {
	results := []Metrics{
		{Success: true, Score: 1.0, Latency: 100 * time.Millisecond, TokenCount: 10},
		{Success: true, Score: 0.8, Latency: 200 * time.Millisecond, TokenCount: 20},
		{Success: false, Score: 0.0, Latency: 50 * time.Millisecond, TokenCount: 5},
	}
	r := Aggregate("agg", results)
	if r.Runs != 3 {
		t.Fatalf("expected 3 runs, got %d", r.Runs)
	}
	if r.Passed != 2 {
		t.Fatalf("expected 2 passed, got %d", r.Passed)
	}
	if r.PassRate != 66.66666666666666 {
		t.Fatalf("expected 66.6%% pass rate, got %.1f", r.PassRate)
	}
}

func TestReportMarkdown(t *testing.T) {
	r := Aggregate("test", []Metrics{
		{Task: "task-1", Success: true, Score: 1.0, Latency: 100 * time.Millisecond},
	})
	md := r.ToMarkdown()
	if !strings.Contains(md, "Evaluation Report") {
		t.Fatal("markdown missing title")
	}
	if !strings.Contains(md, "✅") {
		t.Fatal("markdown missing success indicator")
	}
}

func TestReportJSON(t *testing.T) {
	r := Aggregate("test", []Metrics{
		{Task: "task-1", Success: true, Score: 1.0},
	})
	json, err := r.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}
	if !strings.Contains(json, `"pass_rate"`) {
		t.Fatal("json missing pass_rate")
	}
}

func TestRunnerFunc(t *testing.T) {
	var called bool
	f := RunnerFunc(func(_ context.Context, task string) (*Metrics, error) {
		called = true
		return &Metrics{Task: task, Success: true}, nil
	})
	m, err := f.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("RunnerFunc error: %v", err)
	}
	if !called {
		t.Fatal("RunnerFunc was not called")
	}
	if !m.Success {
		t.Fatal("expected success")
	}
}
