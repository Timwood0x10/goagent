package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/ares_callbacks"
	"github.com/Timwood0x10/ares/internal/llm"
)

// TestNewCallbackRegistry verifies that NewCallbackRegistry returns a non-nil
// Registry that implements both Emitter and CallbackRegistrar interfaces.
func TestNewCallbackRegistry(t *testing.T) {
	reg := NewCallbackRegistry()
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}

	var _ ares_callbacks.Emitter = reg
	var _ ares_callbacks.CallbackRegistrar = reg
}

// TestNewLLMClientWithCallbacks verifies that the factory method creates an LLM
// client with the callback registry properly wired.
func TestNewLLMClientWithCallbacks(t *testing.T) {
	reg := NewCallbackRegistry()

	var emitted bool
	reg.On(ares_callbacks.EventLLMStart, func(ctx *ares_callbacks.Context) {
		emitted = true
	})

	cfg := &llm.Config{
		Provider: "ollama",
		Model:    "test-model",
		BaseURL:  "http://localhost:11434",
	}

	client, err := NewLLMClientWithCallbacks(cfg, reg)
	if err != nil {
		t.Fatalf("NewLLMClientWithCallbacks failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	_, _ = client.Generate(context.TODO(), "test prompt")
	if !emitted {
		t.Error("expected LLM start event to be emitted via callback registry")
	}
}

// TestNilSafety verifies that all bootstrap functions handle nil inputs gracefully.
func TestNilSafety(t *testing.T) {
	t.Run("NewLLMClientWithCallbacks with nil registry", func(t *testing.T) {
		cfg := &llm.Config{
			Provider: "ollama",
			Model:    "test",
			BaseURL:  "http://localhost:11434",
		}
		client, err := NewLLMClientWithCallbacks(cfg, nil)
		if err != nil {
			t.Fatalf("unexpected error with nil registry: %v", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client even with nil registry")
		}
	})
}
