package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInterruptPlugin_Capabilities verifies the plugin advertises CapInterrupt
// so it is discoverable via PluginsByCap(CapInterrupt). Previously
// Capabilities() returned nil, making the plugin invisible to capability
// queries.
func TestInterruptPlugin_Capabilities(t *testing.T) {
	p := NewInterruptPlugin("test-hitl")
	caps := p.Capabilities()
	assert.NotEmpty(t, caps)
	assert.Contains(t, caps, CapInterrupt)
}

// TestInterruptPlugin_DiscoverableByCap verifies the bus indexes the plugin
// under CapInterrupt after Register.
func TestInterruptPlugin_DiscoverableByCap(t *testing.T) {
	bus := NewPluginBus()
	require.NoError(t, bus.Register(NewInterruptPlugin("hitl")))

	plugins := bus.PluginsByCap(CapInterrupt)
	require.Len(t, plugins, 1)
	assert.Equal(t, "hitl", plugins[0].Name())
}

// TestInterruptPlugin_WithCollectorConcurrent exercises concurrent
// WithCollector swaps against AfterStep reads under -race. WithCollector must
// not race with the collector read in AfterStep.
func TestInterruptPlugin_WithCollectorConcurrent(t *testing.T) {
	p := NewInterruptPlugin("race-hitl")
	require.NoError(t, p.Start(context.Background(), NewPluginBus()))

	result := &StepResult{
		StepID: "s1",
		Status: StepStatusSkipped,
		Error:  "rejected by human",
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Writer: swap collectors continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				p.WithCollector(NewExecutionCollector("exec-1"))
			}
		}
	}()

	// Reader: invoke AfterStep, which snapshots the collector under the lock.
	for i := 0; i < 200; i++ {
		require.NoError(t, p.AfterStep(context.Background(), "exec-1", result))
	}

	close(done)
	wg.Wait()
}
