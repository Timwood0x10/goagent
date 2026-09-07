// nolint: errcheck // Test code may ignore return values
package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestFileWatcher(t *testing.T, loader WorkflowLoader, workflows map[string]*Workflow) *FileWatcher {
	t.Helper()
	w, err := NewFileWatcher(loader, workflows)
	if err != nil {
		t.Fatalf("NewFileWatcher failed: %v", err)
	}
	return w
}

// =====================================================
// FileWatcher Coverage Tests
// =====================================================

func TestFileWatcherCoverage(t *testing.T) {
	t.Run("create file watcher", func(t *testing.T) {
		loader := NewJSONFileLoader()
		workflows := make(map[string]*Workflow)
		watcher := newTestFileWatcher(t, loader, workflows)

		if watcher == nil {
			t.Error("FileWatcher should not be nil")
			return
		}

		if watcher.pollInterval != 5*time.Second {
			t.Errorf("Expected poll interval 5s, got %v", watcher.pollInterval)
		}
	})

	t.Run("register and unregister callback", func(t *testing.T) {
		loader := NewJSONFileLoader()
		workflows := make(map[string]*Workflow)
		watcher := newTestFileWatcher(t, loader, workflows)

		callbackCalled := false
		callbackID := watcher.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCalled = true
		})

		if callbackID == "" {
			t.Error("Callback ID should not be empty")
		}

		// Manually trigger callbacks to test registration
		watcher.notifyCallbacks()

		if !callbackCalled {
			t.Error("Callback should have been called")
		}

		// Unregister and test again
		callbackCalled = false
		watcher.UnregisterCallback(callbackID)

		watcher.notifyCallbacks()

		if callbackCalled {
			t.Error("Callback should not be called after unregister")
		}
	})

	t.Run("register multiple callbacks", func(t *testing.T) {
		loader := NewJSONFileLoader()
		workflows := make(map[string]*Workflow)
		watcher := newTestFileWatcher(t, loader, workflows)

		callbackCount := 0

		watcher.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		watcher.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		watcher.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		watcher.notifyCallbacks()

		if callbackCount != 3 {
			t.Errorf("Expected 3 callbacks to be called, got %d", callbackCount)
		}
	})

	t.Run("scan and load from non-existent directory", func(t *testing.T) {
		loader := NewJSONFileLoader()
		workflows := make(map[string]*Workflow)
		watcher := newTestFileWatcher(t, loader, workflows)

		err := watcher.scanAndLoad(context.Background(), "/non/existent/directory")
		if err == nil {
			t.Error("Expected error with non-existent directory")
		}
	})

	t.Run("scan and load from empty directory", func(t *testing.T) {
		loader := NewJSONFileLoader()
		workflows := make(map[string]*Workflow)
		watcher := newTestFileWatcher(t, loader, workflows)

		// Use /tmp which should exist but may not have workflow files
		err := watcher.scanAndLoad(context.Background(), "/tmp")
		if err != nil {
			t.Logf("Expected error scanning /tmp: %v", err)
		}
	})
}

// =====================================================
// WorkflowReloader Coverage Tests
// =====================================================

func TestWorkflowReloaderCoverage(t *testing.T) {
	t.Run("create workflow reloader", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		if reloader == nil {
			t.Error("WorkflowReloader should not be nil")
			return
		}

		if reloader.workflows == nil {
			t.Error("Workflows map should be initialized")
		}

		if reloader.callbacks == nil {
			t.Error("Callbacks map should be initialized")
		}
	})

	t.Run("register and unregister callback", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		callbackCalled := false
		callbackID := reloader.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCalled = true
		})

		if callbackID == "" {
			t.Error("Callback ID should not be empty")
		}

		// Manually trigger callbacks
		reloader.notifyCallbacks()

		if !callbackCalled {
			t.Error("Callback should have been called")
		}

		// Unregister and test again
		callbackCalled = false
		reloader.UnregisterCallback(callbackID)

		reloader.notifyCallbacks()

		if callbackCalled {
			t.Error("Callback should not be called after unregister")
		}
	})

	t.Run("register multiple callbacks", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		callbackCount := 0

		reloader.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		reloader.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		reloader.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCount++
		})

		reloader.notifyCallbacks()

		if callbackCount != 3 {
			t.Errorf("Expected 3 callbacks to be called, got %d", callbackCount)
		}
	})

	t.Run("get workflow by ID", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		// Add a workflow manually
		workflow := &Workflow{
			ID:   "wf1",
			Name: "Test Workflow",
			Steps: []*Step{
				{ID: "step1", Name: "Step 1", AgentType: "leader"},
			},
		}

		reloader.workflows["wf1"] = workflow

		retrieved, exists := reloader.GetWorkflow("wf1")
		if !exists {
			t.Error("Workflow should exist")
		}

		if retrieved.ID != "wf1" {
			t.Errorf("Expected workflow ID 'wf1', got %s", retrieved.ID)
		}

		// Test non-existent workflow
		_, exists = reloader.GetWorkflow("non-existent")
		if exists {
			t.Error("Non-existent workflow should not exist")
		}
	})

	t.Run("list all workflows", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		// Add workflows manually
		reloader.workflows["wf1"] = &Workflow{ID: "wf1", Name: "Workflow 1"}
		reloader.workflows["wf2"] = &Workflow{ID: "wf2", Name: "Workflow 2"}
		reloader.workflows["wf3"] = &Workflow{ID: "wf3", Name: "Workflow 3"}

		workflows := reloader.ListWorkflows()
		if len(workflows) != 3 {
			t.Errorf("Expected 3 workflows, got %d", len(workflows))
		}

		for i, wf := range workflows {
			if wf.ID != "wf1" && wf.ID != "wf2" && wf.ID != "wf3" {
				t.Errorf("Unexpected workflow at index %d: %s", i, wf.ID)
			}
		}
	})

	t.Run("list workflows from empty reloader", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		workflows := reloader.ListWorkflows()
		if len(workflows) != 0 {
			t.Errorf("Expected 0 workflows, got %d", len(workflows))
		}
	})

	t.Run("stop watching", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		// Create a watcher manually
		watcher := newTestFileWatcher(t, loader, reloader.workflows)
		reloader.watcher = watcher

		// Stop watching should not panic
		reloader.StopWatching()

		if reloader.watcher != nil {
			t.Error("Watcher should be nil after stop")
		}
	})

	t.Run("load from non-existent directory", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		err := reloader.Load(context.Background(), "/non/existent/directory")
		if err == nil {
			t.Error("Expected error with non-existent directory")
		}
	})

	t.Run("start watching from non-existent directory", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		err := reloader.StartWatching(context.Background(), "/non/existent/directory")
		if err == nil {
			t.Error("Expected error with non-existent directory")
		}
	})

	t.Run("on reload callback", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		// Add a callback to verify it's called
		callbackCalled := false
		var receivedWorkflows map[string]*Workflow

		reloader.RegisterCallback(func(workflows map[string]*Workflow) {
			callbackCalled = true
			receivedWorkflows = workflows
		})

		// Create test workflows
		testWorkflows := map[string]*Workflow{
			"wf1": {ID: "wf1", Name: "Workflow 1"},
			"wf2": {ID: "wf2", Name: "Workflow 2"},
		}

		// Trigger onReload
		reloader.onReload(testWorkflows)

		if !callbackCalled {
			t.Error("Callback should have been called")
		}

		if len(receivedWorkflows) != 2 {
			t.Errorf("Expected 2 workflows in callback, got %d", len(receivedWorkflows))
		}

		// Verify workflows are stored
		stored, exists := reloader.GetWorkflow("wf1")
		if !exists {
			t.Error("Workflow should be stored after reload")
		}

		if stored.ID != "wf1" {
			t.Errorf("Expected workflow ID 'wf1', got %s", stored.ID)
		}
	})

	t.Run("workflow update on reload", func(t *testing.T) {
		loader := NewJSONFileLoader()
		reloader := NewWorkflowReloader(loader)

		// Initial workflows
		initialWorkflows := map[string]*Workflow{
			"wf1": {
				ID:        "wf1",
				Name:      "Original Workflow",
				UpdatedAt: time.Now().Add(-1 * time.Hour),
			},
		}

		reloader.onReload(initialWorkflows)

		// Updated workflows
		updatedWorkflows := map[string]*Workflow{
			"wf1": {
				ID:        "wf1",
				Name:      "Updated Workflow",
				UpdatedAt: time.Now(),
			},
		}

		reloader.onReload(updatedWorkflows)

		// Verify workflow is updated
		stored, exists := reloader.GetWorkflow("wf1")
		if !exists {
			t.Error("Workflow should exist after update")
		}

		if stored.Name != "Updated Workflow" {
			t.Errorf("Expected updated name 'Updated Workflow', got %s", stored.Name)
		}
	})

	t.Run("invalid loader type", func(t *testing.T) {
		// Create a mock loader that doesn't implement the expected interface
		invalidLoader := &mockInvalidLoader{}
		reloader := NewWorkflowReloader(invalidLoader)

		err := reloader.Load(context.Background(), "/tmp")
		if err != ErrInvalidLoader {
			t.Errorf("Expected ErrInvalidLoader, got %v", err)
		}

		err = reloader.StartWatching(context.Background(), "/tmp")
		if err != ErrInvalidLoader {
			t.Errorf("Expected ErrInvalidLoader, got %v", err)
		}
	})
}

// =====================================================
// Mock Invalid Loader for Testing
// =====================================================

type mockInvalidLoader struct{}

func (m *mockInvalidLoader) Load(ctx context.Context, source string) (*Workflow, error) {
	return nil, errors.New("invalid loader: returns nil without error")
}

// =====================================================
// Callback With ID Coverage Tests
// =====================================================

func TestCallbackWithIDCoverage(t *testing.T) {
	t.Run("create callback with ID", func(t *testing.T) {
		called := false
		callback := callbackWithID{
			id: "callback-1",
			fn: func(workflows map[string]*Workflow) {
				called = true
			},
		}

		if callback.id != "callback-1" {
			t.Errorf("Expected ID 'callback-1', got %s", callback.id)
		}

		if callback.fn == nil {
			t.Error("Callback function should not be nil")
		}

		// Test calling the callback
		callback.fn(nil)

		if !called {
			t.Error("Callback function should have been called")
		}
	})
}

// =====================================================
// Reload Callback Type Coverage Tests
// =====================================================

func TestReloadCallbackCoverage(t *testing.T) {
	t.Run("verify reload callback type", func(t *testing.T) {
		// This test verifies the ReloadCallback type is correctly defined
		var callback ReloadCallback = func(workflows map[string]*Workflow) {
			// Callback implementation
		}

		if callback == nil {
			t.Error("Callback should not be nil")
		}

		// Test calling the callback
		testWorkflows := map[string]*Workflow{
			"wf1": {ID: "wf1", Name: "Test Workflow"},
		}

		callback(testWorkflows)
	})
}

// nolint: errcheck // Test code may ignore return values
