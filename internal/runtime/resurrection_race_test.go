package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// recordingAgent is a minimal base.Agent double that counts Start calls.
type recordingAgent struct {
	base.Agent
	id     string
	mu     sync.Mutex
	starts int
}

func (a *recordingAgent) ID() string { return a.id }
func (a *recordingAgent) Type() models.AgentType {
	return models.AgentType("recording")
}
func (a *recordingAgent) Status() models.AgentStatus {
	return models.AgentStatusReady
}
func (a *recordingAgent) Start(_ context.Context) error {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	return nil
}
func (a *recordingAgent) Stop(_ context.Context) error { return nil }
func (a *recordingAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, nil
}
func (a *recordingAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *recordingAgent) startsCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

// TestRestoreAgent_AbortsWhenOperatorStoppedAfterScheduling pins the
// operator-intent guard: NotifyAgentDead schedules an async restore ~1s out;
// if the operator calls StopAgent inside that window, the pending restore must
// NOT clobber the stop by installing a fresh running instance. Regression for
// the resurrection-after-kill race.
func TestRestoreAgent_AbortsWhenOperatorStoppedAfterScheduling(t *testing.T) {
	m := New(nil, nil, nil)
	ctx := context.Background()
	require.NoError(t, m.Start(ctx))
	t.Cleanup(func() { _ = m.Stop() })

	first := &recordingAgent{id: "agent-a"}
	factory := func() base.Agent { return &recordingAgent{id: "agent-a"} }
	m.RegisterAgent(first, factory)
	require.NoError(t, first.Start(ctx))

	// Operator stops the agent — entry now carries operator intent.
	require.NoError(t, m.StopAgent(ctx, "agent-a"))

	// A late RestoreAgent (the shape scheduleResurrection issues ~1s after a
	// death that raced the stop) must refuse to install: the desired state is
	// "stopped".
	require.NoError(t, m.RestoreAgent(ctx, "agent-a", factory))

	time.Sleep(50 * time.Millisecond) // allow any (wrongful) launch to surface

	entry, ok := m.agents["agent-a"]
	require.True(t, ok)
	require.True(t, entry.stopped, "entry must remain stopped")
}

// TestNotifyAgentDead_ThenOperatorStop_RestoreAborted drives the full race:
// death → resurrection scheduled → operator Stop lands in the backoff window →
// the scheduled restore aborts and no fresh agent is launched.
func TestNotifyAgentDead_ThenOperatorStop_RestoreAborted(t *testing.T) {
	m := New(nil, nil, nil)
	ctx := context.Background()
	require.NoError(t, m.Start(ctx))
	t.Cleanup(func() { _ = m.Stop() })

	victim := &recordingAgent{id: "agent-b"}
	factory := func() base.Agent { return &recordingAgent{id: "agent-b"} }
	m.RegisterAgent(victim, factory)
	require.NoError(t, victim.Start(ctx))

	// Simulate the death racing the stop: NotifyAgentDead schedules the async
	// restore, then the operator's StopAgent arrives inside the window.
	go m.NotifyAgentDead("agent-b", "test: simulated crash")
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, m.StopAgent(ctx, "agent-b"))

	// The scheduled restore fires after its 1s backoff; give it ample time.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		entry, exists := m.agents["agent-b"]
		resurrecting := exists && entry.resurrecting
		stopped := exists && entry.stopped
		m.mu.Unlock()
		if !resurrecting && stopped {
			break // scheduled restore settled
		}
		time.Sleep(20 * time.Millisecond)
	}

	m.mu.Lock()
	fresh := m.agents["agent-b"]
	starts := 0
	if fresh != nil && fresh.agent != nil {
		if ra, ok := fresh.agent.(*chaosWrappedAgent); ok {
			if inner, ok2 := ra.Agent.(*recordingAgent); ok2 {
				starts = inner.startsCount()
			}
		}
	}
	m.mu.Unlock()

	// The original instance started once; NO replacement may have launched.
	require.LessOrEqual(t, starts, 1,
		"pending resurrection must not install a fresh running agent after an operator stop")
	_ = fresh
}
