package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRouter struct {
	name        string
	routeFn     func(ctx context.Context, state RouteState) (*RouteDecision, error)
	startCalled bool
	stopCalled  bool
}

func (m *mockRouter) Name() string                              { return m.name }
func (m *mockRouter) Capabilities() []Capability                { return []Capability{CapRouter} }
func (m *mockRouter) Start(_ context.Context, _ EventBus) error { m.startCalled = true; return nil }
func (m *mockRouter) Stop(_ context.Context) error              { m.stopCalled = true; return nil }
func (m *mockRouter) Route(ctx context.Context, state RouteState) (*RouteDecision, error) {
	if m.routeFn == nil {
		return nil, errors.New("not implemented in mock")
	}
	return m.routeFn(ctx, state)
}

func TestFallbackRouter_New_Empty(t *testing.T) {
	_, err := NewFallbackRouter("test", nil)
	assert.Error(t, err)

	_, err = NewFallbackRouter("test", []RouterPlugin{})
	assert.Error(t, err)
}

func TestFallbackRouter_FirstRouterSucceeds(t *testing.T) {
	r1 := &mockRouter{
		name: "r1",
		routeFn: func(_ context.Context, _ RouteState) (*RouteDecision, error) {
			return &RouteDecision{
				NextStepID: "step-b",
				Reason:     "r1 decision",
				Source:     "expression",
			}, nil
		},
	}
	r2 := &mockRouter{name: "r2"}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	dec, err := fb.Route(context.Background(), RouteState{CurrentStepID: "step-a"})
	require.NoError(t, err)
	require.NotNil(t, dec)
	assert.Equal(t, "step-b", dec.NextStepID)
	assert.Equal(t, "r1 decision", dec.Reason)
}

func TestFallbackRouter_FirstReturnsNil_SecondSucceeds(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	r2 := &mockRouter{
		name: "r2",
		routeFn: func(_ context.Context, _ RouteState) (*RouteDecision, error) {
			return &RouteDecision{
				NextStepID: "step-c",
				Reason:     "r2 decision",
				Source:     "memory",
			}, nil
		},
	}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	dec, err := fb.Route(context.Background(), RouteState{CurrentStepID: "step-a"})
	require.NoError(t, err)
	require.NotNil(t, dec)
	assert.Equal(t, "step-c", dec.NextStepID)
	assert.Equal(t, "r2 decision", dec.Reason)
	assert.Equal(t, "memory", dec.Source)
}

func TestFallbackRouter_AllReturnNil(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	r2 := &mockRouter{name: "r2"}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	dec, err := fb.Route(context.Background(), RouteState{CurrentStepID: "step-a"})
	require.NoError(t, err)
	require.NotNil(t, dec)
	assert.Empty(t, dec.NextStepID)
	assert.Equal(t, "fallback", dec.Source)
	assert.Contains(t, dec.Reason, "no router produced")
}

func TestFallbackRouter_FirstErrors_SecondSucceeds(t *testing.T) {
	r1 := &mockRouter{
		name: "r1",
		routeFn: func(_ context.Context, _ RouteState) (*RouteDecision, error) {
			return nil, errors.New("r1 exploded")
		},
	}
	r2 := &mockRouter{
		name: "r2",
		routeFn: func(_ context.Context, _ RouteState) (*RouteDecision, error) {
			return &RouteDecision{NextStepID: "step-d", Reason: "r2 ok", Source: "expression"}, nil
		},
	}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	dec, err := fb.Route(context.Background(), RouteState{CurrentStepID: "step-a"})
	require.NoError(t, err)
	require.NotNil(t, dec)
	assert.Equal(t, "step-d", dec.NextStepID)
}

func TestFallbackRouter_Start(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	r2 := &mockRouter{name: "r2"}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	err = fb.Start(context.Background(), nil)
	require.NoError(t, err)
	assert.True(t, r1.startCalled)
	assert.True(t, r2.startCalled)
}

func TestFallbackRouter_Stop(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	r2 := &mockRouter{name: "r2"}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	err = fb.Stop(context.Background())
	require.NoError(t, err)
	assert.True(t, r1.stopCalled)
	assert.True(t, r2.stopCalled)
}

func TestFallbackRouter_Routers(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	r2 := &mockRouter{name: "r2"}

	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1, r2})
	require.NoError(t, err)

	routers := fb.Routers()
	assert.Len(t, routers, 2)
	assert.Equal(t, "r1", routers[0].Name())
	assert.Equal(t, "r2", routers[1].Name())
}

func TestFallbackRouter_Capabilities(t *testing.T) {
	r1 := &mockRouter{name: "r1"}
	fb, err := NewFallbackRouter("fb", []RouterPlugin{r1})
	require.NoError(t, err)
	assert.Equal(t, []Capability{CapRouter}, fb.Capabilities())
}

func TestFallbackRouter_DefaultName(t *testing.T) {
	fb, err := NewFallbackRouter("", []RouterPlugin{&mockRouter{name: "r1"}})
	require.NoError(t, err)
	assert.Equal(t, "fallback-router", fb.Name())
}
