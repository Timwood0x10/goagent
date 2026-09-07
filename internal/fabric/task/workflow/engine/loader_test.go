// nolint: errcheck // Test code may ignore return values
package engine

import (
	"context"
	"testing"
	"time"
)

// =====================================================
// Decoder Coverage Tests
// =====================================================

func TestDecoderCoverage(t *testing.T) {
	t.Run("JSON decoder decode valid JSON", func(t *testing.T) {
		decoder := &JSONDecoder{}

		data := []byte(`{"name": "test"}`)
		var result map[string]string

		err := decoder.Decode(data, &result)
		if err != nil {
			t.Errorf("Decode error: %v", err)
		}

		if result["name"] != "test" {
			t.Errorf("Expected 'test', got %s", result["name"])
		}
	})

	t.Run("JSON decoder decode invalid JSON", func(t *testing.T) {
		decoder := &JSONDecoder{}

		data := []byte(`{invalid json}`)
		var result map[string]string

		err := decoder.Decode(data, &result)
		if err == nil {
			t.Error("Expected error with invalid JSON")
		}
	})

	t.Run("YAML decoder decode valid YAML", func(t *testing.T) {
		decoder := &YAMLDecoder{}

		data := []byte(`name: test
value: 123
`)
		var result map[string]interface{}

		err := decoder.Decode(data, &result)
		if err != nil {
			t.Errorf("Decode error: %v", err)
		}

		if result["name"] != "test" {
			t.Errorf("Expected 'test', got %v", result["name"])
		}
	})

	t.Run("YAML decoder decode invalid YAML", func(t *testing.T) {
		decoder := &YAMLDecoder{}

		data := []byte(`invalid: yaml: content:`)
		var result map[string]interface{}

		err := decoder.Decode(data, &result)
		if err == nil {
			t.Error("Expected error with invalid YAML")
		}
	})
}

// =====================================================
// FileLoader Coverage Tests
// =====================================================

func TestFileLoaderCoverage(t *testing.T) {
	t.Run("create file loader with JSON decoder", func(t *testing.T) {
		decoder := &JSONDecoder{}
		loader := NewFileLoader(decoder)

		if loader == nil {
			t.Error("Loader should not be nil")
		}
	})

	t.Run("create JSON file loader", func(t *testing.T) {
		loader := NewJSONFileLoader()

		if loader == nil {
			t.Error("Loader should not be nil")
		}
	})

	t.Run("create YAML file loader", func(t *testing.T) {
		loader := NewYAMLFileLoader()

		if loader == nil {
			t.Error("Loader should not be nil")
		}
	})

	t.Run("parse workflow JSON", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{
			"id": "wf1",
			"name": "Test Workflow",
			"version": "1.0",
			"description": "A test workflow",
			"steps": [
				{
					"id": "step1",
					"name": "First Step",
					"agent_type": "leader",
					"input": "test input"
				}
			],
			"variables": {
				"var1": "value1"
			},
			"metadata": {
				"meta1": "value1"
			}
		}`)

		workflow, err := loader.Parse(context.Background(), data, "test.json")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
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

	t.Run("parse workflow YAML", func(t *testing.T) {
		loader := NewYAMLFileLoader()

		data := []byte(`
id: wf2
name: Test Workflow YAML
version: 1.0
description: A test workflow in YAML
steps:
  - id: step1
    name: First Step
    agent_type: leader
    input: test input
variables:
  var1: value1
metadata:
  meta1: value1
`)

		workflow, err := loader.Parse(context.Background(), data, "test.yaml")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if workflow.ID != "wf2" {
			t.Errorf("Expected workflow ID 'wf2', got %s", workflow.ID)
		}

		if len(workflow.Steps) != 1 {
			t.Errorf("Expected 1 step, got %d", len(workflow.Steps))
		}
	})

	t.Run("parse workflow with invalid JSON", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{invalid json}`)

		_, err := loader.Parse(context.Background(), data, "test.json")
		if err == nil {
			t.Error("Expected error with invalid JSON")
		}
	})

	t.Run("parse workflow with retry policy", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{
			"id": "wf3",
			"name": "Test Workflow with Retry",
			"steps": [
				{
					"id": "step1",
					"name": "Retry Step",
					"agent_type": "leader",
					"retry_policy": {
						"max_attempts": 3,
						"initial_delay": 1000000000,
						"max_delay": 30000000000,
						"backoff_multiplier": 2.0
					}
				}
			]
		}`)

		workflow, err := loader.Parse(context.Background(), data, "test.json")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if workflow.Steps[0].RetryPolicy == nil {
			t.Error("Retry policy should not be nil")
		}

		if workflow.Steps[0].RetryPolicy.MaxAttempts != 3 {
			t.Errorf("Expected max attempts 3, got %d", workflow.Steps[0].RetryPolicy.MaxAttempts)
		}
	})

	t.Run("parse workflow with dependencies", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{
			"id": "wf4",
			"name": "Test Workflow with Dependencies",
			"steps": [
				{
					"id": "step1",
					"name": "First Step",
					"agent_type": "leader"
				},
				{
					"id": "step2",
					"name": "Second Step",
					"agent_type": "leader",
					"depends_on": ["step1"]
				},
				{
					"id": "step3",
					"name": "Third Step",
					"agent_type": "leader",
					"depends_on": ["step1", "step2"]
				}
			]
		}`)

		workflow, err := loader.Parse(context.Background(), data, "test.json")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if len(workflow.Steps) != 3 {
			t.Errorf("Expected 3 steps, got %d", len(workflow.Steps))
		}

		if len(workflow.Steps[1].DependsOn) != 1 {
			t.Errorf("Expected 1 dependency for step2, got %d", len(workflow.Steps[1].DependsOn))
		}

		if len(workflow.Steps[2].DependsOn) != 2 {
			t.Errorf("Expected 2 dependencies for step3, got %d", len(workflow.Steps[2].DependsOn))
		}
	})

	t.Run("load workflow from non-existent file", func(t *testing.T) {
		loader := NewJSONFileLoader()

		_, err := loader.Load(context.Background(), "/non/existent/file.json")
		if err == nil {
			t.Error("Expected error with non-existent file")
		}
	})

	t.Run("parse workflow with timeout", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{
			"id": "wf5",
			"name": "Test Workflow with Timeout",
			"steps": [
				{
					"id": "step1",
					"name": "Timeout Step",
					"agent_type": "leader",
					"timeout": 30000000000
				}
			]
		}`)

		workflow, err := loader.Parse(context.Background(), data, "test.json")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if workflow.Steps[0].Timeout != 30*time.Second {
			t.Errorf("Expected timeout 30s, got %v", workflow.Steps[0].Timeout)
		}
	})

	t.Run("parse workflow with metadata", func(t *testing.T) {
		loader := NewJSONFileLoader()

		data := []byte(`{
			"id": "wf6",
			"name": "Test Workflow with Metadata",
			"steps": [
				{
					"id": "step1",
					"name": "Metadata Step",
					"agent_type": "leader",
					"metadata": {
						"priority": "high",
						"category": "test"
					}
				}
			]
		}`)

		workflow, err := loader.Parse(context.Background(), data, "test.json")
		if err != nil {
			t.Fatalf("Parse error: %v", err)
		}

		if workflow.Steps[0].Metadata == nil {
			t.Error("Metadata should not be nil")
		}

		if workflow.Steps[0].Metadata["priority"] != "high" {
			t.Errorf("Expected priority 'high', got %s", workflow.Steps[0].Metadata["priority"])
		}
	})
}

// =====================================================
// DirectoryLoader Coverage Tests
// =====================================================

func TestDirectoryLoaderCoverage(t *testing.T) {
	t.Run("create directory loader", func(t *testing.T) {
		fileLoader := NewJSONFileLoader()
		dirLoader := NewDirectoryLoader(fileLoader)

		if dirLoader == nil {
			t.Error("Directory loader should not be nil")
		}
	})

	t.Run("load from non-existent directory", func(t *testing.T) {
		fileLoader := NewJSONFileLoader()
		dirLoader := NewDirectoryLoader(fileLoader)

		_, err := dirLoader.LoadAll(context.Background(), "/non/existent/directory")
		if err == nil {
			t.Error("Expected error with non-existent directory")
		}
	})

	t.Run("get file extension", func(t *testing.T) {
		testCases := []struct {
			filename string
			expected string
		}{
			{"test.json", ".json"},
			{"test.yaml", ".yaml"},
			{"test.yml", ".yml"},
			{"test", ""},
			{"test.txt", ".txt"},
		}

		for _, tc := range testCases {
			result := getFileExt(tc.filename)
			if result != tc.expected {
				t.Errorf("Expected extension %s for %s, got %s", tc.expected, tc.filename, result)
			}
		}
	})

	t.Run("handle duplicate workflow IDs", func(t *testing.T) {
		if ErrDuplicateID == nil {
			t.Error("ErrDuplicateID should not be nil")
		}
	})
}

// =====================================================
// WorkflowFile Coverage Tests
// =====================================================

func TestWorkflowFileCoverage(t *testing.T) {
	t.Run("create workflow file", func(t *testing.T) {
		wfFile := &WorkflowFile{
			ID:          "wf1",
			Name:        "Test Workflow",
			Version:     "1.0",
			Description: "Test description",
			Steps: []*StepFile{
				{
					ID:        "step1",
					Name:      "First Step",
					AgentType: "leader",
				},
			},
			Variables: map[string]string{
				"var1": "value1",
			},
			Metadata: map[string]string{
				"meta1": "value1",
			},
		}

		if wfFile.ID != "wf1" {
			t.Errorf("Expected ID 'wf1', got %s", wfFile.ID)
		}

		if len(wfFile.Steps) != 1 {
			t.Errorf("Expected 1 step, got %d", len(wfFile.Steps))
		}
	})

	t.Run("create step file with all fields", func(t *testing.T) {
		stepFile := &StepFile{
			ID:        "step1",
			Name:      "Test Step",
			AgentType: "leader",
			Input:     "test input",
			DependsOn: []string{"step0"},
			Timeout:   30 * time.Second,
			RetryPolicy: &RetryPolicyFile{
				MaxAttempts:       3,
				InitialDelay:      1 * time.Second,
				MaxDelay:          10 * time.Second,
				BackoffMultiplier: 2.0,
			},
			Metadata: map[string]string{
				"key": "value",
			},
		}

		if stepFile.ID != "step1" {
			t.Errorf("Expected ID 'step1', got %s", stepFile.ID)
		}

		if stepFile.RetryPolicy == nil {
			t.Error("Retry policy should not be nil")
		}

		if stepFile.RetryPolicy.MaxAttempts != 3 {
			t.Errorf("Expected max attempts 3, got %d", stepFile.RetryPolicy.MaxAttempts)
		}
	})
}

// =====================================================
// Convert Functions Coverage Tests
// =====================================================

func TestConvertFunctionsCoverage(t *testing.T) {
	t.Run("convert step file to step", func(t *testing.T) {
		stepFile := &StepFile{
			ID:        "step1",
			Name:      "Test Step",
			AgentType: "leader",
			Input:     "test input",
			DependsOn: []string{"step0"},
			Timeout:   30 * time.Second,
			Metadata: map[string]string{
				"key": "value",
			},
		}

		step, err := convertStep(stepFile)
		if err != nil {
			t.Fatalf("Convert step error: %v", err)
		}

		if step.ID != "step1" {
			t.Errorf("Expected ID 'step1', got %s", step.ID)
		}

		if step.Status != StepStatusPending {
			t.Errorf("Expected status %s, got %s", StepStatusPending, step.Status)
		}

		if step.RetryPolicy != nil {
			t.Error("Retry policy should be nil when not specified in step file")
		}
	})

	t.Run("convert workflow file to workflow", func(t *testing.T) {
		loader := NewJSONFileLoader()

		wfFile := &WorkflowFile{
			ID:          "wf1",
			Name:        "Test Workflow",
			Version:     "1.0",
			Description: "Test description",
			Steps: []*StepFile{
				{
					ID:        "step1",
					Name:      "First Step",
					AgentType: "leader",
				},
			},
			Variables: map[string]string{
				"var1": "value1",
			},
			Metadata: map[string]string{
				"meta1": "value1",
			},
		}

		workflow, err := loader.convert(wfFile, "test.json")
		if err != nil {
			t.Fatalf("Convert workflow error: %v", err)
		}

		if workflow.ID != "wf1" {
			t.Errorf("Expected ID 'wf1', got %s", workflow.ID)
		}

		if workflow.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero")
		}

		if workflow.UpdatedAt.IsZero() {
			t.Error("UpdatedAt should not be zero")
		}

		if len(workflow.Variables) != 1 {
			t.Errorf("Expected 1 variable, got %d", len(workflow.Variables))
		}
	})

	t.Run("convert workflow file with multiple steps", func(t *testing.T) {
		loader := NewJSONFileLoader()

		wfFile := &WorkflowFile{
			ID:   "wf2",
			Name: "Test Workflow",
			Steps: []*StepFile{
				{ID: "step1", Name: "Step 1", AgentType: "leader"},
				{ID: "step2", Name: "Step 2", AgentType: "leader", DependsOn: []string{"step1"}},
				{ID: "step3", Name: "Step 3", AgentType: "leader", DependsOn: []string{"step1", "step2"}},
			},
		}

		workflow, err := loader.convert(wfFile, "test.json")
		if err != nil {
			t.Fatalf("Convert workflow error: %v", err)
		}

		if len(workflow.Steps) != 3 {
			t.Errorf("Expected 3 steps, got %d", len(workflow.Steps))
		}

		for i, step := range workflow.Steps {
			if step.Status != StepStatusPending {
				t.Errorf("Step %d should have status pending, got %s", i, step.Status)
			}
		}
	})
}

// nolint: errcheck // Test code may ignore return values
