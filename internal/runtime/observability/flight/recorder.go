package flight

import (
	"context"
	"sync"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/evidence"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// FlightRecorder is the unified entry point for all flight data.
// It aggregates Timeline, Graph, DecisionLog, DiagnosticsEngine, Genealogy, and MemoryPipeline.
type FlightRecorder struct {
	collector  *Collector
	eventStore ares_events.EventStore
	genealogy  *Genealogy
	// genealogyCollector populates genealogy from the event stream. It is
	// created only when the caller did not supply a pre-built Genealogy, so
	// an explicitly injected tree (tests) is never silently overwritten.
	genealogyCollector *GenealogyCollector
	mu                 sync.RWMutex
	started            bool
}

// FlightRecorderConfig holds dependencies for the flight recorder.
type FlightRecorderConfig struct {
	EventStore    ares_events.EventStore
	EvidenceStore evidence.Store // optional: unified Evidence Store
	MemManager    memory.MemoryManager
	Genealogy     *Genealogy // optional, for agent genealogy tracking
}

// NewFlightRecorder creates a new flight recorder.
//
// When cfg.Genealogy is nil but an EventStore is present, the recorder builds
// its own GenealogyCollector and drives it from the event stream. Without
// this, Genealogy() stays nil for every production caller (bootstrap passes
// only EventStore + EvidenceStore), and the /flight/genealogy control endpoint
// degrades to the "No agents" placeholder forever — the lineage tree would be
// write-only code that nothing ever populates.
func NewFlightRecorder(cfg FlightRecorderConfig) *FlightRecorder {
	collector := NewCollector(CollectorConfig{
		EventStore:    cfg.EventStore,
		EvidenceStore: cfg.EvidenceStore,
	})

	fr := &FlightRecorder{
		collector:  collector,
		eventStore: cfg.EventStore,
		genealogy:  cfg.Genealogy,
	}
	if cfg.Genealogy == nil && cfg.EventStore != nil {
		fr.genealogyCollector = NewGenealogyCollector(cfg.EventStore)
		fr.genealogy = fr.genealogyCollector.Genealogy()
	}
	return fr
}

// Start begins collecting flight data.
func (fr *FlightRecorder) Start(ctx context.Context) error {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if fr.started {
		return nil
	}

	if err := fr.collector.Start(ctx); err != nil {
		return err
	}

	// The genealogy collector subscribes to the same event store. A failure
	// here must not abort flight recording: the timeline/graph/diagnostics
	// data is the primary payload and stays valid without the lineage tree.
	if fr.genealogyCollector != nil {
		if err := fr.genealogyCollector.Start(ctx); err != nil {
			log.Warn("flight recorder: genealogy collector start failed (lineage tree disabled)", "error", err)
		}
	}

	fr.started = true
	log.Info("flight recorder started")
	return nil
}

// Stop stops the flight recorder.
func (fr *FlightRecorder) Stop() {
	fr.mu.Lock()
	defer fr.mu.Unlock()

	if !fr.started {
		return
	}

	fr.collector.Stop()
	if fr.genealogyCollector != nil {
		fr.genealogyCollector.Stop()
	}
	fr.started = false
	log.Info("flight recorder stopped")
}

// Timeline returns the execution timeline.
func (fr *FlightRecorder) Timeline() *Timeline {
	return fr.collector.Timeline()
}

// Graph returns the call graph.
func (fr *FlightRecorder) Graph() *Graph {
	return fr.collector.Graph()
}

// Decisions returns the decision log.
func (fr *FlightRecorder) Decisions() *DecisionLog {
	return fr.collector.Decisions()
}

// Diagnostics returns the diagnostics engine.
func (fr *FlightRecorder) Diagnostics() *DiagnosticsEngine {
	return fr.collector.Diagnostics()
}

// EventStoreRef returns the underlying event store for direct subscription.
// This allows external subscribers (e.g., evolution adapters) to receive ares_events
// without going through the Collector's internal processing pipeline.
func (fr *FlightRecorder) EventStoreRef() ares_events.EventStore {
	return fr.eventStore
}

// Genealogy returns the agent genealogy tree. May be nil if not configured.
func (fr *FlightRecorder) Genealogy() *Genealogy {
	fr.mu.RLock()
	defer fr.mu.RUnlock()
	return fr.genealogy
}

// Pipeline returns the memory pipeline for a session.
func (fr *FlightRecorder) Pipeline(sessionID string) *MemoryPipeline {
	return fr.collector.Pipeline(sessionID)
}

// Replay creates a replay session for a task.
func (fr *FlightRecorder) Replay(ctx context.Context, taskID string) (*ReplaySession, error) {
	return NewReplaySession(ctx, fr.eventStore, taskID)
}
