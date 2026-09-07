// nolint: errcheck // Test code may ignore return values
package ahp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeartbeatMonitor_RegisterCallback_Nil verifies that registering a nil
// callback is a no-op and does not panic.
func TestHeartbeatMonitor_RegisterCallback_Nil(t *testing.T) {
	mon := NewHeartbeatMonitor(DefaultHeartbeatConfig())

	// Registering nil should be a no-op.
	mon.RegisterCallback(nil)

	assert.Len(t, mon.callbacks, 0,
		"nil callback should not be appended")
}

// TestHeartbeatMonitor_RegisterCallback_Valid verifies that a valid callback
// is stored and retrievable.
func TestHeartbeatMonitor_RegisterCallback_Valid(t *testing.T) {
	mon := NewHeartbeatMonitor(DefaultHeartbeatConfig())

	called := false
	mon.RegisterCallback(func(agentID string) {
		called = true
	})

	require.Len(t, mon.callbacks, 1,
		"expected exactly one registered callback")

	// Manually invoke to verify it works.
	mon.callbacks[0]("agent-1")
	assert.True(t, called, "callback should have been invoked")
}

// TestHeartbeatMonitor_CheckTimeouts_InvokesCallback verifies that
// CheckTimeouts invokes callbacks when an agent is newly marked offline.
func TestHeartbeatMonitor_CheckTimeouts_InvokesCallback(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond, // Very short timeout.
		MaxMissed: 1,                    // One miss is enough.
	}
	mon := NewHeartbeatMonitor(cfg)

	var callbackAgentID string
	mon.RegisterCallback(func(agentID string) {
		callbackAgentID = agentID
	})

	// Record a heartbeat so the agent is known.
	mon.RecordHeartbeat("agent-1")

	// Wait long enough for the heartbeat to expire.
	time.Sleep(10 * time.Millisecond)

	timedOut := mon.CheckTimeouts()

	assert.Contains(t, timedOut, "agent-1",
		"agent-1 should be reported as timed out")
	assert.Equal(t, "agent-1", callbackAgentID,
		"callback should have been invoked with the correct agent ID")
}

// TestHeartbeatMonitor_CheckTimeouts_NoCallbackForAlreadyOffline verifies that
// agents already marked offline do not trigger callbacks again.
func TestHeartbeatMonitor_CheckTimeouts_NoCallbackForAlreadyOffline(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 1,
	}
	mon := NewHeartbeatMonitor(cfg)

	var callCount int32
	mon.RegisterCallback(func(agentID string) {
		atomic.AddInt32(&callCount, 1)
	})

	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	// First call marks the agent offline and invokes callback.
	timedOut1 := mon.CheckTimeouts()
	assert.Len(t, timedOut1, 1, "first call should time out agent-1")

	// Second call should NOT invoke callback for already-offline agent.
	timedOut2 := mon.CheckTimeouts()
	assert.Empty(t, timedOut2, "second call should not time out already-offline agent")

	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount),
		"callback should have been invoked exactly once")
}

// TestHeartbeatMonitor_CheckTimeouts_MultipleCallbacks verifies that all
// registered callbacks are invoked when an agent times out.
func TestHeartbeatMonitor_CheckTimeouts_MultipleCallbacks(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 1,
	}
	mon := NewHeartbeatMonitor(cfg)

	var mu sync.Mutex
	callbackIDs := make([]int, 0, 3)

	for i := 0; i < 3; i++ {
		id := i
		mon.RegisterCallback(func(agentID string) {
			mu.Lock()
			callbackIDs = append(callbackIDs, id)
			mu.Unlock()
		})
	}

	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	timedOut := mon.CheckTimeouts()
	assert.Len(t, timedOut, 1)

	mu.Lock()
	assert.Len(t, callbackIDs, 3,
		"all three callbacks should have been invoked")
	mu.Unlock()
}

// TestHeartbeatMonitor_CallbackInvokedOutsideLock verifies that callbacks are
// invoked without holding the monitor lock. The callback itself calls
// GetStatus which acquires the read lock -- if callbacks ran under the write
// lock this would deadlock.
func TestHeartbeatMonitor_CallbackInvokedOutsideLock(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 1,
	}
	mon := NewHeartbeatMonitor(cfg)

	done := make(chan struct{})
	mon.RegisterCallback(func(agentID string) {
		// This call acquires the read lock. If the callback were invoked
		// under the write lock, this would deadlock.
		_, _ = mon.GetStatus(agentID)
		close(done)
	})

	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	mon.CheckTimeouts()

	select {
	case <-done:
		// Success: callback completed without deadlock.
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock detected: callback did not complete within timeout")
	}
}

// TestHeartbeatMonitor_ConcurrentRegisterAndCheck verifies that concurrent
// RegisterCallback and CheckTimeouts calls do not race or panic.
func TestHeartbeatMonitor_ConcurrentRegisterAndCheck(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 1,
	}
	mon := NewHeartbeatMonitor(cfg)

	var wg sync.WaitGroup

	// Register callbacks concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mon.RegisterCallback(func(agentID string) {})
		}()
	}

	// Record heartbeats and check timeouts concurrently.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mon.RecordHeartbeat("agent-" + string(rune('A'+id)))
		}(i)
	}

	wg.Wait()
	time.Sleep(10 * time.Millisecond)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mon.CheckTimeouts()
		}()
	}

	wg.Wait()
}

// TestHeartbeatMonitor_CheckTimeouts_EmptyMonitor verifies that checking
// timeouts on a monitor with no agents does not panic.
func TestHeartbeatMonitor_CheckTimeouts_EmptyMonitor(t *testing.T) {
	mon := NewHeartbeatMonitor(DefaultHeartbeatConfig())

	mon.RegisterCallback(func(agentID string) {
		t.Errorf("callback should not be invoked for empty monitor")
	})

	timedOut := mon.CheckTimeouts()
	assert.Empty(t, timedOut,
		"no agents should time out from an empty monitor")
}

// TestHeartbeatMonitor_CheckTimeouts_MissedCountIncremental verifies the
// incremental missed-count mechanism: agents must miss MaxMissed times
// before being marked offline.
func TestHeartbeatMonitor_CheckTimeouts_MissedCountIncremental(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 3, // Must miss 3 times.
	}
	mon := NewHeartbeatMonitor(cfg)

	var callCount int32
	mon.RegisterCallback(func(agentID string) {
		atomic.AddInt32(&callCount, 1)
	})

	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	// First check: MissedCount becomes 1, not yet offline.
	timedOut := mon.CheckTimeouts()
	assert.Empty(t, timedOut, "agent should not be offline after 1 miss")
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount))

	// Second check: MissedCount becomes 2, not yet offline.
	timedOut = mon.CheckTimeouts()
	assert.Empty(t, timedOut, "agent should not be offline after 2 misses")
	assert.Equal(t, int32(0), atomic.LoadInt32(&callCount))

	// Third check: MissedCount reaches 3, agent goes offline.
	timedOut = mon.CheckTimeouts()
	assert.Contains(t, timedOut, "agent-1",
		"agent should be offline after 3 misses")
	assert.Equal(t, int32(1), atomic.LoadInt32(&callCount))
}

// TestHeartbeatMonitor_RecordHeartbeat_ResetsMissedCount verifies that
// recording a heartbeat resets the missed count.
func TestHeartbeatMonitor_RecordHeartbeat_ResetsMissedCount(t *testing.T) {
	cfg := &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   1 * time.Millisecond,
		MaxMissed: 3,
	}
	mon := NewHeartbeatMonitor(cfg)

	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	// Two misses.
	mon.CheckTimeouts()
	mon.CheckTimeouts()

	// Record a fresh heartbeat -- resets missed count.
	mon.RecordHeartbeat("agent-1")
	time.Sleep(10 * time.Millisecond)

	// One more miss: should be 1, not 3.
	timedOut := mon.CheckTimeouts()
	assert.Empty(t, timedOut,
		"agent should not be offline because heartbeat reset the counter")
}
