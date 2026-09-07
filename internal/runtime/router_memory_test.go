package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockMemoryPlugin struct {
	name     string
	adviceFn func(ctx context.Context, state RouteState) ([]RouteAdvice, error)
}

func (m *mockMemoryPlugin) Name() string                              { return m.name }
func (m *mockMemoryPlugin) Capabilities() []Capability                { return []Capability{CapMemory} }
func (m *mockMemoryPlugin) Start(_ context.Context, _ EventBus) error { return nil }
func (m *mockMemoryPlugin) Stop(_ context.Context) error              { return nil }
func (m *mockMemoryPlugin) AdviseRoute(ctx context.Context, state RouteState) ([]RouteAdvice, error) {
	if m.adviceFn != nil {
		return m.adviceFn(ctx, state)
	}
	return nil, nil
}

func TestMemoryRouter_Route(t *testing.T) {
	t.Run("returns memory advice when confident", func(t *testing.T) {
		bus := NewPluginBus()
		mem := &mockMemoryPlugin{
			name: "test-memory",
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				return []RouteAdvice{
					{NextStepID: "step-2", Confidence: 0.8, Reason: "similar past execution"},
				}, nil
			},
		}
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(NewMemoryRouter("mr", nil, 0.5)))
		require.NoError(t, bus.Start(context.Background()))

		router, ok := bus.PluginsByCap(CapRouter)[0].(RouterPlugin)
		require.True(t, ok)

		dec, err := router.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		require.NoError(t, err)
		require.NotNil(t, dec)
		assert.Equal(t, "step-2", dec.NextStepID)
		assert.Equal(t, "memory", dec.Source)
	})

	t.Run("falls back to expression rules when memory confidence too low", func(t *testing.T) {
		bus := NewPluginBus()
		mem := &mockMemoryPlugin{
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				return []RouteAdvice{
					{NextStepID: "step-2", Confidence: 0.3, Reason: "low confidence"},
				}, nil
			},
		}
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(NewMemoryRouter("mr", []RouteRule{
			{FromStepID: "step-1", ToStepID: "step-3", Reason: "default path"},
		}, 0.5)))
		require.NoError(t, bus.Start(context.Background()))

		router, ok := bus.PluginsByCap(CapRouter)[0].(RouterPlugin)
		require.True(t, ok)

		dec, err := router.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		require.NoError(t, err)
		require.NotNil(t, dec)
		assert.Equal(t, "step-3", dec.NextStepID)
		assert.Equal(t, "expression", dec.Source)
	})

	t.Run("returns nil when no memory plugin available", func(t *testing.T) {
		bus := NewPluginBus()
		require.NoError(t, bus.Register(NewMemoryRouter("mr", nil, 0.5)))
		require.NoError(t, bus.Start(context.Background()))

		router, ok := bus.PluginsByCap(CapRouter)[0].(RouterPlugin)
		require.True(t, ok)

		dec, err := router.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		// No memory advice and no expression rule → (nil, nil) per contract.
		require.NoError(t, err)
		assert.Nil(t, dec)
	})

	t.Run("handles empty advice slice", func(t *testing.T) {
		bus := NewPluginBus()
		mem := &mockMemoryPlugin{
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				return nil, nil
			},
		}
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(NewMemoryRouter("mr", nil, 0.5)))
		require.NoError(t, bus.Start(context.Background()))

		router, ok := bus.PluginsByCap(CapRouter)[0].(RouterPlugin)
		require.True(t, ok)

		dec, err := router.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		// Empty advice + no expression rule → (nil, nil) per contract.
		require.NoError(t, err)
		assert.Nil(t, dec)
	})

	t.Run("uses pre-fetched advice from BeforeStep", func(t *testing.T) {
		bus := NewPluginBus()
		var callCount atomic.Int32
		mem := &mockMemoryPlugin{
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				callCount.Add(1)
				return []RouteAdvice{
					{NextStepID: "step-2", Confidence: 0.9, Reason: "pre-fetched"},
				}, nil
			},
		}
		mr := NewMemoryRouter("mr", nil, 0.5)
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(mr))
		require.NoError(t, bus.Start(context.Background()))

		// BeforeStep starts a goroutine to pre-fetch advice for step-1
		require.NoError(t, mr.BeforeStep(context.Background(), "exec-1", &Step{ID: "step-1"}))

		// Wait for the async goroutine to complete
		require.Eventually(t, func() bool {
			return callCount.Load() == 1
		}, time.Second, 10*time.Millisecond, "pre-fetch goroutine should complete")

		// Route should use pre-fetched advice without calling AdviseRoute again
		dec, err := mr.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		require.NoError(t, err)
		require.NotNil(t, dec)
		assert.Equal(t, "step-2", dec.NextStepID)
		assert.Equal(t, "memory", dec.Source)
		assert.Equal(t, int32(1), callCount.Load(), "AdviseRoute should NOT be called again during Route")
	})

	t.Run("falls through to sync query when no pre-fetched advice", func(t *testing.T) {
		bus := NewPluginBus()
		mem := &mockMemoryPlugin{
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				return []RouteAdvice{
					{NextStepID: "step-3", Confidence: 0.9, Reason: "sync query"},
				}, nil
			},
		}
		mr := NewMemoryRouter("mr", nil, 0.5)
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(mr))
		require.NoError(t, bus.Start(context.Background()))

		// No BeforeStep called — should fall through to synchronous query
		dec, err := mr.Route(context.Background(), RouteState{CurrentStepID: "step-1"})
		require.NoError(t, err)
		require.NotNil(t, dec)
		assert.Equal(t, "step-3", dec.NextStepID)
		assert.Equal(t, "memory", dec.Source)
	})

	t.Run("pre-fetched advice for wrong step is ignored", func(t *testing.T) {
		bus := NewPluginBus()
		mem := &mockMemoryPlugin{
			adviceFn: func(_ context.Context, _ RouteState) ([]RouteAdvice, error) {
				return []RouteAdvice{
					{NextStepID: "step-4", Confidence: 0.9, Reason: "sync"},
				}, nil
			},
		}
		mr := NewMemoryRouter("mr", nil, 0.5)
		require.NoError(t, bus.Register(mem))
		require.NoError(t, bus.Register(mr))
		require.NoError(t, bus.Start(context.Background()))

		// Pre-fetch for step-1
		require.NoError(t, mr.BeforeStep(context.Background(), "exec-1", &Step{ID: "step-1"}))

		// Route called for step-2 — pre-fetched step-1 advice should be ignored
		dec, err := mr.Route(context.Background(), RouteState{CurrentStepID: "step-2"})
		require.NoError(t, err)
		require.NotNil(t, dec)
		assert.Equal(t, "step-4", dec.NextStepID)
	})
}
