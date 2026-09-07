// Package introspect — flight-recorder control-plane endpoints.
//
// After the old internal/dashboard package was deleted (monitoring.md Phase
// 4), the /flight/* read endpoints were dropped with it even though the
// flight data (timeline / summary / graph / decisions / diagnostics /
// genealogy) is still recorded by ares_flight.FlightRecorder. This file
// restores those read-only surfaces under /api/flight/* on the serve control
// plane, backed by the same recorder. It is strictly read-only — nothing here
// mutates the recorder.
package introspect

import (
	"encoding/json"
	"net/http"

	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

// JSON response keys reused across flight endpoints.
const (
	keyMermaid = "mermaid"
	keyRecords = "records"
	keyDist    = "distribution"
)

// FlightProvider supplies the flight-recorder read surfaces. Implemented by
// *flight.FlightRecorder (via the serve wiring); nil disables the endpoints.
type FlightProvider interface {
	// TimelineEvents returns the recorded execution timeline, optionally
	// filtered to one agent (empty agentID = all).
	TimelineEvents(agentID string) []flight.TimelineEvent
	// TimelineSummary aggregates the timeline (optionally per-agent).
	TimelineSummary(agentID string) flight.TimelineSummary
	// GraphMermaid renders the call graph as mermaid source.
	GraphMermaid() string
	// Decisions returns the decision log, optionally filtered to one agent.
	Decisions(agentID string) []flight.Decision
	// Diagnostics returns the failure records (optionally per-agent) and the
	// category distribution.
	Diagnostics(agentID string) ([]flight.DiagnosticRecord, flight.CategoryDistribution)
	// GenealogyMermaid renders the agent lineage tree as mermaid source.
	GenealogyMermaid() string
}

// WithFlight attaches the flight-recorder provider to the control server
// (migrated from dashboard /flight/*, monitoring.md Phase 4 follow-up).
func WithFlight(provider FlightProvider) ControlServerOption {
	return func(s *ControlServer) {
		s.flight = provider
	}
}

// flightRecorderAdapter adapts *flight.FlightRecorder to FlightProvider so the
// serve wiring can hand the bootstrap recorder straight to the control server.
type flightRecorderAdapter struct {
	fr *flight.FlightRecorder
}

// NewFlightRecorderAdapter wraps a *flight.FlightRecorder as a FlightProvider.
// A nil recorder yields a nil provider (endpoints report 503).
func NewFlightRecorderAdapter(fr *flight.FlightRecorder) FlightProvider {
	if fr == nil {
		return nil
	}
	return &flightRecorderAdapter{fr: fr}
}

// TimelineEvents implements FlightProvider.
func (a *flightRecorderAdapter) TimelineEvents(agentID string) []flight.TimelineEvent {
	if agentID != "" {
		return a.fr.Timeline().FilterByAgent(agentID)
	}
	return a.fr.Timeline().Events()
}

// TimelineSummary implements FlightProvider.
func (a *flightRecorderAdapter) TimelineSummary(agentID string) flight.TimelineSummary {
	return a.fr.Timeline().Summary()
}

// GraphMermaid implements FlightProvider.
func (a *flightRecorderAdapter) GraphMermaid() string {
	return a.fr.Graph().ExportMermaid()
}

// Decisions implements FlightProvider.
func (a *flightRecorderAdapter) Decisions(agentID string) []flight.Decision {
	if agentID != "" {
		return a.fr.Decisions().FilterByAgent(agentID)
	}
	return a.fr.Decisions().All()
}

// Diagnostics implements FlightProvider.
func (a *flightRecorderAdapter) Diagnostics(agentID string) ([]flight.DiagnosticRecord, flight.CategoryDistribution) {
	if agentID != "" {
		return a.fr.Diagnostics().FilterByAgent(agentID), a.fr.Diagnostics().Distribution()
	}
	return a.fr.Diagnostics().All(), a.fr.Diagnostics().Distribution()
}

// GenealogyMermaid implements FlightProvider.
func (a *flightRecorderAdapter) GenealogyMermaid() string {
	if a.fr.Genealogy() == nil {
		return "graph LR\n    empty[No agents]"
	}
	return a.fr.Genealogy().ExportMermaid()
}

func (s *ControlServer) handleFlightTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	_ = json.NewEncoder(w).Encode(s.flight.TimelineEvents(r.URL.Query().Get("agent_id")))
}

func (s *ControlServer) handleFlightSummary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode(flight.TimelineSummary{})
		return
	}
	_ = json.NewEncoder(w).Encode(s.flight.TimelineSummary(r.URL.Query().Get("agent_id")))
}

func (s *ControlServer) handleFlightGraph(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode(map[string]string{keyMermaid: "graph LR\n    empty[No data]"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{keyMermaid: s.flight.GraphMermaid()})
}

func (s *ControlServer) handleFlightDecisions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	_ = json.NewEncoder(w).Encode(s.flight.Decisions(r.URL.Query().Get("agent_id")))
}

func (s *ControlServer) handleFlightDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{keyRecords: []any{}, keyDist: flight.CategoryDistribution{}})
		return
	}
	records, dist := s.flight.Diagnostics(r.URL.Query().Get("agent_id"))
	_ = json.NewEncoder(w).Encode(map[string]any{keyRecords: records, keyDist: dist})
}

func (s *ControlServer) handleFlightGenealogy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.flight == nil {
		_ = json.NewEncoder(w).Encode(map[string]string{keyMermaid: "graph LR\n    empty[No agents]"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{keyMermaid: s.flight.GenealogyMermaid()})
}
