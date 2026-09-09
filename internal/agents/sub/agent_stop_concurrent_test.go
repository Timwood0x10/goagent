package sub

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/errors"
)

// TestSubAgent_ConcurrentStop_NoDoubleClose is the 1.9 regression: two
// concurrent Stop calls both passed the `status == Offline` guard (the first
// had already set Stopping), both read the same stopCh and both closed it —
// `panic: close of closed channel` crashed the process. Run under -race the
// panic surfaces as a hard failure; the guard is the timeout, since a
// wedged Stop (or a crashed test binary) fails the run.
func TestSubAgent_ConcurrentStop_NoDoubleClose(t *testing.T) {
	executor := newStubExecutor()
	agent := New("sub-concurrent-stop", models.AgentTypeTop, executor, nil)

	require.NoError(t, agent.Start(context.Background()))

	const stoppers = 4 // beyond the pair: a burst must be equally safe
	var wg sync.WaitGroup
	errs := make([]error, stoppers)
	start := make(chan struct{})

	for i := 0; i < stoppers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // maximize the window where all Stop calls overlap
			errs[idx] = agent.Stop(context.Background())
		}(i)
	}
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent Stop calls never returned (deadlock or panic swallowed)")
	}

	// Exactly one Stop performs the shutdown; the others observe an agent
	// already stopping/stopped. Both outcomes are legitimate, so concurrent
	// Stop must return either nil or ErrAgentNotRunning — never a panic,
	// which the -race/panic guard above already caught.
	for i, err := range errs {
		if err != nil {
			assert.ErrorIs(t, err, errors.ErrAgentNotRunning,
				"Stop %d: only ErrAgentNotRunning is acceptable alongside the winner's nil, got %v", i, err)
		}
	}

	assert.Equal(t, models.AgentStatusOffline, agent.Status(),
		"agent must end Offline regardless of which Stop won")

	// The stop channel must be closed exactly once (a double close would
	// have panicked before this point) and the agent must be restartable.
	require.NoError(t, agent.Start(context.Background()))
	assert.Equal(t, models.AgentStatusReady, agent.Status(),
		"Start after concurrent Stop must rebuild a fresh stop channel")
	require.NoError(t, agent.Stop(context.Background()))
}

// TestSubAgent_ConcurrentStop_RepeatStress hammers Start/Stop cycles with
// overlapping Stoppers to catch interleavings a single pass can miss; it
// relies on -race and the process surviving to be meaningful.
func TestSubAgent_ConcurrentStop_RepeatStress(t *testing.T) {
	executor := newStubExecutor()

	for round := 0; round < 20; round++ {
		agent := New(fmt.Sprintf("sub-stop-stress-%d", round), models.AgentTypeTop, executor, nil)
		require.NoError(t, agent.Start(context.Background()))

		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = agent.Stop(context.Background())
			}()
		}
		wg.Wait()

		assert.Equal(t, models.AgentStatusOffline, agent.Status())
	}
}
