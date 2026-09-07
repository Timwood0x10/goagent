// Package introspect — control-plane endpoints.
//
// After the old internal/monitoring gin server is deleted, the serve control
// plane still needs a small set of read-only JSON endpoints: agent list/detail,
// runtime config snapshot and intelligence health. This file implements them
// as a plain net/http handler so the serve wiring (actionHandler.inner) keeps
// working without gin or the old monitoring package. Destructive endpoints
// (agent kill/resume/retry, chaos, MCP tool call) stay in cmd/ares/actions.go
// behind checkAuth — this handler is strictly read-only.
package introspect

import (
	"encoding/json"
	"net/http"
	"strings"
)

// JSON response keys reused across control endpoints.
const (
	keyError = "error"
	keyLevel = "level"
)

// ControlServerOption configures the control-plane server.
type ControlServerOption func(*ControlServer)

// WithRuntimeConfig attaches the runtime config snapshot source
// (cfgStore.Current().Redacted() + History()); without it the endpoint is
// disabled (mirrors the old monitoring.WithConfigStore).
func WithRuntimeConfig(getConfig func() (cfg any, history []map[string]any)) ControlServerOption {
	return func(s *ControlServer) {
		s.getConfig = getConfig
	}
}

// WithIntel attaches the intelligence engine backing /api/health,
// /api/anomalies and /api/insights.
func WithIntel(intel *Engine) ControlServerOption {
	return func(s *ControlServer) {
		s.intel = intel
	}
}

// AgentSource supplies the agent fleet snapshot.
type AgentSource interface {
	// ListAgents returns a point-in-time copy of every registered agent.
	ListAgents() []AgentView
}

// AgentView is the control-plane's agent row. It mirrors the shape the old
// monitoring /api/agents endpoint returned (id/name/role/status/task_id) so
// `ares status` and existing scripts keep working unchanged.
type AgentView struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Role   string `json:"role,omitempty"`
	Status string `json:"status"`
	TaskID string `json:"task_id,omitempty"`
}

// ControlServer serves the minimal read-only control-plane JSON API on /api/*.
// It is registered by the serve wiring as the fallback handler
// (actionHandler.inner): every request that the actionHandler does not
// intercept (agents/chaos/tools/tasks/graphs/introspect) lands here.
type ControlServer struct {
	agents    AgentSource
	intel     *Engine
	getConfig func() (cfg any, history []map[string]any)

	// observability providers (migrated from internal/dashboard):
	// evolution trajectory, human feedback sink
	// and cross-Fabric spans. Nil disables the endpoint.
	evolution EvolutionTrajectoryProvider
	feedback  EvolutionFeedbackSink
	spans     ObservabilitySpansProvider

	// flight exposes the flight-recorder read surfaces (timeline/summary/
	// graph/decisions/diagnostics/genealogy) migrated from the deleted
	// dashboard /flight/* routes. Nil disables the endpoints.
	flight FlightProvider

	// lifecycleSnapshot supplies the evolution lifecycle state.
	// Nil disables the /api/evolution/lifecycle endpoint.
	lifecycle LifecycleSnapshotProvider
}

// EvolutionTrajectoryProvider supplies the evolution trajectory.
// Implementations record per-generation snapshots; the endpoint renders them
// as JSON.
type EvolutionTrajectoryProvider interface {
	// EvolutionTrajectory returns the recorded generations (oldest first)
	// as generic JSON-friendly values, or nil when nothing is recorded.
	EvolutionTrajectory() []map[string]any
}

// EvolutionFeedback is a human review of an evolution candidate.
type EvolutionFeedback struct {
	// CandidateID is the reviewed strategy/candidate id.
	CandidateID string `json:"candidate_id"`
	// Rating is the human rating (1-5 scale).
	Rating float64 `json:"rating"`
	// Comments is free-form human commentary.
	Comments string `json:"comments,omitempty"`
	// Approved is the human approval decision.
	Approved bool `json:"approved"`
	// Reason explains the approval/denial.
	Reason string `json:"reason,omitempty"`
}

// EvolutionFeedbackSink receives human feedback submissions.
type EvolutionFeedbackSink interface {
	// SubmitFeedback records one human feedback entry.
	SubmitFeedback(fb EvolutionFeedback) error
}

// ObservabilitySpansProvider supplies cross-Fabric trace spans.
type ObservabilitySpansProvider interface {
	// Spans returns a snapshot of the recorded spans (insertion order), or
	// nil when nothing is recorded.
	Spans() []map[string]any
}

// WithEvolution attaches the evolution trajectory + feedback providers
// (migrated from dashboard.APIv2 SetEvolutionTrajectory/SetEvolutionFeedback).
func WithEvolution(trajectory EvolutionTrajectoryProvider, feedback EvolutionFeedbackSink) ControlServerOption {
	return func(s *ControlServer) {
		s.evolution = trajectory
		s.feedback = feedback
	}
}

// WithObservability attaches the cross-Fabric span provider (migrated from
// dashboard.APIv2 SetObservability).
func WithObservability(provider ObservabilitySpansProvider) ControlServerOption {
	return func(s *ControlServer) {
		s.spans = provider
	}
}

// LifecycleSnapshotProvider supplies the evolution lifecycle state snapshot.
// The provider returns a JSON-friendly map describing the current
// candidate state machine: active/previous/shadow strategy IDs, state, window
// score, window sample count, and the most recent promote/rollback decision.
// When not configured, the /api/evolution/lifecycle endpoint returns 404.
type LifecycleSnapshotProvider interface {
	// LifecycleSnapshot returns the current lifecycle state as a generic
	// JSON-friendly map, or nil when the lifecycle is not configured.
	LifecycleSnapshot() map[string]any
}

// WithLifecycleSnapshot attaches the evolution lifecycle snapshot provider.
// When set, the /api/evolution/lifecycle endpoint returns the current
// state machine snapshot.
func WithLifecycleSnapshot(provider LifecycleSnapshotProvider) ControlServerOption {
	return func(s *ControlServer) {
		s.lifecycle = provider
	}
}

// NewControlServer builds the control-plane server. agents may be nil (the
// agent endpoints then report 503, matching the old behavior of an
// unconfigured monitoring plugin).
func NewControlServer(agents AgentSource, opts ...ControlServerOption) *ControlServer {
	s := &ControlServer{agents: agents}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ServeHTTP routes the read-only control-plane endpoints.
func (s *ControlServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/agents":
		s.handleListAgents(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/agents/"):
		s.handleGetAgent(w, r, strings.TrimPrefix(path, "/api/agents/"))
	case r.Method == http.MethodGet && path == "/api/runtime/config":
		s.handleRuntimeConfig(w, r)
	case r.Method == http.MethodGet && path == "/api/health":
		s.handleHealth(w, r)
	case r.Method == http.MethodGet && path == "/api/health/agents":
		s.handleHealthAgents(w, r)
	case r.Method == http.MethodGet && path == "/api/anomalies":
		s.handleAnomalies(w, r)
	case r.Method == http.MethodGet && path == "/api/insights":
		s.handleInsights(w, r)
	// Observability endpoints (migrated from dashboard :8090):
	case r.Method == http.MethodGet && path == "/api/evolution/trajectory":
		s.handleEvolutionTrajectory(w, r)
	case r.Method == http.MethodPost && path == "/api/evolution/feedback":
		http.Error(w, `{"error":"feedback endpoint requires auth, use action API"}`, http.StatusMethodNotAllowed)
	case r.Method == http.MethodGet && path == "/api/observability/spans":
		s.handleObservabilitySpans(w, r)
	// evolution lifecycle state snapshot.
	case r.Method == http.MethodGet && path == "/api/evolution/lifecycle":
		s.handleLifecycleSnapshot(w, r)
	// Flight-recorder read surfaces (migrated from dashboard /flight/*):
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/api/flight/"):
		s.serveFlight(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

// serveFlight dispatches the flight-recorder read endpoints by path suffix.
func (s *ControlServer) serveFlight(w http.ResponseWriter, r *http.Request, path string) {
	switch path {
	case "/api/flight/timeline":
		s.handleFlightTimeline(w, r)
	case "/api/flight/summary":
		s.handleFlightSummary(w, r)
	case "/api/flight/graph":
		s.handleFlightGraph(w, r)
	case "/api/flight/decisions":
		s.handleFlightDecisions(w, r)
	case "/api/flight/diagnostics":
		s.handleFlightDiagnostics(w, r)
	case "/api/flight/genealogy":
		s.handleFlightGenealogy(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *ControlServer) handleLifecycleSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.lifecycle == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "lifecycle snapshot not configured"})
		return
	}
	snap := s.lifecycle.LifecycleSnapshot()
	if snap == nil {
		snap = map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(snap)
}

func (s *ControlServer) handleEvolutionTrajectory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.evolution == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "evolution trajectory not configured"})
		return
	}
	views := s.evolution.EvolutionTrajectory()
	if views == nil {
		views = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(views)
}

func (s *ControlServer) handleObservabilitySpans(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.spans == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "observability spans not configured"})
		return
	}
	spans := s.spans.Spans()
	if spans == nil {
		spans = []map[string]any{}
	}
	_ = json.NewEncoder(w).Encode(spans)
}

func (s *ControlServer) handleListAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.agents == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "agent source not configured"})
		return
	}
	views := s.agents.ListAgents()
	if views == nil {
		views = []AgentView{}
	}
	_ = json.NewEncoder(w).Encode(views)
}

func (s *ControlServer) handleGetAgent(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "application/json")
	if s.agents == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "agent source not configured"})
		return
	}
	for _, v := range s.agents.ListAgents() {
		if v.ID == id {
			_ = json.NewEncoder(w).Encode(v)
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{keyError: "agent not found: " + id})
}

func (s *ControlServer) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.getConfig == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{keyError: "runtime config not configured"})
		return
	}
	cfg, history := s.getConfig()
	_ = json.NewEncoder(w).Encode(map[string]any{"config": cfg, "history": history})
}

func (s *ControlServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.intel == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{keyLevel: "unknown"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		keyLevel: s.intel.SystemHealth().Level,
		"agents": len(s.intel.Anomalies()),
	})
}

func (s *ControlServer) handleHealthAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.intel == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		keyLevel:    s.intel.SystemHealth().Level,
		"anomalies": len(s.intel.Anomalies()),
	})
}

func (s *ControlServer) handleAnomalies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.intel == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(s.intel.Anomalies())})
}

func (s *ControlServer) handleInsights(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.intel == nil {
		_ = json.NewEncoder(w).Encode([]any{})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(s.intel.Insights())})
}
