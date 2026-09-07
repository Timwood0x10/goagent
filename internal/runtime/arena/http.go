package arena

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

// apiKeyHeader is the HTTP header used to pass the arena API key.
//
//nolint:gosec // G101: not a credential, just the header name
const apiKeyHeader = "X-API-Key"

// Handler provides HTTP endpoints for the arena.
type Handler struct {
	service  *Service
	recorder *flight.FlightRecorder
	apiKey   string // when non-empty, all routes require this key
	// allowAnonymous disables authentication entirely. It must be opted into
	// explicitly and is intended for local development only. An unset API key
	// alone is NOT sufficient to bypass auth: arena exposes destructive
	// endpoints (leader/agent kill, node removal, memory corruption), so the
	// default posture is deny.
	allowAnonymous bool
}

// NewHandler creates a Handler backed by the given Service.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// SetFlightRecorder attaches a flight recorder for arena-flight data endpoints.
func (h *Handler) SetFlightRecorder(recorder *flight.FlightRecorder) {
	h.recorder = recorder
}

// SetAPIKey enables API key authentication on all arena routes. Every request
// must then include the X-API-Key header with a matching value.
func (h *Handler) SetAPIKey(key string) {
	h.apiKey = key
}

// AllowAnonymous explicitly disables authentication on all arena routes.
// This is an intentional, opt-in escape hatch for local development. Callers
// must never enable it for a network-reachable deployment: arena routes can
// kill the leader, remove nodes and corrupt agent memory.
func (h *Handler) AllowAnonymous(allow bool) {
	h.allowAnonymous = allow
}

// APIKeyAuthMiddleware enforces that incoming requests carry the configured
// X-API-Key header.
//
// The default posture is deny: when no key is configured and anonymous access
// was not explicitly opted into via AllowAnonymous, every request is rejected
// with 401. A missing key is treated as a misconfiguration, not as permission
// to serve destructive endpoints anonymously.
func (h *Handler) APIKeyAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.allowAnonymous {
			next.ServeHTTP(w, r)
			return
		}
		if h.apiKey == "" {
			writeError(w, http.StatusUnauthorized,
				"arena API key not configured: set ARENA_API_KEY (or --api-key), "+
					"or pass --allow-anonymous for local development")
			return
		}
		provided := r.Header.Get(apiKeyHeader)
		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(h.apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RegisterRoutes registers arena routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /arena/leader/kill", h.handleKillLeader)
	mux.HandleFunc("POST /arena/agent/{id}/kill", h.handleKillAgent)
	mux.HandleFunc("POST /arena/node/{id}/remove", h.handleRemoveNode)
	mux.HandleFunc("POST /arena/edge/remove", h.handleRemoveEdge)
	mux.HandleFunc("GET /arena/stats", h.handleStats)
	mux.HandleFunc("GET /arena/history", h.handleHistory)
	mux.HandleFunc("GET /arena/stream", h.handleStream)
	mux.HandleFunc("GET /arena/score", h.handleScore)
	mux.HandleFunc("GET /arena/metrics", h.handleMetrics)
	mux.HandleFunc("POST /arena/orchestrator/kill", h.handleKillOrchestrator)
	mux.HandleFunc("POST /arena/agent/{id}/partition", h.handleNetworkPartition)
	mux.HandleFunc("POST /arena/agent/{id}/pause", h.handlePauseAgent)
	mux.HandleFunc("POST /arena/agent/{id}/resume", h.handleResumeAgent)
	mux.HandleFunc("POST /arena/agent/{id}/slow", h.handleSlowAgent)
	mux.HandleFunc("POST /arena/survival", h.handleSurvivalStart)
	mux.HandleFunc("POST /arena/survival/stop", h.handleSurvivalStop)
	mux.HandleFunc("GET /arena/survival/status", h.handleSurvivalStatus)
	mux.HandleFunc("GET /arena/flight/timeline", h.handleArenaTimeline)
	mux.HandleFunc("GET /arena/flight/diagnostics", h.handleArenaDiagnostics)
	mux.HandleFunc("POST /arena/agent/{id}/tool-timeout", h.handleToolTimeout)
	mux.HandleFunc("POST /arena/agent/{id}/memory-corrupt", h.handleMemoryCorrupt)
	mux.HandleFunc("POST /arena/agent/{id}/mcp-disconnect", h.handleMCPDisconnect)
	mux.HandleFunc("POST /arena/agent/{id}/llm-failure", h.handleLLMFailure)
	mux.HandleFunc("POST /arena/scenario/run", h.handleScenarioRun)
	mux.HandleFunc("POST /arena/scenario/validate", h.handleScenarioValidate)
}

// edgeRequest is the JSON body for RemoveEdge requests.
type edgeRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (h *Handler) handleKillLeader(w http.ResponseWriter, r *http.Request) {
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionKillLeader,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleKillAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionKillAgent,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleRemoveNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionRemoveNode,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleRemoveEdge(w http.ResponseWriter, r *http.Request) {
	var req edgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.From == "" || req.To == "" {
		writeError(w, http.StatusBadRequest, "both 'from' and 'to' are required")
		return
	}
	if errMsg := validAgentID(req.From); errMsg != "" {
		writeError(w, http.StatusBadRequest, "from: "+errMsg)
		return
	}
	if errMsg := validAgentID(req.To); errMsg != "" {
		writeError(w, http.StatusBadRequest, "to: "+errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionRemoveEdge,
		TargetID:  req.To,
		SourceID:  req.From,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleKillOrchestrator(w http.ResponseWriter, r *http.Request) {
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionKillOrchestrator,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleNetworkPartition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionNetworkPartition,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

// slowRequest is the JSON body for SlowAgent requests.
type slowRequest struct {
	Duration string `json:"duration"`
}

func (h *Handler) handlePauseAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionPauseAgent,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleResumeAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionResumeAgent,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleSlowAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	var req slowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionSlowAgent,
		TargetID:  id,
		Metadata:  map[string]any{"duration": req.Duration},
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleSurvivalStop(w http.ResponseWriter, _ *http.Request) {
	h.service.StopSurvival()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped", "message": "survival mode stopped"})
}

// toolTimeoutRequest is the JSON body for ToolTimeout requests.
type toolTimeoutRequest struct {
	Duration string `json:"duration"`
}

func (h *Handler) handleToolTimeout(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	var req toolTimeoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionToolTimeout,
		TargetID:  id,
		Metadata:  map[string]any{"duration": req.Duration},
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleMemoryCorrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionMemoryCorrupt,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleMCPDisconnect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionMCPDisconnect,
		TargetID:  id,
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

// llmFailureRequest is the JSON body for LLMFailure requests.
type llmFailureRequest struct {
	ErrorType string `json:"error_type"`
}

func (h *Handler) handleLLMFailure(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if errMsg := validAgentID(id); errMsg != "" {
		writeError(w, http.StatusBadRequest, errMsg)
		return
	}
	var req llmFailureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	action := Action{
		ID:        uuid.New().String(),
		Type:      ActionLLMFailure,
		TargetID:  id,
		Metadata:  map[string]any{"error_type": req.ErrorType},
		CreatedAt: time.Now(),
	}
	result := h.service.Execute(r.Context(), action)
	writeResult(w, result)
}

func (h *Handler) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.service.Stats())
}

func (h *Handler) handleHistory(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.service.History())
}

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch, err := h.service.Subscribe(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ch == nil {
		writeError(w, http.StatusServiceUnavailable, "event store not configured")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				log.Error("arena: sse marshal error", "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				log.Debug("arena: sse write error (client disconnected?)", "error", err)
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleScore(w http.ResponseWriter, _ *http.Request) {
	stats := h.service.Stats()
	avgRecovery := h.service.calculateAvgRecoveryTime(nil)
	metrics := h.service.Metrics()
	score := CalculateScore(stats, avgRecovery, &metrics)
	writeJSON(w, http.StatusOK, score)
}

func (h *Handler) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	metrics := h.service.Metrics()
	writeJSON(w, http.StatusOK, metrics)
}

func (h *Handler) handleSurvivalStart(w http.ResponseWriter, r *http.Request) {
	var cfg SurvivalConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if cfg.Duration <= 0 {
		cfg.Duration = 30 * time.Minute
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}

	// Run survival in background. Use context.Background() as parent because
	// survival is a long-running background task that outlives the HTTP request lifecycle.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Duration*2)
		defer cancel()
		report := h.service.RunSurvival(ctx, cfg)
		log.Info("arena: survival run finished in background",
			"actions", report.ActionsRun,
			"score", report.Score.Score,
			"grade", report.Score.Grade,
		)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "started",
		"message": "survival run started",
	})
}

func (h *Handler) handleSurvivalStatus(w http.ResponseWriter, _ *http.Request) {
	status := h.service.GetSurvivalStatus()
	writeJSON(w, http.StatusOK, status)
}

// handleArenaTimeline returns arena-related timeline events (filtered by source=arena).
func (h *Handler) handleArenaTimeline(w http.ResponseWriter, _ *http.Request) {
	if h.recorder == nil {
		writeError(w, http.StatusServiceUnavailable, "flight recorder not configured")
		return
	}

	allEvents := h.recorder.Timeline().Events()
	var arenaEvents []flight.TimelineEvent
	for _, e := range allEvents {
		if src, ok := e.Metadata["source"].(string); ok && src == "arena" {
			arenaEvents = append(arenaEvents, e)
		}
	}
	writeJSON(w, http.StatusOK, arenaEvents)
}

// handleArenaDiagnostics returns arena-related diagnostic records.
func (h *Handler) handleArenaDiagnostics(w http.ResponseWriter, _ *http.Request) {
	if h.recorder == nil {
		writeError(w, http.StatusServiceUnavailable, "flight recorder not configured")
		return
	}

	allRecords := h.recorder.Diagnostics().All()
	var arenaRecords []flight.DiagnosticRecord
	for _, r := range allRecords {
		if len(r.TaskID) >= 6 && r.TaskID[:6] == "arena-" {
			arenaRecords = append(arenaRecords, r)
		}
	}
	writeJSON(w, http.StatusOK, arenaRecords)
}

func (h *Handler) handleScenarioRun(w http.ResponseWriter, r *http.Request) {
	var scenario Scenario
	if err := json.NewDecoder(r.Body).Decode(&scenario); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if err := ValidateScenario(&scenario); err != nil {
		writeError(w, http.StatusBadRequest, "validation failed: "+err.Error())
		return
	}

	report, err := RunScenarioReport(r.Context(), h.service, scenario)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scenario execution failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, report)
}

func (h *Handler) handleScenarioValidate(w http.ResponseWriter, r *http.Request) {
	var scenario Scenario
	if err := json.NewDecoder(r.Body).Decode(&scenario); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if err := ValidateScenario(&scenario); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":    false,
			"error":    err.Error(),
			"scenario": scenario.Name,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"valid":        true,
		"error":        "",
		"scenario":     scenario.Name,
		"action_count": len(scenario.Actions),
		"description":  scenario.Description,
		"tags":         scenario.Tags,
	})
}

// writeResult writes an action result as JSON. Returns 500 on failure, 200 on success.
func writeResult(w http.ResponseWriter, result Result) {
	status := http.StatusOK
	if !result.Success {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, result)
}

// writeJSON marshals v as JSON and writes it to w.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error("arena: json encode error", "error", err)
	}
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// RecoverMiddleware wraps an http.Handler with panic recovery.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("arena: handler panic recovered", "panic", rec, "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// validAgentID validates an agent/node ID for basic format constraints.
// Returns an error message if invalid, or empty string if valid.
func validAgentID(id string) string {
	if id == "" {
		return "missing agent id"
	}
	if len(id) > 256 {
		return "agent id too long (max 256 characters)"
	}
	for _, r := range id {
		if unicode.IsSpace(r) {
			return "agent id must not contain whitespace"
		}
	}
	return ""
}

// ValidateAction checks that an action has the required fields for its type.
func ValidateAction(action Action) error {
	if action.Type == "" {
		return errors.New("action type is required")
	}
	switch action.Type {
	case ActionKillLeader:
		// No target required; leader is discovered automatically.
	case ActionKillAgent:
		if action.TargetID == "" {
			return errors.New("target_id is required for kill_agent")
		}
	case ActionRemoveNode:
		if action.TargetID == "" {
			return errors.New("target_id is required for remove_node")
		}
	case ActionRemoveEdge:
		if action.SourceID == "" || action.TargetID == "" {
			return errors.New("source_id and target_id are required for remove_edge")
		}
	case ActionPauseAgent:
		if action.TargetID == "" {
			return errors.New("target_id is required for pause_agent")
		}
	case ActionResumeAgent:
		if action.TargetID == "" {
			return errors.New("target_id is required for resume_agent")
		}
	case ActionSlowAgent:
		if action.TargetID == "" {
			return errors.New("target_id is required for slow_agent")
		}
	case ActionKillOrchestrator:
		// No target required; orchestrator is discovered automatically.
	case ActionNetworkPartition:
		if action.TargetID == "" {
			return errors.New("target_id is required for network_partition")
		}
	case ActionToolTimeout:
		if action.TargetID == "" {
			return errors.New("target_id is required for tool_timeout")
		}
	case ActionMemoryCorrupt:
		if action.TargetID == "" {
			return errors.New("target_id is required for memory_corrupt")
		}
	case ActionMCPDisconnect:
		if action.TargetID == "" {
			return errors.New("target_id is required for mcp_disconnect")
		}
	case ActionLLMFailure:
		if action.TargetID == "" {
			return errors.New("target_id is required for llm_failure")
		}
	default:
		return fmt.Errorf("unknown action type: %s", action.Type)
	}
	return nil
}

// RoutePath returns the canonical route path for an action type.
func RoutePath(actionType ActionType) string {
	switch actionType {
	case ActionKillLeader:
		return "POST /arena/leader/kill"
	case ActionKillAgent:
		return "POST /arena/agent/{id}/kill"
	case ActionRemoveNode:
		return "POST /arena/node/{id}/remove"
	case ActionRemoveEdge:
		return "POST /arena/edge/remove"
	case ActionPauseAgent:
		return "POST /arena/agent/{id}/pause"
	case ActionResumeAgent:
		return "POST /arena/agent/{id}/resume"
	case ActionSlowAgent:
		return "POST /arena/agent/{id}/slow"
	case ActionKillOrchestrator:
		return "POST /arena/orchestrator/kill"
	case ActionNetworkPartition:
		return "POST /arena/agent/{id}/partition"
	case ActionToolTimeout:
		return "POST /arena/agent/{id}/tool-timeout"
	case ActionMemoryCorrupt:
		return "POST /arena/agent/{id}/memory-corrupt"
	case ActionMCPDisconnect:
		return "POST /arena/agent/{id}/mcp-disconnect"
	case ActionLLMFailure:
		return "POST /arena/agent/{id}/llm-failure"
	default:
		return ""
	}
}

// ParseActionType converts a string to an ActionType.
func ParseActionType(s string) (ActionType, error) {
	switch strings.ToLower(s) {
	case "kill_leader":
		return ActionKillLeader, nil
	case "kill_agent":
		return ActionKillAgent, nil
	case "remove_node":
		return ActionRemoveNode, nil
	case "remove_edge":
		return ActionRemoveEdge, nil
	case "pause_agent":
		return ActionPauseAgent, nil
	case "resume_agent":
		return ActionResumeAgent, nil
	case "slow_agent":
		return ActionSlowAgent, nil
	case "kill_orchestrator":
		return ActionKillOrchestrator, nil
	case "network_partition":
		return ActionNetworkPartition, nil
	case "tool_timeout":
		return ActionToolTimeout, nil
	case "memory_corrupt":
		return ActionMemoryCorrupt, nil
	case "mcp_disconnect":
		return ActionMCPDisconnect, nil
	case "llm_failure":
		return ActionLLMFailure, nil
	default:
		return "", fmt.Errorf("unknown action type: %s", s)
	}
}
