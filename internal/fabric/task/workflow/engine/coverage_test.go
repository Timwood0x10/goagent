// nolint: errcheck // Test code may ignore return values
package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// =====================================================
// Mock Agent for Testing
// =====================================================

type MockAgent struct {
	id          string
	agentType   string
	processFunc func(ctx context.Context, input any) (any, error)
}

func NewMockAgent(id, agentType string, processFunc func(ctx context.Context, input any) (any, error)) *MockAgent {
	return &MockAgent{
		id:          id,
		agentType:   agentType,
		processFunc: processFunc,
	}
}

func (m *MockAgent) ID() string {
	return m.id
}

func (m *MockAgent) Type() models.AgentType {
	return models.AgentType(m.agentType)
}

func (m *MockAgent) Status() models.AgentStatus {
	return models.AgentStatusReady
}

func (m *MockAgent) Start(ctx context.Context) error {
	return nil
}

func (m *MockAgent) Stop(ctx context.Context) error {
	return nil
}

func (m *MockAgent) Process(ctx context.Context, input any) (any, error) {
	if m.processFunc != nil {
		return m.processFunc(ctx, input)
	}
	return &models.RecommendResult{
		Items: []*models.RecommendItem{
			{
				ItemID:      "test-item-1",
				Name:        "Test Item 1",
				Description: "Mock agent result",
				Price:       100.0,
			},
		},
	}, nil
}

// ProcessStream handles input and returns a stream of events.
func (m *MockAgent) ProcessStream(ctx context.Context, input any) (<-chan base.AgentEvent, error) {
	result, err := m.Process(ctx, input)
	ch := make(chan base.AgentEvent, 1)
	ch <- base.AgentEvent{Type: base.EventComplete, Data: result, Err: err}
	close(ch)
	return ch, nil
}

// =====================================================
// DAG Comprehensive Tests
// =====================================================

func TestDAGCoverage(t *testing.T) {
	t.Run("create DAG with single step", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test"},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Fatalf("Failed to create DAG: %v", err)
		}

		if len(dag.Nodes) != 1 {
			t.Errorf("Expected 1 node, got %d", len(dag.Nodes))
		}

		if dag.Nodes["step1"].InDegree != 0 {
			t.Errorf("Expected InDegree 0, got %d", dag.Nodes["step1"].InDegree)
		}
	})

	t.Run("create DAG with dependencies", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{"step1"}},
			{ID: "step3", Name: "Third Step", AgentType: "test", DependsOn: []string{"step2"}},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Fatalf("Failed to create DAG: %v", err)
		}

		if len(dag.Nodes) != 3 {
			t.Errorf("Expected 3 nodes, got %d", len(dag.Nodes))
		}

		if dag.Nodes["step2"].InDegree != 1 {
			t.Errorf("Expected step2 InDegree 1, got %d", dag.Nodes["step2"].InDegree)
		}

		if dag.Nodes["step3"].InDegree != 1 {
			t.Errorf("Expected step3 InDegree 1, got %d", dag.Nodes["step3"].InDegree)
		}
	})

	t.Run("create DAG with multiple dependencies", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step3", Name: "Third Step", AgentType: "test", DependsOn: []string{"step1", "step2"}},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Fatalf("Failed to create DAG: %v", err)
		}

		if dag.Nodes["step3"].InDegree != 2 {
			t.Errorf("Expected step3 InDegree 2, got %d", dag.Nodes["step3"].InDegree)
		}
	})

	t.Run("detect simple cycle", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{"step2"}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{"step1"}},
		}

		_, err := NewDAG(steps)
		if err != ErrCycleDetected {
			t.Errorf("Expected ErrCycleDetected, got %v", err)
		}
	})

	t.Run("detect complex cycle", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{"step2"}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{"step3"}},
			{ID: "step3", Name: "Third Step", AgentType: "test", DependsOn: []string{"step1"}},
		}

		_, err := NewDAG(steps)
		if err != ErrCycleDetected {
			t.Errorf("Expected ErrCycleDetected, got %v", err)
		}
	})

	t.Run("detect self cycle", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{"step1"}},
		}

		_, err := NewDAG(steps)
		if err != ErrCycleDetected {
			t.Errorf("Expected ErrCycleDetected, got %v", err)
		}
	})

	t.Run("detect missing dependency", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{"nonexistent"}},
		}

		_, err := NewDAG(steps)
		if err != ErrInvalidDependency {
			t.Errorf("Expected ErrInvalidDependency, got %v", err)
		}
	})

	t.Run("linear execution order", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{"step1"}},
			{ID: "step3", Name: "Third Step", AgentType: "test", DependsOn: []string{"step2"}},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Fatalf("Failed to create DAG: %v", err)
		}

		order, err := dag.GetExecutionOrder()
		if err != nil {
			t.Fatalf("Failed to get execution order: %v", err)
		}

		if len(order) != 3 {
			t.Errorf("Expected 3 steps, got %d", len(order))
		}

		if order[0] != "step1" {
			t.Errorf("Expected first step to be step1, got %s", order[0])
		}

		if order[2] != "step3" {
			t.Errorf("Expected last step to be step3, got %s", order[2])
		}
	})

	t.Run("parallel execution order", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", Name: "First Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step2", Name: "Second Step", AgentType: "test", DependsOn: []string{}},
			{ID: "step3", Name: "Third Step", AgentType: "test", DependsOn: []string{"step1", "step2"}},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Fatalf("Failed to create DAG: %v", err)
		}

		order, err := dag.GetExecutionOrder()
		if err != nil {
			t.Fatalf("Failed to get execution order: %v", err)
		}

		if len(order) != 3 {
			t.Errorf("Expected 3 steps, got %d", len(order))
		}

		if order[2] != "step3" {
			t.Errorf("Expected last step to be step3, got %s", order[2])
		}
	})
}

// =====================================================
// Agent Registry Coverage Tests
// =====================================================

func TestAgentRegistryCoverage(t *testing.T) {
	t.Run("register and get factory", func(t *testing.T) {
		registry := NewAgentRegistry()

		factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
			return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
				return &models.RecommendResult{}, nil
			}), nil
		}

		err := registry.Register("test-agent", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		retrievedFactory, exists := registry.GetFactory("test-agent")
		if !exists {
			t.Error("Factory should exist after registration")
		}

		if retrievedFactory == nil {
			t.Error("Retrieved factory should not be nil")
		}
	})

	t.Run("register duplicate agent type", func(t *testing.T) {
		registry := NewAgentRegistry()

		factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
			return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
				return &models.RecommendResult{}, nil
			}), nil
		}

		err := registry.Register("test-agent", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		err = registry.Register("test-agent", factory)
		if err != ErrAgentTypeRegistered {
			t.Errorf("Expected ErrAgentTypeRegistered, got %v", err)
		}
	})

	t.Run("create agent from factory", func(t *testing.T) {
		registry := NewAgentRegistry()

		factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
			return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
				return &models.RecommendResult{}, nil
			}), nil
		}

		err := registry.Register("test-agent", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		agent, err := registry.CreateAgent(context.Background(), "test-agent", nil)
		if err != nil {
			t.Fatalf("Failed to create agent: %v", err)
		}

		if agent == nil {
			t.Error("Created agent should not be nil")
		}

		if agent.ID() != "mock-agent" {
			t.Errorf("Expected agent ID 'mock-agent', got %s", agent.ID())
		}
	})

	t.Run("create non-existent agent", func(t *testing.T) {
		registry := NewAgentRegistry()

		_, err := registry.CreateAgent(context.Background(), "non-existent", nil)
		if err == nil {
			t.Error("Expected error when creating non-existent agent")
		}

		if !errors.Is(err, ErrAgentTypeNotFound) {
			t.Errorf("Expected ErrAgentTypeNotFound, got %v", err)
		}
	})

	t.Run("list registered agent types", func(t *testing.T) {
		registry := NewAgentRegistry()

		factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
			return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
				return &models.RecommendResult{}, nil
			}), nil
		}

		err := registry.Register("agent1", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		err = registry.Register("agent2", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		types := registry.ListTypes()
		if len(types) != 2 {
			t.Errorf("Expected 2 agent types, got %d", len(types))
		}
	})

	t.Run("unregister agent", func(t *testing.T) {
		registry := NewAgentRegistry()

		factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
			return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
				return &models.RecommendResult{}, nil
			}), nil
		}

		err := registry.Register("test-agent", factory)
		if err != nil {
			t.Fatalf("Failed to register agent: %v", err)
		}

		registry.Unregister("test-agent")

		_, exists := registry.GetFactory("test-agent")
		if exists {
			t.Error("Factory should not exist after unregister")
		}
	})

	t.Run("concurrent registration safety", func(t *testing.T) {
		registry := NewAgentRegistry()
		var wg sync.WaitGroup

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				factory := func(ctx context.Context, config interface{}) (base.Agent, error) {
					return NewMockAgent("mock-agent", "test", func(ctx context.Context, input any) (any, error) {
						return &models.RecommendResult{}, nil
					}), nil
				}

				registry.Register(string(rune('a'+id)), factory)
			}(i)
		}

		wg.Wait()

		types := registry.ListTypes()
		if len(types) != 10 {
			t.Errorf("Expected 10 agent types, got %d", len(types))
		}
	})
}

// =====================================================
// Output Store Coverage Tests
// =====================================================

func TestOutputStoreCoverage(t *testing.T) {
	t.Run("set and get output", func(t *testing.T) {
		store := NewOutputStore()

		output := &StepOutput{
			StepID:    "step1",
			Output:    "test output",
			Variables: make(map[string]interface{}),
		}

		store.Set("step1", output)

		retrieved, exists := store.Get("step1")
		if !exists {
			t.Error("Output should exist after setting")
		}

		if retrieved.Output != "test output" {
			t.Errorf("Expected output 'test output', got '%s'", retrieved.Output)
		}
	})

	t.Run("get non-existent output", func(t *testing.T) {
		store := NewOutputStore()

		_, exists := store.Get("non-existent")
		if exists {
			t.Error("Non-existent output should not exist")
		}
	})

	t.Run("get multiple outputs", func(t *testing.T) {
		store := NewOutputStore()

		store.Set("step1", &StepOutput{
			StepID: "step1",
			Output: "output1",
		})

		store.Set("step2", &StepOutput{
			StepID: "step2",
			Output: "output2",
		})

		store.Set("step3", &StepOutput{
			StepID: "step3",
			Output: "output3",
		})

		outputs := store.GetMultiple([]string{"step1", "step3"})

		if len(outputs) != 2 {
			t.Errorf("Expected 2 outputs, got %d", len(outputs))
		}

		if outputs["step1"].Output != "output1" {
			t.Errorf("Expected output1, got %s", outputs["step1"].Output)
		}

		if outputs["step3"].Output != "output3" {
			t.Errorf("Expected output3, got %s", outputs["step3"].Output)
		}
	})

	t.Run("clear all outputs", func(t *testing.T) {
		store := NewOutputStore()

		store.Set("step1", &StepOutput{
			StepID: "step1",
			Output: "output1",
		})

		store.Set("step2", &StepOutput{
			StepID: "step2",
			Output: "output2",
		})

		store.Clear()

		_, exists := store.Get("step1")
		if exists {
			t.Error("Output should not exist after clear")
		}

		_, exists = store.Get("step2")
		if exists {
			t.Error("Output should not exist after clear")
		}
	})

	t.Run("concurrent access safety", func(t *testing.T) {
		store := NewOutputStore()
		var wg sync.WaitGroup

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				stepID := string(rune('a' + id))
				store.Set(stepID, &StepOutput{
					StepID: stepID,
					Output: "output",
				})

				store.Get(stepID)
			}(i)
		}

		wg.Wait()

		types := store.GetMultiple([]string{"a", "b", "c"})
		if len(types) != 3 {
			t.Errorf("Expected 3 outputs, got %d", len(types))
		}
	})
}

// =====================================================
// Error Definitions Coverage Tests
// =====================================================

func TestErrorDefinitionsCoverage(t *testing.T) {
	t.Run("verify error definitions", func(t *testing.T) {
		errors := []error{
			ErrInvalidDependency,
			ErrCycleDetected,
			ErrAgentTypeRegistered,
			ErrAgentTypeNotFound,
			ErrAgentResultNil,
			ErrWorkflowIncomplete,
			ErrInvalidLoader,
			ErrDuplicateID,
		}

		for _, err := range errors {
			if err == nil {
				t.Errorf("Error should not be nil: %v", err)
			}
			if err.Error() == "" {
				t.Errorf("Error message should not be empty: %v", err)
			}
		}
	})
}

// =====================================================
// Workflow Status Constants Coverage Tests
// =====================================================

func TestWorkflowStatusConstantsCoverage(t *testing.T) {
	t.Run("verify workflow status constants", func(t *testing.T) {
		statuses := []WorkflowStatus{
			WorkflowStatusPending,
			WorkflowStatusRunning,
			WorkflowStatusCompleted,
			WorkflowStatusFailed,
			WorkflowStatusCancelled,
		}

		for _, status := range statuses {
			if string(status) == "" {
				t.Errorf("Workflow status should not be empty: %v", status)
			}
		}
	})
}

func TestStepStatusConstantsCoverage(t *testing.T) {
	t.Run("verify step status constants", func(t *testing.T) {
		statuses := []StepStatus{
			StepStatusPending,
			StepStatusRunning,
			StepStatusCompleted,
			StepStatusFailed,
			StepStatusSkipped,
		}

		for _, status := range statuses {
			if string(status) == "" {
				t.Errorf("Step status should not be empty: %v", status)
			}
		}
	})
}

// =====================================================
// Workflow Types Coverage Tests
// =====================================================

//nolint:gocyclo // Test function with comprehensive test cases
func TestWorkflowTypesCoverage(t *testing.T) {
	t.Run("create workflow with all fields", func(t *testing.T) {
		workflow := &Workflow{
			ID:          "wf1",
			Name:        "Test Workflow",
			Version:     "1.0",
			Description: "A test workflow",
			Steps: []*Step{
				{
					ID:        "step1",
					Name:      "First Step",
					AgentType: "test",
				},
			},
			Variables: map[string]string{
				"var1": "value1",
			},
			Metadata: map[string]string{
				"meta1": "value1",
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		if workflow.ID != "wf1" {
			t.Errorf("Expected workflow ID 'wf1', got %s", workflow.ID)
		}

		if workflow.Name != "Test Workflow" {
			t.Errorf("Expected workflow name 'Test Workflow', got %s", workflow.Name)
		}

		if len(workflow.Steps) != 1 {
			t.Errorf("Expected 1 step, got %d", len(workflow.Steps))
		}
	})

	t.Run("create step with all fields", func(t *testing.T) {
		step := &Step{
			ID:         "step1",
			Name:       "Test Step",
			AgentType:  "test",
			Input:      "test input",
			DependsOn:  []string{"step0"},
			Timeout:    10 * time.Second,
			Status:     StepStatusPending,
			Output:     "step output",
			Error:      "",
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
			Metadata:   map[string]string{"key": "value"},
			RetryPolicy: &RetryPolicy{
				MaxAttempts:       3,
				InitialDelay:      1 * time.Second,
				MaxDelay:          10 * time.Second,
				BackoffMultiplier: 2.0,
			},
		}

		if step.ID != "step1" {
			t.Errorf("Expected step ID 'step1', got %s", step.ID)
		}

		if step.AgentType != "test" {
			t.Errorf("Expected agent type 'test', got %s", step.AgentType)
		}

		if len(step.DependsOn) != 1 {
			t.Errorf("Expected 1 dependency, got %d", len(step.DependsOn))
		}

		if step.Timeout != 10*time.Second {
			t.Errorf("Expected timeout 10s, got %v", step.Timeout)
		}

		if step.RetryPolicy.MaxAttempts != 3 {
			t.Errorf("Expected max attempts 3, got %d", step.RetryPolicy.MaxAttempts)
		}
	})

	t.Run("create workflow execution", func(t *testing.T) {
		execution := &WorkflowExecution{
			ID:         "exec1",
			WorkflowID: "wf1",
			Status:     WorkflowStatusRunning,
			StepStates: map[string]*StepState{
				"step1": {
					StepID:     "step1",
					Status:     StepStatusRunning,
					Output:     "step output",
					Error:      "",
					StartedAt:  time.Now(),
					FinishedAt: time.Now(),
					Attempts:   1,
				},
			},
			Variables: map[string]interface{}{
				"var1": "value1",
			},
			Context:   &models.TaskContext{},
			StartedAt: time.Now(),
		}

		if execution.ID != "exec1" {
			t.Errorf("Expected execution ID 'exec1', got %s", execution.ID)
		}

		if execution.Status != WorkflowStatusRunning {
			t.Errorf("Expected status %s, got %s", WorkflowStatusRunning, execution.Status)
		}

		if len(execution.StepStates) != 1 {
			t.Errorf("Expected 1 step state, got %d", len(execution.StepStates))
		}
	})

	t.Run("create workflow result", func(t *testing.T) {
		result := &WorkflowResult{
			ExecutionID: "exec1",
			WorkflowID:  "wf1",
			Status:      WorkflowStatusCompleted,
			Output: map[string]interface{}{
				"result1": "output1",
			},
			Error:    "",
			Duration: 10 * time.Second,
			Steps: []*StepResult{
				{
					StepID:   "step1",
					Name:     "First Step",
					Status:   StepStatusCompleted,
					Output:   "step output",
					Error:    "",
					Duration: 5 * time.Second,
					Metadata: map[string]string{"key": "value"},
				},
			},
		}

		if result.ExecutionID != "exec1" {
			t.Errorf("Expected execution ID 'exec1', got %s", result.ExecutionID)
		}

		if result.Status != WorkflowStatusCompleted {
			t.Errorf("Expected status %s, got %s", WorkflowStatusCompleted, result.Status)
		}

		if len(result.Steps) != 1 {
			t.Errorf("Expected 1 step result, got %d", len(result.Steps))
		}
	})

	t.Run("create step result", func(t *testing.T) {
		stepResult := &StepResult{
			StepID:   "step1",
			Name:     "Test Step",
			Status:   StepStatusCompleted,
			Output:   "step output",
			Error:    "",
			Duration: 5 * time.Second,
			Metadata: map[string]string{"key": "value"},
		}

		if stepResult.StepID != "step1" {
			t.Errorf("Expected step ID 'step1', got %s", stepResult.StepID)
		}

		if stepResult.Status != StepStatusCompleted {
			t.Errorf("Expected status %s, got %s", StepStatusCompleted, stepResult.Status)
		}
	})

	t.Run("create step state", func(t *testing.T) {
		stepState := &StepState{
			StepID:     "step1",
			Status:     StepStatusRunning,
			Output:     "step output",
			Error:      "step error",
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
			Attempts:   2,
		}

		if stepState.StepID != "step1" {
			t.Errorf("Expected step ID 'step1', got %s", stepState.StepID)
		}

		if stepState.Status != StepStatusRunning {
			t.Errorf("Expected status %s, got %s", StepStatusRunning, stepState.Status)
		}

		if stepState.Attempts != 2 {
			t.Errorf("Expected 2 attempts, got %d", stepState.Attempts)
		}
	})

	t.Run("create retry policy", func(t *testing.T) {
		policy := &RetryPolicy{
			MaxAttempts:       5,
			InitialDelay:      2 * time.Second,
			MaxDelay:          30 * time.Second,
			BackoffMultiplier: 3.0,
		}

		if policy.MaxAttempts != 5 {
			t.Errorf("Expected max attempts 5, got %d", policy.MaxAttempts)
		}

		if policy.InitialDelay != 2*time.Second {
			t.Errorf("Expected initial delay 2s, got %v", policy.InitialDelay)
		}

		if policy.MaxDelay != 30*time.Second {
			t.Errorf("Expected max delay 30s, got %v", policy.MaxDelay)
		}

		if policy.BackoffMultiplier != 3.0 {
			t.Errorf("Expected backoff multiplier 3.0, got %f", policy.BackoffMultiplier)
		}
	})

	t.Run("create DAG nodes", func(t *testing.T) {
		node := &DAGNode{
			StepID:    "step1",
			InDegree:  2,
			OutDegree: 3,
		}

		if node.StepID != "step1" {
			t.Errorf("Expected step ID 'step1', got %s", node.StepID)
		}

		if node.InDegree != 2 {
			t.Errorf("Expected in-degree 2, got %d", node.InDegree)
		}

		if node.OutDegree != 3 {
			t.Errorf("Expected out-degree 3, got %d", node.OutDegree)
		}
	})

	t.Run("create DAG structure", func(t *testing.T) {
		dag := &DAG{
			Nodes: map[string]*DAGNode{
				"step1": {
					StepID:    "step1",
					InDegree:  0,
					OutDegree: 1,
				},
				"step2": {
					StepID:    "step2",
					InDegree:  1,
					OutDegree: 0,
				},
			},
			Edges: map[string][]string{
				"step1": {"step2"},
			},
		}

		if len(dag.Nodes) != 2 {
			t.Errorf("Expected 2 nodes, got %d", len(dag.Nodes))
		}

		if len(dag.Edges) != 1 {
			t.Errorf("Expected 1 edge, got %d", len(dag.Edges))
		}

		if dag.Nodes["step1"].OutDegree != 1 {
			t.Errorf("Expected step1 out-degree 1, got %d", dag.Nodes["step1"].OutDegree)
		}

		if dag.Nodes["step2"].InDegree != 1 {
			t.Errorf("Expected step2 in-degree 1, got %d", dag.Nodes["step2"].InDegree)
		}
	})

	t.Run("create step output", func(t *testing.T) {
		output := &StepOutput{
			StepID: "step1",
			Output: "step output",
			Variables: map[string]interface{}{
				"var1": "value1",
				"var2": 123,
			},
		}

		if output.StepID != "step1" {
			t.Errorf("Expected step ID 'step1', got %s", output.StepID)
		}

		if output.Output != "step output" {
			t.Errorf("Expected output 'step output', got '%s'", output.Output)
		}

		if len(output.Variables) != 2 {
			t.Errorf("Expected 2 variables, got %d", len(output.Variables))
		}
	})
}

// nolint: errcheck // Test code may ignore return values
