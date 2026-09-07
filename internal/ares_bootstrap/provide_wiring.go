// Package ares_bootstrap — evolution wiring adapters.
package ares_bootstrap

import (
	"context"
	"errors"

	"github.com/Timwood0x10/ares/internal/ares_events"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// flightRecorderWrapper wraps flight.FlightRecorder to implement evolution.FlightRecorder interface.
type flightRecorderWrapper struct {
	recorder *flight.FlightRecorder
}

// Diagnostics returns access to diagnostic reports.
func (w *flightRecorderWrapper) Diagnostics() evolution.DiagnosticsAccessor {
	return &diagnosticsAccessorWrapper{engine: w.recorder.Diagnostics()}
}

// EventStore returns the event store subscriber.
func (w *flightRecorderWrapper) EventStore() evolution.EventStoreSubscriber {
	return &eventStoreSubscriberWrapper{store: w.recorder.EventStoreRef()}
}

// diagnosticsAccessorWrapper wraps flight.DiagnosticsEngine to implement evolution.DiagnosticsAccessor.
type diagnosticsAccessorWrapper struct {
	engine *flight.DiagnosticsEngine
}

// Get retrieves the diagnostic report for a specific agent.
func (w *diagnosticsAccessorWrapper) Get(agentID string) *evolution.DiagnosticsReport {
	if w.engine == nil {
		return nil
	}

	records := w.engine.FilterByAgent(agentID)
	if len(records) == 0 {
		return nil
	}

	diagRecords := make([]evolution.DiagnosticRecord, len(records))
	for i, r := range records {
		diagRecords[i] = evolution.DiagnosticRecord{
			ID:         r.ID,
			AgentID:    r.AgentID,
			TaskID:     r.TaskID,
			Category:   string(r.Category),
			RootCause:  r.RootCause,
			Suggestion: r.Suggestion,
			Severity:   categorizeSeverity(r.Category),
		}
	}

	return &evolution.DiagnosticsReport{
		AgentID:   agentID,
		Records:   diagRecords,
		HasIssues: true,
	}
}

// categorizeSeverity converts DiagnosticCategory to a severity score (1-10).
func categorizeSeverity(cat flight.DiagnosticCategory) int {
	switch cat {
	case flight.DiagToolTimeout:
		return 5
	case flight.DiagLLMError:
		return 7
	case flight.DiagParseError:
		return 4
	case flight.DiagMemoryError:
		return 6
	case flight.DiagNetworkError:
		return 6
	case flight.DiagConfigError:
		return 3
	case flight.DiagConcurrencyError:
		return 8
	default:
		return 5
	}
}

// eventStoreSubscriberWrapper wraps ares_events.EventStore to implement evolution.EventStoreSubscriber.
type eventStoreSubscriberWrapper struct {
	store ares_events.EventStore
}

// Subscribe subscribes to ares_events from the underlying event store.
func (w *eventStoreSubscriberWrapper) Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	if w.store == nil {
		return nil, errors.New("event store is nil")
	}
	return w.store.Subscribe(ctx, filter)
}

// expRepoAdapter wraps repositories.ExperienceRepositoryInterface to satisfy evolution.ExperienceRepository.
type expRepoAdapter struct {
	inner repositories.ExperienceRepositoryInterface
}

func (a *expRepoAdapter) Create(ctx context.Context, exp *evolution.Experience) error {
	sm := &storage_models.Experience{
		TenantID: exp.TenantID,
		Type:     exp.Type,
		Problem:  exp.Problem,
		Solution: exp.Solution,
		Score:    exp.Score,
	}
	return a.inner.Create(ctx, sm)
}
