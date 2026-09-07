package arena

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

// mockEventStore implements EventStore for testing.
type mockEventStore struct {
	mu       sync.Mutex
	appended []*ares_events.Event
	version  int64
}

func (m *mockEventStore) Append(_ context.Context, _ string, evts []*ares_events.Event, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appended = append(m.appended, evts...)
	m.version += int64(len(evts))
	return nil
}

func (m *mockEventStore) StreamVersion(_ context.Context, _ string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.version == 0 {
		return 0, ares_events.ErrStreamNotFound
	}
	return m.version, nil
}

func (m *mockEventStore) Subscribe(_ context.Context, _ ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	ch := make(chan *ares_events.Event, 10)
	return ch, nil
}

func newTestService(rt RuntimeProvider, dag DAGProvider, store EventStore) *Service {
	inj := NewInjector(rt, dag)
	return NewService(inj, store, nil)
}

func TestExecute_KillAgent(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "test-1",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Empty(t, result.Error)
	assert.Equal(t, []string{"agent-1"}, rt.getStopped())
}

func TestExecute_KillLeader(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "leader-1", Type: "leader"},
			}
		},
	}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "test-2",
		Type:      ActionKillLeader,
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"leader-1"}, rt.getStopped())
	assert.Equal(t, "leader-1", result.Action.Metadata["killed_leader_id"])
}

func TestExecute_RemoveNode(t *testing.T) {
	dag := &mockDAG{}
	svc := newTestService(nil, dag, nil)

	action := Action{
		ID:        "test-3",
		Type:      ActionRemoveNode,
		TargetID:  "node-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"node-1"}, dag.getRemovedNodes())
}

func TestExecute_RemoveEdge(t *testing.T) {
	dag := &mockDAG{}
	svc := newTestService(nil, dag, nil)

	action := Action{
		ID:        "test-4",
		Type:      ActionRemoveEdge,
		TargetID:  "b",
		SourceID:  "a",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	edges := dag.getRemovedEdges()
	require.Len(t, edges, 1)
	assert.Equal(t, [2]string{"a", "b"}, edges[0])
}

func TestExecute_UnknownType(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	action := Action{
		ID:        "test-5",
		Type:      ActionType("unknown"),
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "unknown action type")
}

func TestExecute_RecordsFailure(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, _ string) error {
			return errors.New("agent crashed")
		},
	}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "test-6",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "agent crashed")
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestExecute_EmitsEvent(t *testing.T) {
	store := &mockEventStore{}
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, store)

	action := Action{
		ID:        "test-7",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	svc.Execute(context.Background(), action)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.appended, 1)
	assert.Equal(t, ares_events.EventType("arena.action.executed"), store.appended[0].Type)
	assert.Equal(t, "arena", store.appended[0].StreamID)
	assert.Equal(t, "test-7", store.appended[0].Payload["action_id"])
}

func TestExecute_EmitsFailedEvent(t *testing.T) {
	store := &mockEventStore{}
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, _ string) error {
			return errors.New("boom")
		},
	}
	svc := newTestService(rt, nil, store)

	action := Action{
		ID:        "test-8",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	svc.Execute(context.Background(), action)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.appended, 1)
	assert.Equal(t, ares_events.EventType("arena.action.failed"), store.appended[0].Type)
}

func TestHistory(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	// Execute two actions.
	svc.Execute(context.Background(), Action{
		ID: "h-1", Type: ActionKillAgent, TargetID: "a-1", CreatedAt: time.Now(),
	})
	svc.Execute(context.Background(), Action{
		ID: "h-2", Type: ActionKillAgent, TargetID: "a-2", CreatedAt: time.Now(),
	})

	history := svc.History()
	assert.Len(t, history, 2)
	assert.Equal(t, "h-1", history[0].Action.ID)
	assert.Equal(t, "h-2", history[1].Action.ID)
}

func TestStats_Aggregation(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			if id == "fail" {
				return errors.New("fail")
			}
			return nil
		},
	}
	svc := newTestService(rt, nil, nil)

	svc.Execute(context.Background(), Action{
		ID: "s-1", Type: ActionKillAgent, TargetID: "ok", CreatedAt: time.Now(),
	})
	svc.Execute(context.Background(), Action{
		ID: "s-2", Type: ActionKillAgent, TargetID: "fail", CreatedAt: time.Now(),
	})
	svc.Execute(context.Background(), Action{
		ID: "s-3", Type: ActionKillAgent, TargetID: "ok", CreatedAt: time.Now(),
	})

	stats := svc.Stats()
	assert.Equal(t, 3, stats.TotalActions)
	assert.Equal(t, 2, stats.SuccessfulActions)
	assert.Equal(t, 1, stats.FailedActions)
	assert.True(t, stats.LastAction.After(time.Time{}))
}

func TestReset(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	svc.Execute(context.Background(), Action{
		ID: "r-1", Type: ActionKillAgent, TargetID: "a-1", CreatedAt: time.Now(),
	})

	assert.Len(t, svc.History(), 1)
	assert.Equal(t, 1, svc.Stats().TotalActions)

	svc.Reset()

	assert.Empty(t, svc.History())
	assert.Equal(t, 0, svc.Stats().TotalActions)
	assert.Equal(t, 0, svc.Stats().SuccessfulActions)
	assert.Equal(t, 0, svc.Stats().FailedActions)
}

func TestConcurrentExecute(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			svc.Execute(context.Background(), Action{
				ID:        fmt.Sprintf("c-%d", idx),
				Type:      ActionKillAgent,
				TargetID:  fmt.Sprintf("agent-%d", idx),
				CreatedAt: time.Now(),
			})
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 50, svc.Stats().TotalActions)
	assert.Equal(t, 50, len(svc.History()))
}

func TestSubscribe_NilStore(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	ch, err := svc.Subscribe(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "event store not configured")
	assert.Nil(t, ch)
}

func TestSubscribe_WithStore(t *testing.T) {
	store := &mockEventStore{}
	svc := newTestService(nil, nil, store)

	ch, err := svc.Subscribe(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestExecute_ToolTimeout(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "tt-1",
		Type:      ActionToolTimeout,
		TargetID:  "agent-1",
		Metadata:  map[string]any{"duration": "5s"},
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"agent-1"}, rt.getToolTimedOut())
}

func TestExecute_MemoryCorrupt(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "mc-1",
		Type:      ActionMemoryCorrupt,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"agent-1"}, rt.getMemoryCorrupted())
}

func TestExecute_MCPDisconnect(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "md-1",
		Type:      ActionMCPDisconnect,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"agent-1"}, rt.getMCPDisconnected())
}

func TestExecute_LLMFailure(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	action := Action{
		ID:        "lf-1",
		Type:      ActionLLMFailure,
		TargetID:  "agent-1",
		Metadata:  map[string]any{"error_type": "rate_limit"},
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)
	failed := rt.getLLMFailed()
	require.Len(t, failed, 1)
	assert.Equal(t, "agent-1", failed[0].id)
	assert.Equal(t, "rate_limit", failed[0].errType)
}
