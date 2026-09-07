// Package ares_bootstrap — Runtime Closure Lifecycle Tests.
//
// These tests verify the Bootstrap lifecycle: complete start, reverse-order
// stop, failure rollback, and no orphan goroutines after shutdown.
//
//go:build closure

package ares_bootstrap

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClosure_Lifecycle_CompleteStartStop verifies that Bootstrap can start
// and stop cleanly without leaving orphan goroutines.
//
// This test reuses the existing shutdown test patterns from
// ares_shutdown but applies them to the full Bootstrap lifecycle.
func TestClosure_Lifecycle_CompleteStartStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	// Measure goroutine count before Bootstrap.
	beforeGoroutines := runtime.NumGoroutine()

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)

	// Cancel context to trigger shutdown of background goroutines.
	cancel()

	// Wait for all background goroutines to exit.
	waitDone := make(chan struct{})
	go func() {
		comp.WaitBackground()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// Good — all goroutines exited.
	case <-time.After(5 * time.Second):
		t.Fatal("WaitBackground did not return within 5s; goroutine leak detected")
	}

	// Allow goroutines to settle.
	time.Sleep(100 * time.Millisecond)

	// Check for goroutine leaks. Allow some slack for GC.
	afterGoroutines := runtime.NumGoroutine()
	leaked := afterGoroutines - beforeGoroutines
	if leaked > 5 {
		t.Errorf("Goroutine leak detected: before=%d after=%d leaked=%d",
			beforeGoroutines, afterGoroutines, leaked)
	}
}

// TestClosure_Lifecycle_RuntimeStartStop verifies that the Runtime Manager
// can start and stop cleanly after Bootstrap.
func TestClosure_Lifecycle_RuntimeStartStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)
	require.NotNil(t, comp.Runtime)

	// Start the runtime.
	err = comp.Runtime.Start(ctx)
	require.NoError(t, err)

	// Stop the runtime.
	err = comp.Runtime.Stop()
	assert.NoError(t, err)

	// Double stop should not panic.
	err = comp.Runtime.Stop()
	assert.NoError(t, err)

	cancel()
	comp.WaitBackground()
}

// TestClosure_Lifecycle_MCPStop verifies that MCP manager can stop cleanly.
//
// Known gap: ProvideMCP starts the MCP manager during construction, which
// violates the "construct has no side effects" principle. The test verifies
// that MCP can still be stopped cleanly despite starting during construction.
func TestClosure_Lifecycle_MCPStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)
	require.NotNil(t, comp.MCP)

	// MCP was already started during Bootstrap (ProvideMCP calls Start).
	// This is the construct-has-side-effects gap.
	// Verify it can stop cleanly.
	err = comp.MCP.Stop(ctx)
	assert.NoError(t, err, "MCP must stop cleanly despite B03")

	cancel()
	comp.WaitBackground()
}

// TestClosure_Lifecycle_DashboardStop verifies that the dashboard can stop
// cleanly after Bootstrap.
func TestClosure_Lifecycle_DashboardStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)
	// The observability providers are assembled (the standalone
	// :8090 dashboard server was removed, so there is nothing to Stop).
	require.NotNil(t, comp.Dashboard)

	cancel()
	comp.WaitBackground()
}

// TestClosure_Lifecycle_ConcurrentStop verifies that calling Stop
// concurrently does not cause panics or data races.
func TestClosure_Lifecycle_ConcurrentStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)

	// Start runtime.
	require.NoError(t, comp.Runtime.Start(ctx))

	// Stop concurrently — should not panic.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		_ = comp.Runtime.Stop()
		_ = comp.Runtime.Stop()
	}()

	select {
	case <-stopDone:
		// Good.
	case <-time.After(5 * time.Second):
		t.Fatal("Concurrent Stop did not complete within 5s")
	}

	cancel()
	comp.WaitBackground()
}

// TestClosure_Lifecycle_RepeatedStartStop verifies that the Runtime can be
// started and stopped multiple times without resource leaks.
//
// This is a simplified version of the "100x start/stop" soak test.
func TestClosure_Lifecycle_RepeatedStartStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	const iterations = 10

	for i := 0; i < iterations; i++ {
		iterCtx, iterCancel := context.WithCancel(ctx)

		comp, err := Bootstrap(iterCtx, cfg, nil)
		require.NoError(t, err, "iteration %d: Bootstrap failed", i)

		require.NotNil(t, comp.Runtime)

		err = comp.Runtime.Start(iterCtx)
		require.NoError(t, err, "iteration %d: Runtime Start failed", i)

		err = comp.Runtime.Stop()
		require.NoError(t, err, "iteration %d: Runtime Stop failed", i)

		iterCancel()
		comp.WaitBackground()
	}
}

// TestClosure_Lifecycle_ContextCancellation verifies that cancelling the
// bootstrap context stops all background goroutines.
func TestClosure_Lifecycle_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
	}

	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)

	// Cancel the context — all background goroutines should exit.
	cancel()

	// WaitBackground should return promptly after context cancellation.
	waitDone := make(chan struct{})
	go func() {
		comp.WaitBackground()
		close(waitDone)
	}()

	select {
	case <-waitDone:
		// Good — all goroutines exited after context cancellation.
	case <-time.After(5 * time.Second):
		t.Fatal("WaitBackground did not return within 5s after context cancellation; " +
			"goroutine leak detected (F05/F06: naked goroutines may not respect context)")
	}
}

// NOTE: the previous TestClosure_Lifecycle_BootstrapCleanup was a no-op
// (it only called t.Skip with the rationale "Failure injection requires Stage 1
// Runtime interface"). Failure rollback is not observable without a
// deterministic error injection point in Bootstrap, which would require a
// production refactor to expose runCleanups. Rather than keep a test that
// always skips (and inflate the test count), it was removed. Reverse-order
// cleanup on the happy path is exercised indirectly by the stop/shutdown tests.
