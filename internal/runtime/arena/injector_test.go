package arena

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

// mockRuntime implements RuntimeProvider for testing.
type mockRuntime struct {
	mu              sync.Mutex
	stopAgentFn     func(ctx context.Context, agentID string) error
	listAgentsFn    func() []ares_runtime.AgentInfo
	pauseAgentFn    func(ctx context.Context, agentID string) error
	resumeAgentFn   func(ctx context.Context, agentID string) error
	slowAgentFn     func(ctx context.Context, agentID string, delay time.Duration) error
	partitionNetFn  func(ctx context.Context, agentID string) error
	toolTimeoutFn   func(ctx context.Context, agentID string, timeout time.Duration) error
	corruptMemFn    func(ctx context.Context, agentID string) error
	disconnectMCPFn func(ctx context.Context, agentID string) error
	injectLLMFailFn func(ctx context.Context, agentID string, errType string) error
	stopped         []string
	paused          []string
	resumed         []string
	slowed          []string
	partitioned     []string
	toolTimedOut    []string
	memoryCorrupted []string
	mcpDisconnected []string
	llmFailed       []struct {
		id      string
		errType string
	}
}

func (m *mockRuntime) StopAgent(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.stopped = append(m.stopped, agentID)
	m.mu.Unlock()
	if m.stopAgentFn != nil {
		return m.stopAgentFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) getStopped() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.stopped))
	copy(out, m.stopped)
	return out
}

func (m *mockRuntime) PauseAgent(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.paused = append(m.paused, agentID)
	m.mu.Unlock()
	if m.pauseAgentFn != nil {
		return m.pauseAgentFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) ResumeAgent(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.resumed = append(m.resumed, agentID)
	m.mu.Unlock()
	if m.resumeAgentFn != nil {
		return m.resumeAgentFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) SlowAgent(ctx context.Context, agentID string, delay time.Duration) error {
	m.mu.Lock()
	m.slowed = append(m.slowed, agentID)
	m.mu.Unlock()
	if m.slowAgentFn != nil {
		return m.slowAgentFn(ctx, agentID, delay)
	}
	return nil
}

func (m *mockRuntime) PartitionNetwork(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.partitioned = append(m.partitioned, agentID)
	m.mu.Unlock()
	if m.partitionNetFn != nil {
		return m.partitionNetFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) ToolTimeout(ctx context.Context, agentID string, timeout time.Duration) error {
	m.mu.Lock()
	m.toolTimedOut = append(m.toolTimedOut, agentID)
	m.mu.Unlock()
	if m.toolTimeoutFn != nil {
		return m.toolTimeoutFn(ctx, agentID, timeout)
	}
	return nil
}

func (m *mockRuntime) CorruptMemory(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.memoryCorrupted = append(m.memoryCorrupted, agentID)
	m.mu.Unlock()
	if m.corruptMemFn != nil {
		return m.corruptMemFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) DisconnectMCP(ctx context.Context, agentID string) error {
	m.mu.Lock()
	m.mcpDisconnected = append(m.mcpDisconnected, agentID)
	m.mu.Unlock()
	if m.disconnectMCPFn != nil {
		return m.disconnectMCPFn(ctx, agentID)
	}
	return nil
}

func (m *mockRuntime) InjectLLMFailure(ctx context.Context, agentID string, errType string) error {
	m.mu.Lock()
	m.llmFailed = append(m.llmFailed, struct {
		id      string
		errType string
	}{id: agentID, errType: errType})
	m.mu.Unlock()
	if m.injectLLMFailFn != nil {
		return m.injectLLMFailFn(ctx, agentID, errType)
	}
	return nil
}

func (m *mockRuntime) ListAgents() []ares_runtime.AgentInfo {
	if m.listAgentsFn != nil {
		return m.listAgentsFn()
	}
	return nil
}

func (m *mockRuntime) getToolTimedOut() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.toolTimedOut))
	copy(out, m.toolTimedOut)
	return out
}

func (m *mockRuntime) getMemoryCorrupted() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.memoryCorrupted))
	copy(out, m.memoryCorrupted)
	return out
}

func (m *mockRuntime) getMCPDisconnected() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.mcpDisconnected))
	copy(out, m.mcpDisconnected)
	return out
}

func (m *mockRuntime) getLLMFailed() []struct {
	id      string
	errType string
} {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]struct {
		id      string
		errType string
	}, len(m.llmFailed))
	copy(out, m.llmFailed)
	return out
}

// mockDAG implements DAGProvider for testing.
type mockDAG struct {
	mu           sync.Mutex
	listNodesFn  func(ctx context.Context) []string
	listEdgesFn  func(ctx context.Context) [][2]string
	removeNodeFn func(ctx context.Context, id string) error
	removeEdgeFn func(ctx context.Context, from, to string) error
	removedNodes []string
	removedEdges [][2]string
	nodes        []string
	edges        [][2]string
}

func (m *mockDAG) ListNodes(ctx context.Context) []string {
	if m.listNodesFn != nil {
		return m.listNodesFn(ctx)
	}
	if len(m.nodes) > 0 {
		return m.nodes
	}
	// Default: return node IDs matching agent IDs from the runtime mock.
	return []string{"node-1", "node-2"}
}

func (m *mockDAG) ListEdges(ctx context.Context) [][2]string {
	if m.listEdgesFn != nil {
		return m.listEdgesFn(ctx)
	}
	if len(m.edges) > 0 {
		return m.edges
	}
	return [][2]string{{"node-1", "node-2"}}
}

func (m *mockDAG) RemoveNode(ctx context.Context, id string) error {
	m.mu.Lock()
	m.removedNodes = append(m.removedNodes, id)
	m.mu.Unlock()
	if m.removeNodeFn != nil {
		return m.removeNodeFn(ctx, id)
	}
	return nil
}

func (m *mockDAG) RemoveEdge(ctx context.Context, from, to string) error {
	m.mu.Lock()
	m.removedEdges = append(m.removedEdges, [2]string{from, to})
	m.mu.Unlock()
	if m.removeEdgeFn != nil {
		return m.removeEdgeFn(ctx, from, to)
	}
	return nil
}

func (m *mockDAG) getRemovedNodes() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.removedNodes))
	copy(out, m.removedNodes)
	return out
}

func (m *mockDAG) getRemovedEdges() [][2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][2]string, len(m.removedEdges))
	copy(out, m.removedEdges)
	return out
}

func TestKillAgent_Success(t *testing.T) {
	rt := &mockRuntime{}
	inj := NewInjector(rt, nil)

	err := inj.KillAgent(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, rt.getStopped())
}

func TestKillAgent_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.KillAgent(context.Background(), "agent-1")
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestKillAgent_RuntimeError(t *testing.T) {
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, _ string) error {
			return errors.New("stop failed")
		},
	}
	inj := NewInjector(rt, nil)

	err := inj.KillAgent(context.Background(), "agent-1")
	assert.ErrorContains(t, err, "stop failed")
}

func TestKillLeader_Success(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "worker-1", Type: "sub"},
				{ID: "leader-1", Type: "leader"},
				{ID: "worker-2", Type: "sub"},
			}
		},
	}
	inj := NewInjector(rt, nil)

	leaderID, err := inj.KillLeader(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "leader-1", leaderID)
	assert.Equal(t, []string{"leader-1"}, rt.getStopped())
}

func TestKillLeader_NotFound(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "worker-1", Type: "sub"},
			}
		},
	}
	inj := NewInjector(rt, nil)

	_, err := inj.KillLeader(context.Background())
	assert.ErrorIs(t, err, ErrLeaderNotFound)
}

func TestKillLeader_EmptyList(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return nil
		},
	}
	inj := NewInjector(rt, nil)

	_, err := inj.KillLeader(context.Background())
	assert.ErrorIs(t, err, ErrLeaderNotFound)
}

func TestKillLeader_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	_, err := inj.KillLeader(context.Background())
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestRemoveNode_Success(t *testing.T) {
	dag := &mockDAG{}
	inj := NewInjector(nil, dag)

	err := inj.RemoveNode(context.Background(), "node-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1"}, dag.getRemovedNodes())
}

func TestRemoveNode_NilDAG(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.RemoveNode(context.Background(), "node-1")
	assert.ErrorIs(t, err, ErrDAGNil)
}

func TestRemoveNode_DAGError(t *testing.T) {
	dag := &mockDAG{
		removeNodeFn: func(_ context.Context, _ string) error {
			return errors.New("node has dependents")
		},
	}
	inj := NewInjector(nil, dag)

	err := inj.RemoveNode(context.Background(), "node-1")
	assert.ErrorContains(t, err, "node has dependents")
}

func TestRemoveEdge_Success(t *testing.T) {
	dag := &mockDAG{}
	inj := NewInjector(nil, dag)

	err := inj.RemoveEdge(context.Background(), "a", "b")
	require.NoError(t, err)
	edges := dag.getRemovedEdges()
	require.Len(t, edges, 1)
	assert.Equal(t, [2]string{"a", "b"}, edges[0])
}

func TestRemoveEdge_NilDAG(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.RemoveEdge(context.Background(), "a", "b")
	assert.ErrorIs(t, err, ErrDAGNil)
}

func TestRemoveEdge_DAGError(t *testing.T) {
	dag := &mockDAG{
		removeEdgeFn: func(_ context.Context, _, _ string) error {
			return errors.New("edge not found")
		},
	}
	inj := NewInjector(nil, dag)

	err := inj.RemoveEdge(context.Background(), "a", "b")
	assert.ErrorContains(t, err, "edge not found")
}

func TestNewInjector_NilDeps(t *testing.T) {
	inj := NewInjector(nil, nil)
	assert.NotNil(t, inj)
	assert.Nil(t, inj.ares_runtime)
	assert.Nil(t, inj.dag)
}

func TestNewInjector_WithDeps(t *testing.T) {
	rt := &mockRuntime{}
	dag := &mockDAG{}
	inj := NewInjector(rt, dag)
	assert.NotNil(t, inj)
	assert.NotNil(t, inj.ares_runtime)
	assert.NotNil(t, inj.dag)
}

func TestToolTimeout_Success(t *testing.T) {
	rt := &mockRuntime{}
	inj := NewInjector(rt, nil)

	err := inj.ToolTimeout(context.Background(), "agent-1", 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, rt.getToolTimedOut())
}

func TestToolTimeout_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.ToolTimeout(context.Background(), "agent-1", 5*time.Second)
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestToolTimeout_RuntimeError(t *testing.T) {
	rt := &mockRuntime{
		toolTimeoutFn: func(_ context.Context, _ string, _ time.Duration) error {
			return errors.New("timeout injection failed")
		},
	}
	inj := NewInjector(rt, nil)

	err := inj.ToolTimeout(context.Background(), "agent-1", 5*time.Second)
	assert.ErrorContains(t, err, "timeout injection failed")
}

func TestCorruptMemory_Success(t *testing.T) {
	rt := &mockRuntime{}
	inj := NewInjector(rt, nil)

	err := inj.CorruptMemory(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, rt.getMemoryCorrupted())
}

func TestCorruptMemory_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.CorruptMemory(context.Background(), "agent-1")
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestCorruptMemory_RuntimeError(t *testing.T) {
	rt := &mockRuntime{
		corruptMemFn: func(_ context.Context, _ string) error {
			return errors.New("corruption failed")
		},
	}
	inj := NewInjector(rt, nil)

	err := inj.CorruptMemory(context.Background(), "agent-1")
	assert.ErrorContains(t, err, "corruption failed")
}

func TestDisconnectMCP_Success(t *testing.T) {
	rt := &mockRuntime{}
	inj := NewInjector(rt, nil)

	err := inj.DisconnectMCP(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-1"}, rt.getMCPDisconnected())
}

func TestDisconnectMCP_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.DisconnectMCP(context.Background(), "agent-1")
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestDisconnectMCP_RuntimeError(t *testing.T) {
	rt := &mockRuntime{
		disconnectMCPFn: func(_ context.Context, _ string) error {
			return errors.New("disconnect failed")
		},
	}
	inj := NewInjector(rt, nil)

	err := inj.DisconnectMCP(context.Background(), "agent-1")
	assert.ErrorContains(t, err, "disconnect failed")
}

func TestInjectLLMFailure_Success(t *testing.T) {
	rt := &mockRuntime{}
	inj := NewInjector(rt, nil)

	err := inj.InjectLLMFailure(context.Background(), "agent-1", "rate_limit")
	require.NoError(t, err)
	failed := rt.getLLMFailed()
	require.Len(t, failed, 1)
	assert.Equal(t, "agent-1", failed[0].id)
	assert.Equal(t, "rate_limit", failed[0].errType)
}

func TestInjectLLMFailure_NilRuntime(t *testing.T) {
	inj := NewInjector(nil, nil)

	err := inj.InjectLLMFailure(context.Background(), "agent-1", "rate_limit")
	assert.ErrorIs(t, err, ErrRuntimeNil)
}

func TestInjectLLMFailure_RuntimeError(t *testing.T) {
	rt := &mockRuntime{
		injectLLMFailFn: func(_ context.Context, _ string, _ string) error {
			return errors.New("llm failure injection failed")
		},
	}
	inj := NewInjector(rt, nil)

	err := inj.InjectLLMFailure(context.Background(), "agent-1", "rate_limit")
	assert.ErrorContains(t, err, "llm failure injection failed")
}
