// observer.go provides the RuntimeObserver — the OBSERVE stage of the
// evolution control plane. It subscribes to task-completed/failed and
// agent-stopped events from the EventStore, converts the outcome-bearing
// ones into normalized [0,1] StrategySample values, and writes them to the
// EvidenceStore as KindFitness evidence (source="strategy") so the GA
// scorer and deployment staging can read real runtime fitness (B6 fix).
//
// The lifecycle consumes these samples indirectly: its rollback watch loop
// reads the aggregator Window over the same evidence (B1 fix). That path is
// window-mean based (min-samples gated), which is deliberately preferred
// over a direct per-sample feed.
//
// The observer is deliberately passive: it never decides to promote or
// rollback. It only collects and forwards. Agent code is unaware that its
// execution outcomes are being scored here.
package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/evidence"
)

// StrategySample is one runtime execution observation of the currently active
// strategy. All values are normalized to [0,1] so they are dimensionally
// consistent with RollbackPolicy thresholds (B1 fix).
type StrategySample struct {
	// StrategyID is the ID of the active strategy when the sample was taken.
	StrategyID string

	// Success indicates whether the task completed successfully.
	Success bool

	// Score is the normalized fitness score in [0,1].
	Score float64

	// TaskType categorizes the observed task (e.g. "chat", "workflow").
	TaskType string

	// At is when the sample was recorded.
	At time.Time
}

// TODO(tech-debt): the design doc (ga-runtime-evolution-design-zh.md §4 ①)
// gives StrategySample Latency and CostUSD fields (fed by flight-trace
// cost/latency). Neither is populated today: task events carry no duration
// or cost keys (see e.g. agentloop.Engine.emitTaskCompleted's payload), so
// the fields were removed rather than left permanently zero. Reintroduce
// them together with the aggregator's cost/latency penalty term once those
// values reach the EventStore payloads.

// RuntimeObserver subscribes to the EventStore for task and agent lifecycle
// events, converts them into StrategySample values, and writes each sample
// to the evidence store (source="strategy"). It is the sole producer of
// runtime fitness samples — agent code never calls it directly.
type RuntimeObserver struct {
	subscriber EventStoreSubscriber
	evStore    evidence.Store
	activeID   func() string
	mu         sync.Mutex
	cancel     context.CancelFunc
	eg         *errgroupAdapter
}

// errgroupAdapter is a minimal managed-goroutine wrapper so the observer
// does not import golang.org/x/sync/errgroup directly into its public API
// (it already transitively uses it via the scheduler package).
type errgroupAdapter struct {
	done chan struct{}
}

func newErrgroupAdapter() *errgroupAdapter {
	return &errgroupAdapter{done: make(chan struct{})}
}

func (e *errgroupAdapter) Wait() {
	<-e.done
}

// ObserverOption configures a RuntimeObserver.
type ObserverOption func(*RuntimeObserver)

// WithObserverEvidenceStore sets the evidence store so the observer writes
// KindFitness evidence (source="strategy") for each sample.
func WithObserverEvidenceStore(store evidence.Store) ObserverOption {
	return func(o *RuntimeObserver) {
		o.evStore = store
	}
}

// WithObserverActiveIDFunc sets a function that returns the currently active
// strategy ID. When nil, the observer uses the strategy ID from the event
// payload or falls back to "unknown".
func WithObserverActiveIDFunc(fn func() string) ObserverOption {
	return func(o *RuntimeObserver) {
		o.activeID = fn
	}
}

// NewRuntimeObserver creates an observer that subscribes to the given
// EventStoreSubscriber. The observer does not start until Start is called.
func NewRuntimeObserver(subscriber EventStoreSubscriber, opts ...ObserverOption) *RuntimeObserver {
	o := &RuntimeObserver{subscriber: subscriber}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Start subscribes to task lifecycle events and begins collecting samples.
// It is idempotent: calling Start twice is a no-op. The subscription runs
// until ctx is cancelled or Stop is called.
func (o *RuntimeObserver) Start(ctx context.Context) error {
	if o.subscriber == nil {
		return nil
	}
	o.mu.Lock()
	if o.cancel != nil {
		o.mu.Unlock()
		return nil
	}
	subCtx, cancel := context.WithCancel(ctx)
	ch, err := o.subscriber.Subscribe(subCtx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventTaskCompleted,
			ares_events.EventTaskFailed,
			ares_events.EventAgentStopped,
		},
	})
	if err != nil {
		cancel()
		o.mu.Unlock()
		return err
	}
	eg := newErrgroupAdapter()
	o.cancel = cancel
	o.eg = eg
	o.mu.Unlock()

	go func() {
		// K3: production background goroutines must not die silently or take
		// the process down on a bug — recover, log, and exit cleanly.
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(context.Background(), "event loop panicked",
					"method", "processEvent", "error", fmt.Errorf("panic: %v", r))
			}
			close(eg.done)
		}()
		for {
			select {
			case evt, ok := <-ch:
				if !ok {
					return
				}
				if evt == nil {
					continue
				}
				o.processEvent(subCtx, evt)
			case <-subCtx.Done():
				return
			}
		}
	}()
	return nil
}

// Stop cancels the subscription and waits for the event loop to exit.
func (o *RuntimeObserver) Stop() {
	o.mu.Lock()
	cancel := o.cancel
	eg := o.eg
	o.cancel = nil
	o.eg = nil
	o.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if eg != nil {
		eg.Wait()
	}
}

// processEvent converts a single event into a StrategySample (when the
// event represents a strategy outcome — see eventToSample) and writes it
// to the evidence store.
func (o *RuntimeObserver) processEvent(ctx context.Context, evt *ares_events.Event) {
	sample, ok := o.eventToSample(evt)
	if !ok {
		return
	}
	o.writeEvidence(ctx, sample)
}

// evidenceKeyStrategyID is the evidence payload key carrying the strategy
// attribution (evolution loop closure E1). It is read from task events by
// eventToSample and written into every fitness/decision payload, so promote
// attribution stays consistent across producers.
const evidenceKeyStrategyID = "strategy_id"

// agentStoppedGracefulReasons lists EventAgentStopped payload "reason"
// values that represent intentional, operator-driven terminations. These
// say nothing about strategy quality, so they produce no sample.
var agentStoppedGracefulReasons = map[string]bool{
	"explicit_stop": true,
}

// eventToSample converts a task lifecycle event into a normalized [0,1]
// StrategySample. Completed → 1.0, Failed → 0.0. EventAgentStopped produces
// a 0.0 sample ONLY when the payload reason marks an abnormal termination
// (e.g. death/restart): a killed agent must not silently keep its fitness
// credit. Graceful stops ("explicit_stop", or no reason — sub-agent
// shutdown) yield ok=false and produce no sample.
// The active strategy ID is resolved from the activeID func (if set) or the
// event payload.
func (o *RuntimeObserver) eventToSample(evt *ares_events.Event) (StrategySample, bool) {
	score := 0.0
	success := false
	switch evt.Type {
	case ares_events.EventTaskCompleted:
		score = 1.0
		success = true
	case ares_events.EventTaskFailed:
		// 0.0 (already initialized).
	case ares_events.EventAgentStopped:
		reason, _ := evt.Payload["reason"].(string)
		if reason == "" || agentStoppedGracefulReasons[reason] {
			return StrategySample{}, false
		}
		// Abnormal termination → failure sample (0.0).
	default:
		return StrategySample{}, false
	}

	strategyID := "unknown"
	if o.activeID != nil {
		if id := o.activeID(); id != "" {
			strategyID = id
		}
	}
	if evt.Payload != nil {
		if id, ok := evt.Payload[evidenceKeyStrategyID].(string); ok && id != "" {
			strategyID = id
		}
	}
	taskType := ""
	if evt.Payload != nil {
		if t, ok := evt.Payload["task_type"].(string); ok {
			taskType = t
		}
	}
	return StrategySample{
		StrategyID: strategyID,
		Success:    success,
		Score:      score,
		TaskType:   taskType,
		At:         time.Now(),
	}, true
}

// writeEvidence writes a KindFitness evidence record (source="strategy") so
// the GA scorer and deployment staging can read real runtime fitness. This
// fixes B6: staging Evaluate now has a multi-dimensional evidence source
// including the "strategy" dimension.
func (o *RuntimeObserver) writeEvidence(ctx context.Context, sample StrategySample) {
	if o.evStore == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"value":               sample.Score,
		"success":             sample.Success,
		evidenceKeyStrategyID: sample.StrategyID,
		"task_type":           sample.TaskType,
	})
	if err != nil {
		return
	}
	_ = o.evStore.Append(ctx, evidence.Evidence{
		// Full-date format: the PG store uses ON CONFLICT (id) DO NOTHING,
		// so a time-only suffix would silently drop samples from different
		// days colliding on the same clock reading.
		ID:        "strategy_" + sample.StrategyID + "_" + sample.At.Format("20060102150405.000000"),
		Source:    observerEvidenceSource,
		Kind:      evidence.KindFitness,
		Payload:   payload,
		Timestamp: sample.At,
	})
}
