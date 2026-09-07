package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// chaosTestManager builds a started Manager with an in-memory event store and
// registers a mock agent guarded by a factory call counter. The returned
// cleanup cancels the runtime context.
func chaosTestManager(t *testing.T) (*Manager, *ares_events.MemoryEventStore, *mockAgent, *atomic.Int32) {
	t.Helper()
	store := ares_events.NewMemoryEventStore()
	m := New(nil, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	var factoryCalls atomic.Int32
	m.RegisterAgent(agent, func() base.Agent {
		factoryCalls.Add(1)
		return newMockAgent("a1")
	})
	require.NoError(t, m.StartAgent(ctx, agent))
	waitUntil(t, func() bool { return agent.started.Load() == 1 })
	return m, store, agent, &factoryCalls
}

// waitUntil polls cond until it is true or the timeout elapses. Polling with
// a deadline is used instead of time.Sleep-based synchronization (code rule 7.3).
func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestPauseAgent_NotFound(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)
	ctx := context.Background()
	assert.ErrorIs(t, m.PauseAgent(ctx, "nonexistent"), ErrAgentNotFound)
}

func TestResumeAgent_NotFound(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)
	ctx := context.Background()
	assert.ErrorIs(t, m.ResumeAgent(ctx, "nonexistent"), ErrAgentNotFound)
}

// TestPauseAgent_SuspendsWithoutStoppedFlag verifies pause keeps the same
// managedAgent entry: paused=true is set but the permanent stopped flag is
// NOT, so the agent remains resumable and distinguishable from StopAgent.
func TestPauseAgent_SuspendsWithoutStoppedFlag(t *testing.T) {
	m, _, agent, _ := chaosTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.PauseAgent(ctx, "a1"))
	waitUntil(t, func() bool { return agent.stopped.Load() == 1 })

	m.mu.RLock()
	ma, ok := m.agents["a1"]
	m.mu.RUnlock()
	require.True(t, ok, "managed agent entry must survive pause")
	assert.True(t, ma.paused, "paused flag must be set")
	assert.False(t, ma.stopped, "permanent stopped flag must NOT be set by pause")

	info, ok := m.GetAgentInfo("a1")
	require.True(t, ok)
	assert.True(t, info.Paused, "AgentInfo must expose paused state")
}

// TestPauseAgent_EmitsStoppedEvent verifies pause emits EventAgentStopped with
// a distinguishable "pause" reason, reusing the canonical lifecycle event type.
func TestPauseAgent_EmitsStoppedEvent(t *testing.T) {
	m, store, _, _ := chaosTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.PauseAgent(ctx, "a1"))

	evts := readStreamEvents(t, store, "a1")
	stopped := lastEventOfType(evts, ares_events.EventAgentStopped)
	require.NotNil(t, stopped, "pause must emit EventAgentStopped")
	assert.Equal(t, "pause", stopped.Payload[FieldReason])
}

// TestResumeAgent_RelaunchesSameInstance is the core contract: resume must
// re-run Start on the SAME in-memory agent instance — no factory call, no
// restart counter increment, no state loss.
func TestResumeAgent_RelaunchesSameInstance(t *testing.T) {
	m, _, agent, factoryCalls := chaosTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.PauseAgent(ctx, "a1"))
	waitUntil(t, func() bool { return agent.stopped.Load() == 1 })

	require.NoError(t, m.ResumeAgent(ctx, "a1"))
	// Same instance: Start must be invoked a second time.
	waitUntil(t, func() bool { return agent.started.Load() == 2 })

	assert.Equal(t, int32(0), factoryCalls.Load(),
		"resume must not rebuild via factory (state must be preserved)")
	assert.Zero(t, m.Stats().TotalRestarts,
		"resume must not count as a restart")

	m.mu.RLock()
	ma, ok := m.agents["a1"]
	m.mu.RUnlock()
	require.True(t, ok)
	assert.False(t, ma.paused, "paused flag must be cleared after resume")
	assert.Equal(t, agent, chaosUnwrap(ma.agent),
		"the same agent instance must be relaunched (unwrapped from chaos wrapper)")
}

// TestResumeAgent_NotPaused_NoOp verifies resume on a running (not paused)
// agent is a safe no-op: it must not restart or rebuild the agent.
func TestResumeAgent_NotPaused_NoOp(t *testing.T) {
	m, _, agent, factoryCalls := chaosTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.ResumeAgent(ctx, "a1"))
	assert.Equal(t, int32(1), agent.started.Load(),
		"resume of a running agent must not call Start again")
	assert.Equal(t, int32(0), factoryCalls.Load())
	assert.Zero(t, m.Stats().TotalRestarts)
}

// TestResumeAgent_EmitsStartedEvent verifies resume emits EventAgentStarted
// with a distinguishable "resume" reason.
func TestResumeAgent_EmitsStartedEvent(t *testing.T) {
	m, store, _, _ := chaosTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.PauseAgent(ctx, "a1"))
	require.NoError(t, m.ResumeAgent(ctx, "a1"))

	evts := readStreamEvents(t, store, "a1")
	started := lastEventOfType(evts, ares_events.EventAgentStarted)
	require.NotNil(t, started, "resume must emit EventAgentStarted")
	assert.Equal(t, "resume", started.Payload[FieldReason])
}

// TestPausedAgent_NotResurrected verifies NotifyAgentDead skips paused agents:
// an unexpected death while paused must not trigger the factory-based restore.
func TestPausedAgent_NotResurrected(t *testing.T) {
	m, _, _, factoryCalls := chaosTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.PauseAgent(ctx, "a1"))
	waitUntil(t, func() bool {
		info, ok := m.GetAgentInfo("a1")
		return ok && info.Paused
	})

	m.NotifyAgentDead("a1", "test: paused agent reported dead")
	// The resurrection path is skipped synchronously for paused agents; the
	// bounded poll proves no async restore was scheduled.
	assertStaysZero(t, 100*time.Millisecond, factoryCalls)
}

// assertStaysZero fails if v becomes non-zero within the duration. Used for
// negative assertions (something must NOT happen) with a bounded deadline
// instead of a bare time.Sleep (code rule 7.3).
func assertStaysZero(t *testing.T, d time.Duration, v *atomic.Int32) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if v.Load() != 0 {
			t.Fatalf("expected counter to stay 0, got %d", v.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readStreamEvents returns all events for a stream.
func readStreamEvents(t *testing.T, store *ares_events.MemoryEventStore, streamID string) []*ares_events.Event {
	t.Helper()
	evts, err := store.Read(context.Background(), streamID, ares_events.ReadOptions{})
	require.NoError(t, err)
	return evts
}

// lastEventOfType returns the most recent event of the given type, or nil.
func lastEventOfType(evts []*ares_events.Event, typ ares_events.EventType) *ares_events.Event {
	for i := len(evts) - 1; i >= 0; i-- {
		if evts[i].Type == typ {
			return evts[i]
		}
	}
	return nil
}

// TestChaosFaultInjections verifies the four fault-injection methods actually
// take effect at the Process boundary: after injection, Process returns a
// fault error instead of delegating to the wrapped agent. Without injection
// the wrapper delegates unchanged (mock Process returns its own error).
func TestChaosFaultInjections(t *testing.T) {
	tests := []struct {
		name        string
		inject      func(m *Manager, ctx context.Context) error
		wantErrText string
	}{
		{
			name:        "network_partition",
			inject:      func(m *Manager, ctx context.Context) error { return m.PartitionNetwork(ctx, "a1") },
			wantErrText: "network partition injected",
		},
		{
			name:        "memory_corrupt",
			inject:      func(m *Manager, ctx context.Context) error { return m.CorruptMemory(ctx, "a1") },
			wantErrText: "memory corruption injected",
		},
		{
			name:        "mcp_disconnect",
			inject:      func(m *Manager, ctx context.Context) error { return m.DisconnectMCP(ctx, "a1") },
			wantErrText: "MCP disconnected injected",
		},
		{
			name: "llm_failure",
			inject: func(m *Manager, ctx context.Context) error {
				return m.InjectLLMFailure(ctx, "a1", "rate_limit")
			},
			wantErrText: "LLM failure (rate_limit) injected",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, _, _ := chaosTestManager(t)
			ctx := context.Background()

			// Before injection the wrapper delegates to the raw mock agent
			// (its Process returns "not implemented in mock").
			m.mu.RLock()
			wrapped := m.agents["a1"].agent
			m.mu.RUnlock()
			_, err := wrapped.Process(ctx, "input")
			require.ErrorContains(t, err, "not implemented in mock",
				"pre-injection Process must delegate to the wrapped agent")

			require.NoError(t, tt.inject(m, ctx))

			_, err = wrapped.Process(ctx, "input")
			require.ErrorContains(t, err, tt.wantErrText,
				"post-injection Process must surface the injected fault")
		})
	}
}

// TestChaosFaultInjections_ProcessStream verifies the injected fault also
// fails ProcessStream with a closed channel, per its contract.
func TestChaosFaultInjections_ProcessStream(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.PartitionNetwork(ctx, "a1"))

	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()

	ch, err := wrapped.ProcessStream(ctx, "input")
	require.ErrorContains(t, err, "network partition injected")
	_, ok := <-ch
	assert.False(t, ok, "channel must be closed when a fault is injected")
}

// TestChaosUnwrapRoundTrip verifies chaosUnwrap returns the raw agent from a
// wrapped instance and passes through unwrapped instances unchanged, so
// optional-interface assertions (StatefulAgent, Heartbeater) keep working.
func TestChaosUnwrapRoundTrip(t *testing.T) {
	agent := newMockAgent("u1")
	wrapped := &chaosWrappedAgent{Agent: agent, id: "u1"}
	assert.Same(t, agent, chaosUnwrap(wrapped), "unwrap must return the raw agent")
	assert.Same(t, agent, chaosUnwrap(agent), "unwrap of a plain agent is a no-op")
	assert.Nil(t, chaosUnwrap(nil), "unwrap of nil is nil")
}

// TestSlowAgent_InjectsDelay verifies SlowAgent actually slows down the agent's
// Process call. Previously slowDelay was written into the agent context but
// never read (dead code), so the injection had no effect. Now it is read at the
// Process boundary and the delay is observed without restarting the agent.
func TestSlowAgent_InjectsDelay(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)
	ctx := context.Background()

	// Baseline: without injection, Process returns the mock error quickly.
	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()
	baseStart := time.Now()
	_, err := wrapped.Process(ctx, "input")
	baseElapsed := time.Since(baseStart)
	require.ErrorContains(t, err, "not implemented in mock")
	assert.Less(t, baseElapsed, 20*time.Millisecond,
		"baseline Process without slow injection must be fast")

	// Inject a 60ms delay and call Process again WITHOUT restarting the agent.
	// The delay must take effect on this next execution.
	const slowDelay = 60 * time.Millisecond
	require.NoError(t, m.SlowAgent(ctx, "a1", slowDelay))

	slowStart := time.Now()
	_, err = wrapped.Process(ctx, "input")
	slowElapsed := time.Since(slowStart)
	require.ErrorContains(t, err, "not implemented in mock",
		"slow injection must still delegate to the wrapped agent (mock error)")
	// Lower-bound assertion: the delay must be observed. A small tolerance
	// accounts for timer coarseness without making the test flaky.
	assert.GreaterOrEqual(t, slowElapsed, slowDelay-10*time.Millisecond,
		"SlowAgent must inject a measurable delay >= configured duration")
	assert.Greater(t, slowElapsed, baseElapsed+slowDelay/2,
		"slow Process must be noticeably slower than the baseline")
}

// TestSlowAgent_RespectsContextCancellation verifies the injected slow delay
// is ctx-aware: a cancelled context aborts the wait immediately instead of
// blocking for the full duration (a bare time.Sleep would ignore cancellation).
func TestSlowAgent_RespectsContextCancellation(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)

	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()

	require.NoError(t, m.SlowAgent(context.Background(), "a1", 5*time.Second))

	// Pre-cancelled context: chaosWait must return ctx.Err() at once.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := wrapped.Process(ctx, "input")
	elapsed := time.Since(start)
	assert.ErrorIs(t, err, context.Canceled,
		"slow wait must abort on a cancelled context")
	assert.Less(t, elapsed, 100*time.Millisecond,
		"slow wait must not block when the context is already cancelled")
}

// TestSlowAgent_ProcessStreamDelay verifies SlowAgent also delays ProcessStream
// before opening the stream.
func TestSlowAgent_ProcessStreamDelay(t *testing.T) {
	m, _, _, _ := chaosTestManager(t)
	ctx := context.Background()

	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()

	const slowDelay = 50 * time.Millisecond
	require.NoError(t, m.SlowAgent(ctx, "a1", slowDelay))

	start := time.Now()
	ch, err := wrapped.ProcessStream(ctx, "input")
	elapsed := time.Since(start)
	// The mock ProcessStream returns a closed channel and nil error; the
	// observable effect of the injection is the elapsed time before the call.
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, slowDelay-10*time.Millisecond,
		"SlowAgent must delay ProcessStream by the configured duration")
	// Drain the closed channel to confirm it still closes cleanly.
	_, ok := <-ch
	assert.False(t, ok, "stream channel must be closed")
}

// blockingProcessAgent embeds mockAgent and overrides Process/ProcessStream to
// block until ctx is cancelled. This lets a per-call ToolTimeout be observed as
// a DeadlineExceeded error without restarting the agent, and lets us prove the
// timeout never cancels the agent's lifecycle context.
type blockingProcessAgent struct {
	*mockAgent
}

func newBlockingProcessAgent(id string) *blockingProcessAgent {
	return &blockingProcessAgent{mockAgent: newMockAgent(id)}
}

func (a *blockingProcessAgent) Process(ctx context.Context, _ any) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (a *blockingProcessAgent) ProcessStream(ctx context.Context, _ any) (<-chan base.AgentEvent, error) {
	ch := make(chan base.AgentEvent)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// chaosBlockingManager builds a started Manager with a blockingProcessAgent so
// ToolTimeout's per-execution deadline can be observed.
func chaosBlockingManager(t *testing.T) (*Manager, *blockingProcessAgent, *atomic.Int32) {
	t.Helper()
	store := ares_events.NewMemoryEventStore()
	m := New(nil, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, m.Start(ctx))

	agent := newBlockingProcessAgent("a1")
	var factoryCalls atomic.Int32
	m.RegisterAgent(agent, func() base.Agent {
		factoryCalls.Add(1)
		return newBlockingProcessAgent("a1")
	})
	require.NoError(t, m.StartAgent(ctx, agent))
	waitUntil(t, func() bool { return agent.started.Load() == 1 })
	return m, agent, &factoryCalls
}

// TestToolTimeout_AppliesWithoutRestart verifies ToolTimeout takes effect on the
// next Process call WITHOUT restarting the agent, and that the per-call deadline
// does NOT cancel the agent's lifecycle context (Start goroutine stays alive).
// Previously ToolTimeout was only read at agent (re)start time and applied to
// the whole agent context, so it could not be changed without a restart and
// could kill healthy agent goroutines.
func TestToolTimeout_AppliesWithoutRestart(t *testing.T) {
	m, agent, factoryCalls := chaosBlockingManager(t)
	ctx := context.Background()

	// Inject a short tool timeout AFTER start — must take effect on the next
	// Process call without a restart.
	const timeout = 40 * time.Millisecond
	require.NoError(t, m.ToolTimeout(ctx, "a1", timeout))

	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()

	start := time.Now()
	_, err := wrapped.Process(ctx, "input")
	elapsed := time.Since(start)

	// The per-call deadline must cancel the Process call.
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"ToolTimeout must deadline the Process call")
	assert.GreaterOrEqual(t, elapsed, timeout-15*time.Millisecond,
		"Process must be bounded by the ToolTimeout")
	assert.Less(t, elapsed, timeout+500*time.Millisecond,
		"Process must return shortly after the deadline")

	// The agent lifecycle must NOT be affected: no restart, still running.
	assert.Equal(t, int32(0), factoryCalls.Load(),
		"ToolTimeout must not restart the agent")
	assert.Equal(t, int32(1), agent.started.Load(),
		"agent Start must not be re-invoked by the tool timeout")
	assert.Equal(t, models.AgentStatusReady, agent.Status(),
		"agent must still be alive after the per-call tool timeout")
}

// TestToolTimeout_DoesNotKillAgentLifecycle verifies a short ToolTimeout never
// cancels the agent's lifecycle context even when set before any Process call:
// the agent stays alive past the deadline, proving the timeout is scoped to
// per-execution and not the whole agent context (the old bug).
func TestToolTimeout_DoesNotKillAgentLifecycle(t *testing.T) {
	m, agent, factoryCalls := chaosBlockingManager(t)
	ctx := context.Background()

	// A 20ms tool timeout must NOT cancel the long-running agent goroutine.
	require.NoError(t, m.ToolTimeout(ctx, "a1", 20*time.Millisecond))

	// Poll past the deadline: the agent must remain alive the whole time.
	deadline := time.Now().Add(120 * time.Millisecond)
	for time.Now().Before(deadline) {
		assert.Equal(t, models.AgentStatusReady, agent.Status(),
			"agent must stay alive past the tool timeout")
		assert.Equal(t, int32(1), agent.started.Load(),
			"agent must not be restarted by the tool timeout")
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, int32(0), factoryCalls.Load(),
		"no factory resurrection must be triggered")
}

// TestToolTimeout_ProcessStreamDeadline verifies the per-call ToolTimeout also
// bounds a streaming execution: the relayed channel closes within a bounded
// time after the deadline, and the agent lifecycle is unaffected.
func TestToolTimeout_ProcessStreamDeadline(t *testing.T) {
	m, agent, factoryCalls := chaosBlockingManager(t)
	ctx := context.Background()

	const timeout = 40 * time.Millisecond
	require.NoError(t, m.ToolTimeout(ctx, "a1", timeout))

	m.mu.RLock()
	wrapped := m.agents["a1"].agent
	m.mu.RUnlock()

	start := time.Now()
	ch, err := wrapped.ProcessStream(ctx, "input")
	require.NoError(t, err, "ProcessStream must open the relay channel")

	// The relay must close once the per-call deadline fires.
	_, ok := <-ch
	elapsed := time.Since(start)
	assert.False(t, ok, "relay channel must close after the tool timeout")
	assert.GreaterOrEqual(t, elapsed, timeout-15*time.Millisecond,
		"stream must stay open until the deadline")
	assert.Less(t, elapsed, timeout+500*time.Millisecond,
		"stream must close shortly after the deadline")

	// Agent lifecycle unaffected.
	assert.Equal(t, int32(1), agent.started.Load(),
		"agent must still be alive after the stream timeout")
	assert.Equal(t, int32(0), factoryCalls.Load(),
		"ToolTimeout must not restart the agent")
}
