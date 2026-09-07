// package graph - tests for state management.

package graph

import (
	"testing"
)

func TestState(t *testing.T) {
	state := NewState()

	// Test Set and Get
	state.Set("key1", "value1")
	val, ok := state.Get("key1")
	if !ok {
		t.Error("expected key1 to exist")
	}
	if val != "value1" {
		t.Errorf("expected value1, got %v", val)
	}

	// Test non-existent key
	_, ok = state.Get("nonexistent")
	if ok {
		t.Error("expected non-existent key to not exist")
	}

	// Test multiple keys
	state.Set("key2", 42)
	state.Set("key3", true)

	// Test ToParams
	params := state.ToParams()
	if len(params) != 3 {
		t.Errorf("expected 3 params, got %d", len(params))
	}
	if params["key1"] != "value1" {
		t.Error("params should have key1")
	}
	if params["key2"] != 42 {
		t.Error("params should have key2")
	}
	if params["key3"] != true {
		t.Error("params should have key3")
	}
}

func TestNewState(t *testing.T) {
	state := NewState()
	if state.values == nil {
		t.Error("NewState did not initialize values map")
	}
}

//nolint:staticcheck // This test intentionally uses nil state to verify nil-safety
func TestStateNil(t *testing.T) {
	// Test nil state behavior - all methods should handle nil safely
	var state *State

	// Test Get on nil state
	val, ok := state.Get("key")
	if ok {
		t.Error("expected false for nil state Get")
	}
	if val != nil {
		t.Error("expected nil for nil state Get")
	}

	// Test Set on nil state (should not panic)
	state.Set("key", "value")

	// Test ToParams on nil state
	params := state.ToParams()
	if len(params) != 0 {
		t.Errorf("expected empty params for nil state, got %d", len(params))
	}
}
