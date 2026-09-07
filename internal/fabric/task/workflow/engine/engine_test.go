// nolint: errcheck // Test code may ignore return values
package engine

import (
	"testing"
)

func TestWorkflowTypes(t *testing.T) {
	t.Run("create workflow", func(t *testing.T) {
		workflow := &Workflow{
			ID:   "wf1",
			Name: "test workflow",
		}

		if workflow.ID != "wf1" {
			t.Errorf("expected wf1, got %s", workflow.ID)
		}
	})

	t.Run("create step", func(t *testing.T) {
		step := &Step{
			ID:        "step1",
			Name:      "test step",
			AgentType: "leader",
		}

		if step.ID != "step1" {
			t.Errorf("expected step1, got %s", step.ID)
		}
	})
}

func TestDAG(t *testing.T) {
	t.Run("create DAG", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", DependsOn: []string{}},
			{ID: "step2", DependsOn: []string{"step1"}},
		}

		dag, err := NewDAG(steps)
		if err != nil {
			t.Errorf("create DAG error: %v", err)
		}

		if len(dag.Nodes) != 2 {
			t.Errorf("expected 2 nodes, got %d", len(dag.Nodes))
		}
	})

	t.Run("execution order", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", DependsOn: []string{}},
			{ID: "step2", DependsOn: []string{"step1"}},
			{ID: "step3", DependsOn: []string{"step2"}},
		}

		dag, _ := NewDAG(steps)
		order, err := dag.GetExecutionOrder()
		if err != nil {
			t.Errorf("get order error: %v", err)
		}

		if order[0] != "step1" {
			t.Errorf("expected step1 first, got %s", order[0])
		}
	})

	t.Run("detect cycle", func(t *testing.T) {
		steps := []*Step{
			{ID: "step1", DependsOn: []string{"step2"}},
			{ID: "step2", DependsOn: []string{"step1"}},
		}

		_, err := NewDAG(steps)
		if err != ErrCycleDetected {
			t.Errorf("expected cycle error")
		}
	})
}

func TestWorkflowErrors(t *testing.T) {
	t.Run("error definitions", func(t *testing.T) {
		if ErrInvalidDependency == nil {
			t.Errorf("ErrInvalidDependency should not be nil")
		}
		if ErrCycleDetected == nil {
			t.Errorf("ErrCycleDetected should not be nil")
		}
	})
}

// nolint: errcheck // Test code may ignore return values
