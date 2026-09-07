package sdk

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	ares_bootstrap "github.com/Timwood0x10/ares/internal/ares_bootstrap"
	ares_events "github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/knowledge/adapter"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	aresexp "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
)

// akgBridgeTimeout caps how long a single AKG DistillConversation call may
// run. Best-effort: the trigger never blocks the subscriber loop longer than
// this, and the bounded goroutine is tracked by the errgroup so Close waits
// for it.
const akgBridgeTimeout = 30 * time.Second

// newEventBackend creates the Runtime lifecycle context, errgroup, and in-memory
// event store, and — when distillSvc or akgBridge is non-nil — registers a
// background subscriber that distills TaskCompleted/TaskFailed events into
// long-term experiences (distillSvc) and/or AKG KnowledgeObjects (akgBridge).
// Bundling these together keeps New() under the 100-line limit: the context,
// errgroup, store, and subscriber are all facets of the same
// "event/lifecycle backend" owned by the Runtime.
//
// The returned context is cancelled by Runtime.Close to stop the subscriber
// goroutine cleanly. The errgroup lets Close wait for in-flight distillation
// work before releasing other resources.
//
// Args:
//
//	distillSvc - distillation service; nil skips the experience-distillation path.
//	akgBridge  - AKG DistillBridge; nil skips the KnowledgeObject-distillation path.
//	             When both are nil the subscriber is not started (store is still returned).
//
// Returns:
//
//	ctx    - lifecycle context for background goroutines; cancelled by the returned cancel.
//	cancel - cancels ctx.
//	eg     - errgroup tracking the subscriber goroutine for clean shutdown.
//	store  - the in-memory event store shared by emitters (Agent.Run) and the subscriber.
func newEventBackend(
	distillSvc *aresexp.DistillationService,
	akgBridge *adapter.DistillBridge,
) (
	context.Context, context.CancelFunc, *errgroup.Group, ares_events.EventStore,
) {
	ctx, cancel := context.WithCancel(context.Background())
	eg := &errgroup.Group{}
	store := ares_events.NewMemoryEventStore()
	if distillSvc != nil || akgBridge != nil {
		wireDistillationSubscriber(ctx, eg, store, distillSvc, akgBridge)
	}
	return ctx, cancel, eg, store
}

// wireSDKEventBackend selects the Runtime's event backend. Stage 8: when the
// Bootstrap core is available, distillation subscribes to Bootstrap's shared
// EventStore (single store across entry points) instead of a private SDK
// store; otherwise the SDK falls back to newEventBackend. Extracted from New()
// to keep the constructor under the 100-line limit.
func wireSDKEventBackend(
	bootstrapComp *ares_bootstrap.Components,
	distillSvc *aresexp.DistillationService,
	akgBridge *adapter.DistillBridge,
) (
	context.Context, context.CancelFunc, *errgroup.Group, ares_events.EventStore,
) {
	if bootstrapComp != nil && bootstrapComp.EventStore != nil {
		rtCtx, rtCancel := context.WithCancel(context.Background())
		eg := &errgroup.Group{}
		if distillSvc != nil || akgBridge != nil {
			wireDistillationSubscriber(rtCtx, eg, bootstrapComp.EventStore, distillSvc, akgBridge)
		}
		return rtCtx, rtCancel, eg, bootstrapComp.EventStore
	}
	return newEventBackend(distillSvc, akgBridge)
}

// wireDistillationSubscriber registers a background consumer of TaskCompleted
// and TaskFailed events. For each event it:
//   - feeds the completed task into the DistillationService (distSvc) so
//     conversations are distilled into long-term experiences, when distSvc is non-nil;
//   - triggers the AKG DistillBridge (akgBridge) so conversations are distilled
//     into AKG KnowledgeObjects and persisted through the quality gate, when
//     akgBridge is non-nil.
//
// The goroutine runs under the Runtime's errgroup and exits when ctx is
// cancelled (typically in Runtime.Close) or when the event store closes the
// subscription channel. Errors during distillation are logged and do not stop
// the subscriber, so a single bad event cannot starve the distillation loop.
//
// Subscribe failures are non-fatal: a warning is logged and no subscriber is
// registered, leaving the Runtime running without event-driven distillation.
//
// Args:
//
//	ctx        - lifecycle context; cancellation stops the subscriber.
//	eg         - errgroup tracking the subscriber goroutine for clean shutdown.
//	store      - the EventStore to subscribe to; must be non-nil.
//	distSvc    - the distillation service that consumes each event; may be nil.
//	akgBridge  - the AKG DistillBridge; may be nil.
func wireDistillationSubscriber(
	ctx context.Context,
	eg *errgroup.Group,
	store ares_events.EventStore,
	distSvc *aresexp.DistillationService,
	akgBridge *adapter.DistillBridge,
) {
	// EventFilter.Types restricts the subscription to the two lifecycle events
	// the distillation loop cares about. Confirmed against
	// internal/ares_events/types.go (EventFilter.Types []EventType).
	filter := ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventTaskCompleted,
			ares_events.EventTaskFailed,
		},
	}
	ch, err := store.Subscribe(ctx, filter)
	if err != nil {
		slog.Warn("sdk: distillation subscriber failed to subscribe; event-driven distillation disabled",
			"error", err)
		return
	}
	eg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return nil
			case ev, ok := <-ch:
				if !ok {
					// Store closed the channel (ctx cancellation also closes it
					// via MemoryEventStore.unsubscribe); exit cleanly.
					return nil
				}
				if distSvc != nil {
					ares_bootstrap.HandleTaskCompletedForDistillation(ctx, distSvc, ev)
				}
				if akgBridge != nil {
					triggerAKGBridge(ctx, eg, ev, akgBridge)
				}
			}
		}
	})
	slog.Info("sdk: event-driven distillation subscriber started",
		"distill_svc", distSvc != nil, "akg_bridge", akgBridge != nil)
}

// triggerAKGBridge extracts task/result text from a task-lifecycle event and
// feeds it to the AKG DistillBridge as a user→assistant conversation. The call
// runs in a bounded goroutine under the errgroup with a 30s timeout so it never
// blocks the subscriber loop. Errors are logged and never returned (best-effort):
// the bridge runs alongside the experience DistillationService and a failure
// here does not affect the main distillation path.
//
// The content guards mirror HandleTaskCompletedForDistillation: events whose
// payload lacks a tenant_id or sufficient task/result text are skipped, since
// the distiller cannot produce meaningful memories from them.
func triggerAKGBridge(
	ctx context.Context,
	eg *errgroup.Group,
	ev *ares_events.Event,
	bridge *adapter.DistillBridge,
) {
	p := ev.Payload
	taskText := eventStringField(p, ares_events.EventKeyTask)
	resultText := eventStringField(p, ares_events.EventKeyResult)
	tenantID := eventStringField(p, ares_events.EventKeyTenantID)
	agentID := eventStringField(p, "agent_id")

	if tenantID == "" || len(taskText) < 10 || len(resultText) < 20 {
		return
	}

	messages := []distillation.Message{
		{Role: roleUser, Content: taskText},
		{Role: roleAssistant, Content: resultText},
	}

	eg.Go(func() error {
		bridgeCtx, cancel := context.WithTimeout(ctx, akgBridgeTimeout)
		defer cancel()
		if _, err := bridge.DistillConversation(bridgeCtx, ev.ID, messages, tenantID, agentID); err != nil {
			slog.Warn("sdk: AKG distill bridge trigger failed",
				"event_id", ev.ID, "error", err)
		}
		return nil
	})
}

// eventStringField returns the first non-empty string value among the given
// keys in the payload map. Mirrors ares_bootstrap.stringField, which is not
// exported.
func eventStringField(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
