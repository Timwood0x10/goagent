package arena

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario defines a chaos engineering scenario with rich configuration.
type Scenario struct {
	Name        string            `yaml:"name" json:"name"`                                   // required
	Description string            `yaml:"description,omitempty" json:"description,omitempty"` // optional
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`               // optional labels
	Config      ScenarioConfig    `yaml:"config,omitempty" json:"config,omitempty"`           // scenario-level config
	Actions     []ScheduledAction `yaml:"actions" json:"actions"`                             // required, at least 1
}

// ScenarioConfig holds scenario-level configuration.
type ScenarioConfig struct {
	StopOnError     bool          `yaml:"stop_on_error,omitempty" json:"stop_on_error,omitempty"`       // stop on first failure
	ParallelActions bool          `yaml:"parallel_actions,omitempty" json:"parallel_actions,omitempty"` // execute actions concurrently
	MaxConcurrent   int           `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`     // max parallel actions (default 3)
	Timeout         time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`                   // overall scenario timeout
	Warmup          time.Duration `yaml:"warmup,omitempty" json:"warmup,omitempty"`                     // delay before first action
	Cooldown        time.Duration `yaml:"cooldown,omitempty" json:"cooldown,omitempty"`                 // delay after last action
}

// ScheduledAction pairs a delay with an action to execute.
type ScheduledAction struct {
	Delay         time.Duration `yaml:"delay" json:"delay"`                                       // delay before this action
	Action        Action        `yaml:"action" json:"action"`                                     // the action to execute
	Label         string        `yaml:"label,omitempty" json:"label,omitempty"`                   // human-readable label
	ExpectSuccess bool          `yaml:"expect_success,omitempty" json:"expect_success,omitempty"` // expected result for verification
	ExpectFailure bool          `yaml:"expect_failure,omitempty" json:"expect_failure,omitempty"` // expect the action to fail (verification)
	DependsOn     []string      `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`         // labels of actions this depends on
}

// ScenarioReport holds the results of a scenario execution.
type ScenarioReport struct {
	ScenarioName string          `json:"scenario_name"`
	Description  string          `json:"description,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Duration     time.Duration   `json:"duration"`
	Results      []Result        `json:"results"`
	Passed       int             `json:"passed"`
	Failed       int             `json:"failed"`
	Score        ResilienceScore `json:"score"`
	Verified     bool            `json:"verified"` // all expect_success matched actual
}

// LoadScenario reads and parses a scenario from YAML data.
func LoadScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("arena: parse scenario YAML: %w", err)
	}
	return &s, nil
}

// LoadScenarioFile reads a scenario from a file path with size limit.
func LoadScenarioFile(path string) (*Scenario, error) {
	const maxFileSize = 10 * 1024 * 1024 // 10MB

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("arena: stat scenario file %s: %w", path, err)
	}
	if fi.Size() > maxFileSize {
		return nil, fmt.Errorf("arena: scenario file too large: %d bytes (max: %d)", fi.Size(), maxFileSize)
	}

	data, err := os.ReadFile(path) // #nosec G304
	if err != nil {
		return nil, fmt.Errorf("arena: read scenario file %s: %w", path, err)
	}
	return LoadScenario(data)
}

// ValidateScenario checks that a scenario is well-formed.
func ValidateScenario(s *Scenario) error {
	if s == nil {
		return errors.New("arena: scenario is nil")
	}
	if s.Name == "" {
		return errors.New("arena: scenario name is required")
	}
	if len(s.Actions) == 0 {
		return errors.New("arena: scenario must have at least one action")
	}
	for i, sa := range s.Actions {
		if sa.Delay < 0 {
			return fmt.Errorf("arena: action[%d] has negative delay: %v", i, sa.Delay)
		}
		if sa.Action.Type == "" {
			return fmt.Errorf("arena: action[%d] has empty type", i)
		}
		if err := ValidateAction(sa.Action); err != nil {
			return fmt.Errorf("arena: action[%d] validation failed: %w", i, err)
		}
	}
	if s.Config.MaxConcurrent < 0 {
		return errors.New("arena: config.max_concurrent must be non-negative")
	}
	if s.Config.Timeout < 0 {
		return errors.New("arena: config.timeout must be non-negative")
	}
	return nil
}

// RunScenarioReport executes a scenario and returns a full report.
// It supports warmup/cooldown delays, timeout context, and stop-on-error behavior.
func RunScenarioReport(ctx context.Context, service *Service, scenario Scenario) (*ScenarioReport, error) {
	if service == nil {
		return nil, errors.New("arena: service is nil")
	}
	if scenario.Name == "" {
		return nil, errors.New("arena: scenario name is empty")
	}

	report := &ScenarioReport{
		ScenarioName: scenario.Name,
		Description:  scenario.Description,
		StartedAt:    time.Now(),
	}

	// Apply overall timeout if configured.
	cfg := scenario.Config
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	log.Info("arena: running scenario report",
		"name", scenario.Name,
		"actions", len(scenario.Actions),
		"warmup", cfg.Warmup,
		"timeout", cfg.Timeout,
	)

	// Warn about configured fields that are not yet implemented.
	if cfg.ParallelActions {
		log.Warn("arena: parallel_actions is configured but not yet supported; actions will run sequentially",
			"name", scenario.Name,
		)
	}
	if cfg.MaxConcurrent > 0 {
		log.Warn("arena: max_concurrent is configured but not yet supported; actions will run sequentially",
			"name", scenario.Name,
			"max_concurrent", cfg.MaxConcurrent,
		)
	}
	hasDependsOn := false
	for _, sa := range scenario.Actions {
		if len(sa.DependsOn) > 0 {
			hasDependsOn = true
			break
		}
	}
	if hasDependsOn {
		log.Warn("arena: depends_on is configured on one or more actions but not yet enforced; execution order follows action list order",
			"name", scenario.Name,
		)
	}

	// Warmup delay before first action.
	if cfg.Warmup > 0 {
		timer := time.NewTimer(cfg.Warmup)
		select {
		case <-ctx.Done():
			timer.Stop()
			report.FinishedAt = time.Now()
			report.Duration = time.Since(report.StartedAt)
			return report, ctx.Err()
		case <-timer.C:
		}
	}

	results := make([]Result, 0, len(scenario.Actions))

	for i, sa := range scenario.Actions {
		// Apply per-action delay.
		if sa.Delay > 0 {
			timer := time.NewTimer(sa.Delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Warn("arena: scenario cancelled during delay",
					"name", scenario.Name,
					"step", i,
				)
				report.FinishedAt = time.Now()
				report.Duration = time.Since(report.StartedAt)
				report.Results = results
				report.computeTotals()
				report.Score = CalculateScoreV1(service.Stats(), service.calculateAvgRecoveryTime(nil))
				return report, ctx.Err()
			case <-timer.C:
			}
		}

		// Validate before execution.
		if err := ValidateAction(sa.Action); err != nil {
			result := Result{
				Success:  false,
				Action:   sa.Action,
				Error:    err.Error(),
				Duration: 0,
			}
			results = append(results, result)
			log.Error("arena: scenario action validation failed",
				"name", scenario.Name,
				"step", i,
				"label", sa.Label,
				"error", err,
			)

			if cfg.StopOnError {
				break
			}
			continue
		}

		result := service.Execute(ctx, sa.Action)
		results = append(results, result)

		log.Info("arena: scenario step completed",
			"name", scenario.Name,
			"step", i,
			"label", sa.Label,
			"type", sa.Action.Type,
			"success", result.Success,
		)

		// Stop on error if configured.
		if cfg.StopOnError && !result.Success {
			log.Warn("arena: stopping scenario due to stop_on_error",
				"name", scenario.Name,
				"step", i,
			)
			break
		}
	}

	// Cooldown after all actions.
	if cfg.Cooldown > 0 {
		timer := time.NewTimer(cfg.Cooldown)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}

	report.FinishedAt = time.Now()
	report.Duration = time.Since(report.StartedAt)
	report.Results = results
	report.computeTotals()
	// Compute score from per-scenario results, not from global Stats (which
	// accumulate across runs). This fixes C-02: score now reflects this
	// scenario's actual recovery speed.
	scenarioStats := service.Stats()
	var totalDuration time.Duration
	recovered := 0
	for _, r := range results {
		if r.Success {
			recovered++
			totalDuration += r.Duration
		}
	}
	avgRecovery := time.Duration(0)
	if recovered > 0 {
		avgRecovery = totalDuration / time.Duration(recovered)
	}
	report.Score = CalculateScoreV1(scenarioStats, avgRecovery)
	report.checkVerified(scenario)

	log.Info("arena: scenario report completed",
		"name", scenario.Name,
		"passed", report.Passed,
		"failed", report.Failed,
		"duration", report.Duration,
		"verified", report.Verified,
	)

	return report, nil
}

// computeTotals fills in Passed/Failed/Skipped from Results.
func (r *ScenarioReport) computeTotals() {
	for _, res := range r.Results {
		if res.Success {
			r.Passed++
		} else {
			r.Failed++
		}
	}
}

// checkVerified compares ExpectSuccess and ExpectFailure against actual results.
// Actions with ExpectSuccess=true must succeed; actions with ExpectFailure=true
// must fail. Actions with neither set are not verified.
func (r *ScenarioReport) checkVerified(scenario Scenario) {
	allMatch := true
	hasExpectations := false
	for i, res := range r.Results {
		if i >= len(scenario.Actions) {
			continue
		}
		sa := scenario.Actions[i]
		if !sa.ExpectSuccess && !sa.ExpectFailure {
			continue
		}
		hasExpectations = true
		// ExpectSuccess=true wants res.Success=true.
		// ExpectFailure=true wants res.Success=false.
		if sa.ExpectSuccess != res.Success {
			allMatch = false
		}
		if sa.ExpectFailure && res.Success {
			allMatch = false
		}
	}
	r.Verified = !hasExpectations || allMatch
}

// RunScenario executes all actions in a scenario with the specified delays.
// Returns the results of all executed actions. Stops if the context is cancelled.
//
// Deprecated: Use RunScenarioReport for structured results.
// Kept for backward compatibility.
func RunScenario(ctx context.Context, service *Service, scenario Scenario) ([]Result, error) {
	log.Warn("RunScenario is deprecated, use RunScenarioReport instead")

	report, err := RunScenarioReport(ctx, service, scenario)
	if err != nil {
		return nil, err
	}

	// Unpack report into legacy return values.
	return report.Results, nil
}
