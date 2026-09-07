package evaluation

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Evaluator is the main entry point for running evaluation scenarios.
type Evaluator struct {
	name      string
	scenarios map[string]*Scenario
	mu        sync.RWMutex
}

// Scenario bundles a runner with metadata.
type Scenario struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Runs        int           `json:"runs"`
	Timeout     time.Duration `json:"timeout"`
	Runner      Runner        `json:"-"`
}

// New creates a new Evaluator.
func New(name string) *Evaluator {
	return &Evaluator{
		name:      name,
		scenarios: make(map[string]*Scenario),
	}
}

// Register adds a scenario to the evaluator.
func (e *Evaluator) Register(sc *Scenario) error {
	if sc.Name == "" {
		return fmt.Errorf("scenario name is required")
	}
	if sc.Runner == nil {
		return fmt.Errorf("scenario %q has no runner", sc.Name)
	}
	if sc.Runs <= 0 {
		sc.Runs = 1
	}
	if sc.Timeout <= 0 {
		sc.Timeout = 60 * time.Second
	}
	e.mu.Lock()
	e.scenarios[sc.Name] = sc
	e.mu.Unlock()
	return nil
}

// RunScenario executes a registered scenario and returns a report.
func (e *Evaluator) RunScenario(ctx context.Context, name string) (*Report, error) {
	e.mu.RLock()
	sc, ok := e.scenarios[name]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("scenario %q not found", name)
	}

	fmt.Printf("Running scenario: %s (%d runs)\n", sc.Name, sc.Runs)
	var results []Metrics
	for i := 0; i < sc.Runs; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		taskName := fmt.Sprintf("%s/run-%d", sc.Name, i+1)
		m, err := runWithTimeout(ctx, sc.Runner, taskName, sc.Timeout)
		if err != nil {
			results = append(results, Metrics{
				Scenario: sc.Name,
				Task:     taskName,
				Success:  false,
				Score:    0,
				Error:    err.Error(),
			})
			continue
		}
		results = append(results, *m)
	}

	report := Aggregate(sc.Name, results)
	return &report, nil
}

// RunAll executes all registered scenarios.
func (e *Evaluator) RunAll(ctx context.Context) (map[string]*Report, error) {
	e.mu.RLock()
	names := make([]string, 0, len(e.scenarios))
	for n := range e.scenarios {
		names = append(names, n)
	}
	e.mu.RUnlock()

	reports := make(map[string]*Report, len(names))
	for _, name := range names {
		r, err := e.RunScenario(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("scenario %q: %w", name, err)
		}
		reports[name] = r
	}
	return reports, nil
}

// ListScenarios returns the names of all registered scenarios.
func (e *Evaluator) ListScenarios() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.scenarios))
	for n := range e.scenarios {
		names = append(names, n)
	}
	return names
}
