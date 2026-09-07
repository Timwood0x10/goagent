package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/agents/base"
)

func TestRuntimeRegisterAgent(t *testing.T) {
	mgr := New(nil, nil, nil)
	agent := newMockAgent("reg-test")
	mgr.RegisterAgent(agent, func() base.Agent { return newMockAgent("reg-test") })
}

func TestRuntimeStartAndStop(t *testing.T) {
	mgr := New(nil, nil, nil)
	assert.NoError(t, mgr.Start(context.Background()))
	assert.NoError(t, mgr.Stop())
}

func TestRuntimeStopAgent(t *testing.T) {
	mgr := New(nil, nil, nil)
	agent := newMockAgent("stop-test")
	mgr.RegisterAgent(agent, func() base.Agent { return newMockAgent("stop-test") })
	assert.NoError(t, mgr.Start(context.Background()))
	assert.NoError(t, mgr.StopAgent(context.Background(), "stop-test"))
	assert.NoError(t, mgr.Stop())
}

func TestRuntimeGetAgent(t *testing.T) {
	mgr := New(nil, nil, nil)
	agent := newMockAgent("get-test")
	mgr.RegisterAgent(agent, func() base.Agent { return newMockAgent("get-test") })
	assert.NoError(t, mgr.Start(context.Background()))
	defer func() { _ = mgr.Stop() }()

	got := mgr.GetAgent("get-test")
	assert.NotNil(t, got)
	assert.Equal(t, "get-test", got.ID())
}

func TestRuntimeGetAgentNotFound(t *testing.T) {
	mgr := New(nil, nil, nil)
	got := mgr.GetAgent("nonexistent")
	assert.Nil(t, got)
}

func TestRuntimeStatsInitial(t *testing.T) {
	mgr := New(nil, nil, nil)
	stats := mgr.Stats()
	assert.NotNil(t, stats)
}

func TestRuntimeDoubleStart(t *testing.T) {
	mgr := New(nil, nil, nil)
	assert.NoError(t, mgr.Start(context.Background()))
	err := mgr.Start(context.Background())
	assert.Error(t, err)
	assert.NoError(t, mgr.Stop())
}
