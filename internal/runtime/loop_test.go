package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note: mockMemoryPlugin is defined in router_memory_test.go (same package);
// LoopPlugin tests reuse it because OnRoundEnd consumes MemoryPlugin instances
// from the bus. It must not be redeclared here.

// TestLoopPlugin_OnRoundEnd_AdvisesMemoryWithRound verifies OnRoundEnd wires
// the real available state (ExecutionID + round number) into the RouteState
// passed to MemoryPlugin.AdviseRoute. Previously the RouteState carried only
// the ExecutionID and the constructed ExecutionState was discarded via
// `_ = state`, so memory advice was driven with effectively empty context.
func TestLoopPlugin_OnRoundEnd_AdvisesMemoryWithRound(t *testing.T) {
	bus := NewPluginBus()

	var captured RouteState
	capturedExecID := ""
	mem := &mockMemoryPlugin{
		name: "test-memory",
		adviceFn: func(_ context.Context, state RouteState) ([]RouteAdvice, error) {
			captured = state
			capturedExecID = state.ExecutionID
			return nil, nil
		},
	}
	loop := NewLoopPlugin("test-loop", LoopConfig{MaxIterations: 3})

	require.NoError(t, bus.Register(mem))
	require.NoError(t, bus.Register(loop))
	require.NoError(t, bus.Start(context.Background()))

	loop.OnRoundEnd(context.Background(), 2, "exec-42")

	assert.Equal(t, "exec-42", capturedExecID)
	assert.Equal(t, "exec-42", captured.ExecutionID)
	// The round number is real data available at the OnRoundEnd boundary; it
	// must be carried into the memory advice context, not discarded.
	assert.NotNil(t, captured.Variables)
	assert.Equal(t, 2, captured.Variables["round"])
}

// TestLoopPlugin_OnRoundEnd_MemoryErrorIsLogged verifies OnRoundEnd does not
// propagate AdviseRoute errors (it logs and continues), keeping the round
// boundary non-fatal.
func TestLoopPlugin_OnRoundEnd_MemoryErrorIsLogged(t *testing.T) {
	bus := NewPluginBus()

	mem := &mockMemoryPlugin{
		name: "failing-memory",
		adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
			return nil, assertError("memory unavailable")
		},
	}
	loop := NewLoopPlugin("test-loop", LoopConfig{MaxIterations: 3})

	require.NoError(t, bus.Register(mem))
	require.NoError(t, bus.Register(loop))
	require.NoError(t, bus.Start(context.Background()))

	// Must not panic / must complete despite the memory plugin erroring.
	loop.OnRoundEnd(context.Background(), 1, "exec-1")
	assert.Equal(t, 1, loop.Iteration())
}

// assertError is a tiny helper returning a named error for the failing-memory
// stub, kept local to avoid pulling in extra packages.
type loopTestErr string

func (e loopTestErr) Error() string { return string(e) }

func assertError(msg string) error { return loopTestErr(msg) }
