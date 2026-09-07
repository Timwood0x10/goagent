package runtime

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// ObserverPlugin subscribes to workflow lifecycle ares_events and writes them
// to an EventStore for persistence and observability.
type ObserverPlugin struct {
	name   string
	store  ares_events.EventStore
	cancel context.CancelFunc
	eg     errgroup.Group
}

// NewObserverPlugin creates an ObserverPlugin that writes ares_events to the
// given EventStore.
func NewObserverPlugin(name string, store ares_events.EventStore) *ObserverPlugin {
	if name == "" {
		name = "observer"
	}
	return &ObserverPlugin{
		name:  name,
		store: store,
	}
}

// Name returns the plugin name.
func (p *ObserverPlugin) Name() string {
	return p.name
}

// Capabilities returns the capabilities this plugin provides.
func (p *ObserverPlugin) Capabilities() []Capability {
	return []Capability{CapObserver}
}

// Start subscribes to workflow ares_events and begins writing them to the store.
// The plugin manages its own lifecycle context via Stop().
func (p *ObserverPlugin) Start(ctx context.Context, bus EventBus) error {
	// Derive an independent context — the ctx passed to Start is a timeout
	// context from invokeWithTimeout that is cancelled immediately after
	// Start returns. Using it for the background loop would kill it on return.
	loopCtx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	ch, err := bus.Subscribe(loopCtx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			EventWorkflowStarted,
			EventWorkflowCompleted,
			EventWorkflowFailed,
			EventStepStarted,
			EventStepCompleted,
			EventStepFailed,
			EventCheckpointSaved,
		},
	})
	if err != nil {
		cancel()
		return fmt.Errorf("observer: subscribe: %w", err)
	}

	p.eg.Go(func() error {
		p.loop(loopCtx, ch)
		return nil
	})
	return nil
}

// Stop cancels the internal loop context, causing the subscription to be
// cleaned up and the background goroutine to exit.
func (p *ObserverPlugin) Stop(_ context.Context) error {
	if p.cancel != nil {
		p.cancel()
	}
	return p.eg.Wait()
}

func (p *ObserverPlugin) loop(ctx context.Context, ch <-chan *ares_events.Event) {
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if err := p.store.Append(ctx, evt.StreamID, []*ares_events.Event{evt}, 0); err != nil {
				log.Warn("observer: append event failed",
					"stream_id", evt.StreamID,
					"type", evt.Type,
					"error", err,
				)
			}
		case <-ctx.Done():
			return
		}
	}
}
