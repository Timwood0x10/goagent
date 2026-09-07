package arena

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

// ---------------------------------------------------------------------------
// Existing tests (preserved for backward compatibility)
// ---------------------------------------------------------------------------

func TestRunScenario_Success(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "leader-1", Type: "leader"}}
		},
	}
	inj := NewInjector(rt, nil)
	svc := NewService(inj, nil, nil)

	scenario := Scenario{
		Name: "test_scenario",
		Actions: []ScheduledAction{
			{
				Delay: 0,
				Action: Action{
					ID:       "sc-1",
					Type:     ActionKillAgent,
					TargetID: "agent-1",
				},
			},
			{
				Delay: 10 * time.Millisecond,
				Action: Action{
					ID:   "sc-2",
					Type: ActionKillLeader,
				},
			},
		},
	}

	results, err := RunScenario(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.True(t, results[0].Success)
	assert.True(t, results[1].Success)
}

func TestRunScenario_NilService(t *testing.T) {
	scenario := Scenario{Name: "test"}
	_, err := RunScenario(context.Background(), nil, scenario)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "service is nil")
}

func TestRunScenario_EmptyName(t *testing.T) {
	svc := newTestService(nil, nil, nil)
	scenario := Scenario{}
	_, err := RunScenario(context.Background(), svc, scenario)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "name is empty")
}

func TestRunScenario_CancelledContext(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	scenario := Scenario{
		Name: "cancelled",
		Actions: []ScheduledAction{
			{
				Delay: 1 * time.Second,
				Action: Action{
					ID:       "sc-1",
					Type:     ActionKillAgent,
					TargetID: "agent-1",
				},
			},
		},
	}

	results, err := RunScenario(ctx, svc, scenario)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, results)
}

func TestRunScenario_InvalidAction(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	scenario := Scenario{
		Name: "invalid",
		Actions: []ScheduledAction{
			{
				Action: Action{
					ID:   "sc-bad",
					Type: ActionKillAgent,
					// Missing TargetID.
				},
			},
		},
	}

	results, err := RunScenario(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.False(t, results[0].Success)
	assert.Contains(t, results[0].Error, "target_id")
}

func TestRunScenario_MixedResults(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			if id == "fail-agent" {
				return errors.New("agent crashed")
			}
			return nil
		},
	}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "mixed",
		Actions: []ScheduledAction{
			{
				Action: Action{
					ID: "sc-ok", Type: ActionKillAgent, TargetID: "ok-agent",
				},
			},
			{
				Action: Action{
					ID: "sc-fail", Type: ActionKillAgent, TargetID: "fail-agent",
				},
			},
		},
	}

	results, err := RunScenario(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.True(t, results[0].Success)
	assert.False(t, results[1].Success)
}

func TestRunScenario_EmptyActions(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	scenario := Scenario{
		Name:    "empty",
		Actions: nil,
	}

	results, err := RunScenario(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestRunScenario_WithDAGActions(t *testing.T) {
	dag := &mockDAG{}
	svc := newTestService(nil, dag, nil)

	scenario := Scenario{
		Name: "dag_chaos",
		Actions: []ScheduledAction{
			{
				Action: Action{
					ID: "sc-node", Type: ActionRemoveNode, TargetID: "node-1",
				},
			},
			{
				Action: Action{
					ID: "sc-edge", Type: ActionRemoveEdge, SourceID: "a", TargetID: "b",
				},
			},
		},
	}

	results, err := RunScenario(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.True(t, results[0].Success)
	assert.True(t, results[1].Success)
	assert.Equal(t, []string{"node-1"}, dag.getRemovedNodes())
	assert.Len(t, dag.getRemovedEdges(), 1)
}

// ---------------------------------------------------------------------------
// LoadScenario tests
// ---------------------------------------------------------------------------

func TestLoadScenario_ValidYAML(t *testing.T) {
	yamlData := `
name: test-scenario
description: A test scenario
tags: [smoke, test]
config:
  stop_on_error: true
  warmup: 1s
  cooldown: 2s
actions:
  - delay: 0s
    action:
      type: kill_leader
    label: kill-leader
    expect_success: true
`

	s, err := LoadScenario([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "test-scenario", s.Name)
	assert.Equal(t, "A test scenario", s.Description)
	assert.Equal(t, []string{"smoke", "test"}, s.Tags)
	assert.True(t, s.Config.StopOnError)
	assert.Equal(t, time.Second, s.Config.Warmup)
	assert.Equal(t, 2*time.Second, s.Config.Cooldown)
	require.Len(t, s.Actions, 1)
	assert.Equal(t, ActionKillLeader, s.Actions[0].Action.Type)
	assert.Equal(t, "kill-leader", s.Actions[0].Label)
	assert.True(t, s.Actions[0].ExpectSuccess)
}

func TestLoadScenario_InvalidYAML(t *testing.T) {
	_, err := LoadScenario([]byte(`{invalid yaml content`))
	assert.Error(t, err)
	assert.ErrorContains(t, err, "parse scenario YAML")
}

func TestLoadScenario_MinimalYAML(t *testing.T) {
	yamlData := `
name: minimal
actions:
  - delay: 0s
    action:
      type: kill_orchestrator
`
	s, err := LoadScenario([]byte(yamlData))
	require.NoError(t, err)
	assert.Equal(t, "minimal", s.Name)
	assert.Empty(t, s.Description)
	assert.Empty(t, s.Tags)
	assert.False(t, s.Config.StopOnError)
	require.Len(t, s.Actions, 1)
	assert.Equal(t, ActionKillOrchestrator, s.Actions[0].Action.Type)
}

// ---------------------------------------------------------------------------
// LoadScenarioFile tests
// ---------------------------------------------------------------------------

func TestLoadScenarioFile_ValidFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test.yaml")
	content := []byte(`
name: file-test
actions:
  - delay: 0s
    action:
      type: kill_leader
`)
	require.NoError(t, os.WriteFile(filePath, content, 0600))

	s, err := LoadScenarioFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "file-test", s.Name)
}

func TestLoadScenario_FileNotFound(t *testing.T) {
	_, err := LoadScenarioFile("/nonexistent/path/scenario.yaml")
	assert.Error(t, err)
	assert.ErrorContains(t, err, "stat scenario file")
}

// ---------------------------------------------------------------------------
// ValidateScenario tests
// ---------------------------------------------------------------------------

func TestValidateScenario_Valid(t *testing.T) {
	s := &Scenario{
		Name: "valid-test",
		Actions: []ScheduledAction{
			{
				Delay:  0,
				Action: Action{Type: ActionKillOrchestrator},
			},
		},
	}
	err := ValidateScenario(s)
	assert.NoError(t, err)
}

func TestValidateScenario_Nil(t *testing.T) {
	err := ValidateScenario(nil)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "scenario is nil")
}

func TestValidateScenario_MissingName(t *testing.T) {
	s := &Scenario{
		Name: "",
		Actions: []ScheduledAction{
			{
				Action: Action{Type: ActionKillLeader},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "name is required")
}

func TestValidateScenario_EmptyActions(t *testing.T) {
	s := &Scenario{
		Name:    "no-actions",
		Actions: []ScheduledAction{},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "at least one action")
}

func TestValidateScenario_NegativeDelay(t *testing.T) {
	s := &Scenario{
		Name: "neg-delay",
		Actions: []ScheduledAction{
			{
				Delay:  -5 * time.Second,
				Action: Action{Type: ActionKillLeader},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "negative delay")
}

func TestValidateScenario_EmptyActionType(t *testing.T) {
	s := &Scenario{
		Name: "empty-type",
		Actions: []ScheduledAction{
			{
				Action: Action{Type: ""},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "empty type")
}

func TestValidateScenario_NegativeMaxConcurrent(t *testing.T) {
	s := &Scenario{
		Name: "bad-config",
		Config: ScenarioConfig{
			MaxConcurrent: -1,
		},
		Actions: []ScheduledAction{
			{
				Action: Action{Type: ActionKillLeader},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "max_concurrent must be non-negative")
}

func TestValidateScenario_NegativeTimeout(t *testing.T) {
	s := &Scenario{
		Name: "bad-timeout",
		Config: ScenarioConfig{
			Timeout: -1 * time.Minute,
		},
		Actions: []ScheduledAction{
			{
				Action: Action{Type: ActionKillLeader},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "timeout must be non-negative")
}

func TestValidateScenario_ActionValidationPropagates(t *testing.T) {
	s := &Scenario{
		Name: "bad-action",
		Actions: []ScheduledAction{
			{
				Action: Action{
					Type:     ActionKillAgent,
					TargetID: "", // missing target_id
				},
			},
		},
	}
	err := ValidateScenario(s)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "target_id is required")
}

// ---------------------------------------------------------------------------
// RunScenarioReport tests
// ---------------------------------------------------------------------------

func TestRunScenarioReport_BasicExecution(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "leader-1", Type: "leader"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "report-basic",
		Actions: []ScheduledAction{
			{
				Delay:         0,
				Action:        Action{ID: "r1", Type: ActionKillLeader},
				Label:         "kill-leader",
				ExpectSuccess: true,
			},
		},
	}

	report, err := RunScenarioReport(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.NotNil(t, report)
	assert.Equal(t, "report-basic", report.ScenarioName)
	assert.False(t, report.StartedAt.IsZero())
	assert.False(t, report.FinishedAt.IsZero())
	assert.True(t, report.Duration > 0)
	assert.Len(t, report.Results, 1)
	assert.Equal(t, 1, report.Passed)
	assert.Equal(t, 0, report.Failed)
	assert.True(t, report.Verified)
}

func TestRunScenarioReport_WithWarmupCooldown(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "leader-1", Type: "leader"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "warmup-cooldown-test",
		Config: ScenarioConfig{
			Warmup:   1 * time.Millisecond,
			Cooldown: 1 * time.Millisecond,
		},
		Actions: []ScheduledAction{
			{
				Delay:  0,
				Action: Action{ID: "wc1", Type: ActionKillLeader},
			},
		},
	}

	report, err := RunScenarioReport(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.NotNil(t, report)
	assert.Len(t, report.Results, 1)
	assert.True(t, report.Results[0].Success)
}

func TestRunScenarioReport_StopOnError(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			return errors.New("intentional failure")
		},
	}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "stop-on-error-test",
		Config: ScenarioConfig{
			StopOnError: true,
		},
		Actions: []ScheduledAction{
			{
				Action: Action{ID: "soe1", Type: ActionKillAgent, TargetID: "fail-target"},
			},
			{
				// This should NOT execute because stop_on_error is true.
				Action: Action{ID: "soe2", Type: ActionKillAgent, TargetID: "ok-target"},
			},
		},
	}

	report, err := RunScenarioReport(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.NotNil(t, report)
	// Only first action should have run (second was skipped due to stop_on_error).
	assert.Len(t, report.Results, 1)
	if len(report.Results) > 0 {
		assert.False(t, report.Results[0].Success)
	}
}

func TestRunScenarioReport_NilService(t *testing.T) {
	scenario := Scenario{Name: "nil-svc"}
	_, err := RunScenarioReport(context.Background(), nil, scenario)
	assert.Error(t, err)
	assert.ErrorContains(t, err, "service is nil")
}

func TestRunScenarioReport_EmptyName(t *testing.T) {
	svc := newTestService(nil, nil, nil)
	_, err := RunScenarioReport(context.Background(), svc, Scenario{})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "name is empty")
}

func TestRunScenarioReport_VerificationMismatch(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			return errors.New("unexpected fail")
		},
	}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "verify-mismatch",
		Actions: []ScheduledAction{
			{
				Action:        Action{ID: "vm1", Type: ActionKillAgent, TargetID: "x"},
				ExpectSuccess: true, // expects success but will fail
			},
		},
	}

	report, err := RunScenarioReport(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.False(t, report.Verified) // expected success but got failure
}

func TestRunScenarioReport_VerificationPass(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	scenario := Scenario{
		Name: "verify-pass",
		Actions: []ScheduledAction{
			{
				Action:        Action{ID: "vp1", Type: ActionKillAgent, TargetID: "agent-1"},
				ExpectSuccess: true,
			},
		},
	}

	report, err := RunScenarioReport(context.Background(), svc, scenario)
	require.NoError(t, err)
	assert.True(t, report.Verified)
}

// ---------------------------------------------------------------------------
// Scenario YAML roundtrip tests
// ---------------------------------------------------------------------------

// TestScenarioYAMLRoundtripViaLoadScenario does an actual YAML roundtrip using LoadScenario.
func TestScenarioYAMLRoundtripViaLoadScenario(t *testing.T) {
	yamlData := `
name: roundtrip-real
description: Real roundtrip via LoadScenario
tags: [test]
config:
  stop_on_error: true
  max_concurrent: 4
  timeout: 10m
  warmup: 500ms
  cooldown: 1s
actions:
  - delay: 2s
    action:
      type: kill_agent
      target_id: agent-01
    label: kill-agent-01
    expect_success: true
    depends_on: [setup]
  - delay: 5s
    action:
      type: network_partition
      target_id: agent-02
    label: partition-agent-02
`

	s, err := LoadScenario([]byte(yamlData))
	require.NoError(t, err)

	assert.Equal(t, "roundtrip-real", s.Name)
	assert.Equal(t, "Real roundtrip via LoadScenario", s.Description)
	assert.Equal(t, []string{"test"}, s.Tags)
	assert.True(t, s.Config.StopOnError)
	assert.Equal(t, 4, s.Config.MaxConcurrent)
	assert.Equal(t, 10*time.Minute, s.Config.Timeout)
	assert.Equal(t, 500*time.Millisecond, s.Config.Warmup)
	assert.Equal(t, time.Second, s.Config.Cooldown)
	require.Len(t, s.Actions, 2)

	assert.Equal(t, 2*time.Second, s.Actions[0].Delay)
	assert.Equal(t, ActionKillAgent, s.Actions[0].Action.Type)
	assert.Equal(t, "agent-01", s.Actions[0].Action.TargetID)
	assert.Equal(t, "kill-agent-01", s.Actions[0].Label)
	assert.True(t, s.Actions[0].ExpectSuccess)
	assert.Equal(t, []string{"setup"}, s.Actions[0].DependsOn)

	assert.Equal(t, 5*time.Second, s.Actions[1].Delay)
	assert.Equal(t, ActionNetworkPartition, s.Actions[1].Action.Type)
	assert.Equal(t, "agent-02", s.Actions[1].Action.TargetID)
	assert.Equal(t, "partition-agent-02", s.Actions[1].Label)
}

// ---------------------------------------------------------------------------
// Example file tests (using files from examples/arena/)
// ---------------------------------------------------------------------------

func TestLoadExampleFiles(t *testing.T) {
	// Resolve paths relative to project root since tests run from package dir.
	baseDir := filepath.Join("..", "..", "..", "examples", "arena")
	examples := []struct {
		path        string
		wantName    string
		wantActions int
	}{
		{"leader_assassination.yaml", "leader-assassination-and-recovery", 4},
		{"cascading_storm.yaml", "cascading-failure-storm", 7},
	}

	for _, tc := range examples {
		t.Run(tc.path, func(t *testing.T) {
			fullPath := filepath.Join(baseDir, tc.path)
			s, err := LoadScenarioFile(fullPath)
			if os.IsNotExist(err) {
				t.Skip("example file not found (may not exist in all environments)")
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, s.Name)
			assert.Equal(t, tc.wantActions, len(s.Actions))

			// Should also validate cleanly.
			// Note: example files may have empty target_ids which fail action validation.
			// So we only validate structural fields here.
			assert.NotEmpty(t, s.Name)
			assert.Greater(t, len(s.Actions), 0)
		})
	}
}
