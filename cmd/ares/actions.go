package main

//nolint: errcheck // best-effort ResponseWriter writes (see writeJSON below)

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	api_tools "github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/runtime"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// writeJSON encodes v to w. HTTP handlers cannot recover a failed response
// write (the status line and headers are already sent), so the error is only
// logged — the client sees a truncated body, the log is the trace.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("actions: encode response failed", "error", err)
	}
}

// actionHandler wraps the monitoring HTTP handler with:
//   - Agent lifecycle (kill/resume/retry)
//   - Chaos engineering (random-kill/kill-all/recover)
//   - Tool API (list/call)
//
// All destructive endpoints (agents, chaos, tools/call) require authentication:
// either the legacy API key (Authorization: Bearer <key>) or a valid JWT with
// write permission (admin/operator) when JWT auth is configured. When neither
// credential is available, all destructive requests are denied
// (deny-by-default). Every destructive action is recorded on the modular audit
// sink (v0.3.0 review: these paths were previously API-key-only and
// un-audited because actionHandler intercepted the gin routes).
type actionHandler struct {
	inner  http.Handler
	mgr    *runtime.Manager
	tools  *api_tools.Registry
	apiKey string                        // legacy credential (nil/empty = disabled)
	auth   *ares_security.AuthMiddleware // JWT credential (nil = disabled)
	audit  *ares_security.AuditLogger    // modular audit sink (nil = disabled)
	// kernel is the peer-runtime kernel handle (Leader OFF mode). It powers
	// POST /api/tasks (submitPeerTask); nil on the legacy leader path makes
	// that endpoint report 503 "peer runtime not active".
	kernel *kernelHandle
	// chaosStopToken guards the chaos emergency-stop endpoint: requests must
	// carry a matching X-Chaos-Token header. Empty disables the endpoint.
	chaosStopToken string
	// intro serves the runtime introspection panel (monitoring.md): embedded
	// UI at GET /introspect and the JSON read API at
	// /api/v1/introspect/*. Nil (panel not wired) yields 404, matching any
	// other unknown path.
	intro *introspect.Handler
	// lifecycle is the evolution StrategyLifecycle (P2-4). It powers
	// POST /api/evolution/approve (manual gate release); nil disables the
	// endpoint with 503 "evolution lifecycle not active".
	lifecycle *evolution.StrategyLifecycle
	// cost serves the LLM cost dashboard API (W1): /api/v1/observability/cost*
	// and the HTML dashboard. Nil disables the routes (404).
	cost *observability.CostDashboard
	// costMux routes the cost endpoints; built once at handler construction
	// via buildCostMux. Non-nil iff cost is non-nil — serveIntrospect
	// dereferences it whenever cost is set, so a nil here panics on the
	// first dashboard request.
	costMux *http.ServeMux
	// readAuth verifies JWTs at READ permission for the JSON read surfaces
	// (/api/v1/introspect/*, /api/tools, /api/mcp/tools, cost API). Nil when
	// auth is not configured: those surfaces then stay unauthenticated, which
	// is safe only because serve defaults to a loopback bind (T1). The panel
	// HTML UI (/introspect) and /metrics stay open regardless — the UI
	// carries no data itself, and metrics follow the scraper convention.
	readAuth *ares_security.AuthMiddleware
}

// buildCostMux registers the cost dashboard routes on a dedicated mux, once
// at handler construction. The result is assigned to actionHandler.costMux
// in the same literal that sets cost, so the two can never drift apart
// (an earlier per-request rebuild hid exactly that drift until it panicked).
func buildCostMux(dash *observability.CostDashboard) *http.ServeMux {
	if dash == nil {
		return nil
	}
	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)
	return mux
}

// checkAuth enforces authentication on destructive endpoints: the legacy API
// key OR a valid JWT with write permission. Returns true if authorized.
// When neither credential is configured, all requests are denied
// (deny-by-default). A valid JWT that lacks write permission is rejected with
// 403 Forbidden (not 401), so the "unauthenticated vs forbidden" distinction
// matches the gin middleware.
func (h *actionHandler) checkAuth(w http.ResponseWriter, r *http.Request) *ares_security.Principal {
	// JWT path first: a valid token with write permission.
	jwtForbidden := false
	if h.auth != nil {
		if princ, status := h.auth.Verify(r); status == http.StatusOK {
			return princ
		} else if status == http.StatusForbidden {
			jwtForbidden = true
		}
	}
	// Legacy API key path.
	if h.apiKey != "" {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			token := strings.TrimPrefix(auth, prefix)
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(h.apiKey)) == 1 {
				return &ares_security.Principal{Subject: "api-key", Role: ares_security.RoleOperator}
			}
		}
	}
	if h.apiKey == "" && h.auth == nil {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"error": "auth not configured"})
		return nil
	}
	// A well-formed JWT was presented but the role lacks write permission.
	// Report Forbidden (authenticated, not authorized) rather than the
	// misleading Unauthorized the generic path would give.
	if jwtForbidden {
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "insufficient role: token is valid but lacks write permission"})
		return nil
	}
	w.WriteHeader(http.StatusUnauthorized)
	writeJSON(w, map[string]any{"error": "invalid credentials"})
	return nil
}

// auditAction records a destructive action on the modular audit sink.
func (h *actionHandler) auditAction(action, target string, princ *ares_security.Principal, ok bool) {
	if h.audit == nil {
		return
	}
	subject := "unauthenticated"
	if princ != nil {
		subject = princ.Subject
	}
	h.audit.Action(action, subject, target, ok)
}

// checkAuthRead gates the JSON read surfaces at READ permission (T7): a valid
// JWT with read permission or the legacy API key (a write key may read).
// When auth is not configured at all it allows the request — the same policy
// the introspect surface documented before T7, safe only under the loopback
// default bind (T1). Returns false after writing the 401/403 response.
func (h *actionHandler) checkAuthRead(w http.ResponseWriter, r *http.Request) bool {
	if h.readAuth == nil && h.apiKey == "" {
		// Auth not configured: unauthenticated read access, loopback by default.
		return true
	}
	// JWT path first: a valid token with READ permission (agent role qualifies).
	if h.readAuth != nil {
		if _, status := h.readAuth.Verify(r); status == http.StatusOK {
			return true
		}
	}
	// Legacy API key path: a write credential may also read.
	if h.apiKey != "" {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			token := strings.TrimPrefix(auth, prefix)
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(h.apiKey)) == 1 {
				return true
			}
		}
	}
	w.WriteHeader(http.StatusUnauthorized)
	writeJSON(w, map[string]any{"error": "invalid credentials"})
	return false
}

// serveIntrospect handles the read-only introspection surface: the panel UI,
// its JSON feed under /api/v1/introspect/*, and a root redirect to the panel.
// Returns true when it handled the request.
//
// Auth policy (T7): the JSON feed — task payloads, raw events, live
// scheduler state — requires READ credentials whenever auth is configured
// (checkAuthRead); the panel HTML (/introspect), the root redirect and
// /metrics stay open (the UI carries no data itself; metrics follow the
// scraper convention). With auth unconfigured every read route is open,
// which is safe only under the loopback default bind (T1).
func (h *actionHandler) serveIntrospect(w http.ResponseWriter, r *http.Request, path string) bool {
	if h.intro == nil || r.Method != http.MethodGet {
		return false
	}
	switch {
	case path == "/introspect" || strings.HasPrefix(path, "/introspect/"):
		// Panel UI: static HTML shell, no data — stays open.
		h.intro.ServeHTTP(w, r)
		return true
	case strings.HasPrefix(path, "/api/v1/introspect/"):
		// JSON feed: task payloads and raw events — read-gated (T7).
		if !h.checkAuthRead(w, r) {
			return true
		}
		h.intro.ServeHTTP(w, r)
		return true
	case path == "/metrics":
		// Prometheus scrape endpoint (monitoring.md Phase 4: the old :8090
		// dashboard server mounted /metrics; re-mounted here so scraping the
		// ARES runtime survives the dashboard deletion).
		observability.MetricsHTTPHandler().ServeHTTP(w, r)
		return true
	case h.cost != nil && (strings.HasPrefix(path, "/api/v1/observability/cost") ||
		path == "/api/v1/observability/dashboard"):
		// W1: LLM cost dashboard (read-only GET). The mux is built once via
		// buildCostMux in the construction literal — rebuilding per request
		// was pure waste. Cost data is read-gated too (T7).
		if !h.checkAuthRead(w, r) {
			return true
		}
		h.costMux.ServeHTTP(w, r)
		return true
	case path == "/":
		http.Redirect(w, r, "/introspect", http.StatusFound)
		return true
	}
	return false
}

//nolint:gocyclo
func (h *actionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// B24: Limit request body on all POST endpoints to 1MB to prevent
	// memory exhaustion from oversized payloads.
	if r.Method == http.MethodPost && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
	}

	// Agent lifecycle: POST /api/agents/:id/{kill,resume,retry}
	if r.Method == "POST" && strings.HasPrefix(path, "/api/agents/") {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/api/agents/"), "/")
		if len(parts) == 2 {
			agentID, action := parts[0], parts[1]
			switch action {
			case "kill":
				h.handleAction(w, r, agentID, "kill", princ, h.mgr.StopAgent)
				return
			case "resume", "retry":
				h.handleAction(w, r, agentID, action, princ, func(ctx context.Context, id string) error {
					return h.mgr.RestartAgent(ctx, id)
				})
				return
			}
		}
	}

	// Read-only introspection + metrics routes (panel UI, JSON feed, root
	// redirect, Prometheus scrape) — all unauthenticated GET pass-throughs.
	if h.serveIntrospect(w, r, path) {
		return
	}

	// Chaos engineering: POST /api/chaos/{random-kill,kill-all,recover,stop}
	if r.Method == "POST" && strings.HasPrefix(path, "/api/chaos/") {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		h.handleChaos(w, r, princ, strings.TrimPrefix(path, "/api/chaos/"))
		return
	}

	// Evolution governance: POST /api/evolution/approve (P2-4 manual gate).
	if r.Method == "POST" && path == "/api/evolution/approve" {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		h.handleEvolutionApprove(w, r, princ)
		return
	}

	// Tool API: POST /api/tools/call
	if r.Method == "POST" && path == "/api/tools/call" {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		h.handleCallTool(w, r, princ)
		return
	}

	// Tool API: GET /api/tools — read-gated (T7): the tool inventory is
	// reconnaissance surface, so it requires READ credentials when auth is
	// configured (same policy as the introspect JSON feed).
	if r.Method == "GET" && path == "/api/tools" {
		if !h.checkAuthRead(w, r) {
			return
		}
		h.handleListTools(w)
		return
	}

	// MCP tool API (monitoring.md Phase 4: migrated from the old gin server
	// into the actionHandler so the control plane stays unified):
	//   GET  /api/mcp/tools           → list available tools (read-gated, T7)
	//   POST /api/mcp/tools/:name/call → invoke a tool (requires auth)
	if r.Method == "GET" && path == "/api/mcp/tools" {
		if !h.checkAuthRead(w, r) {
			return
		}
		h.handleListMCPTools(w)
		return
	}
	if r.Method == "POST" && strings.HasPrefix(path, "/api/mcp/tools/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/mcp/tools/"), "/")
		if len(parts) == 2 && parts[1] == "call" {
			princ := h.checkAuth(w, r)
			if princ == nil {
				return
			}
			h.handleCallMCPTool(w, r, princ, parts[0])
			return
		}
	}

	// Peer task submission: POST /api/tasks — the user-facing entry of the
	// peer runtime loop (submitPeerTask). A task is created in the Task
	// Fabric and the kernel scheduler drives it to completion asynchronously.
	if r.Method == "POST" && path == "/api/tasks" {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		h.handleSubmitTask(w, r, princ)
		return
	}

	// Collaboration graph submission (fusion plan Phase C4): a caller posts
	// a DAG description; the kernel fabric executes it node-by-node.
	if r.Method == "POST" && path == "/api/graphs" {
		princ := h.checkAuth(w, r)
		if princ == nil {
			return
		}
		h.handleSubmitGraph(w, r, princ)
		return
	}

	// Pass through to the read-only control server (introspect.ControlServer:
	// /api/agents, /api/health, /api/runtime/config, /api/flight/*,
	// /api/observability/spans, /api/insights, /api/anomalies,
	// /api/evolution/trajectory). These are read-gated for the same reason as
	// the introspect feed (T7 demands ONE policy across equally sensitive read
	// surfaces): the flight recorder carries scheduling decisions and
	// diagnostics, /api/agents the live agent topology. Non-/api paths (the
	// 404 tail) are left ungated so probing a wrong URL does not need a token.
	if strings.HasPrefix(path, "/api/") && !h.checkAuthRead(w, r) {
		return
	}
	h.inner.ServeHTTP(w, r)
}

// ── Peer Task Submission ─────────────────────────────────

// submitTaskRequest is the POST /api/tasks payload. capability selects the
// peer agent that can handle the task (matches its declared capabilities);
// payload carries opaque user data (task_desc, profile fields, ...).
type submitTaskRequest struct {
	Capability string         `json:"capability"`
	Payload    map[string]any `json:"payload"`
}

// handleSubmitTask submits a task to the peer runtime through the kernel
// (submitPeerTask) and returns the assigned task id. The submission is
// asynchronous: the scheduler drains the fabric and executes the task; the
// response only confirms acceptance. A nil peer kernel reports 503 so
// callers can distinguish "not a peer runtime" from a real submission
// failure.
func (h *actionHandler) handleSubmitTask(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal) {
	w.Header().Set("Content-Type", "application/json")
	if h.kernel == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{
			"error":  "peer runtime not active",
			"status": "error",
		})
		return
	}
	var req submitTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if req.Capability == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "capability is required"})
		return
	}
	taskID, err := submitPeerTask(r.Context(), h.kernel, req.Capability, req.Payload)
	if err != nil {
		h.auditAction("submit_task", req.Capability, princ, false)
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error(), "status": "error"})
		return
	}
	h.auditAction("submit_task", req.Capability, princ, true)
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{
		"task_id": taskID,
		"status":  "submitted",
		"message": "task accepted by the peer runtime",
	})
}

// ── Evolution Governance (P2-4) ──────────────────────────

// handleEvolutionApprove promotes the candidate held in SHADOW by the
// gates.require_manual_approval manual gate (P2-4). Submit only HOLDS the
// candidate (it returns immediately), so Approve here performs the actual
// promote — the response reports the newly-active strategy ID. Approve() is
// a no-op when nothing is pending; the 409 below distinguishes that from a
// real approval. Like every mutator here it is deny-by-default (checkAuth)
// and audited.
func (h *actionHandler) handleEvolutionApprove(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal) {
	w.Header().Set("Content-Type", "application/json")
	if h.lifecycle == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{
			"error":  "evolution lifecycle not active",
			"status": "error",
		})
		return
	}
	pendingBefore := h.lifecycle.Snapshot().PendingApproval
	if !pendingBefore {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{
			"error":  "no candidate pending manual approval",
			"status": "error",
		})
		return
	}
	h.lifecycle.Approve()
	h.auditAction("evolution_approve", "lifecycle", princ, true)
	snap := h.lifecycle.Snapshot()
	writeJSON(w, map[string]any{
		"status":         "approved",
		"pending_before": true,
		"pending_after":  snap.PendingApproval,
		"active_id":      snap.ActiveID,
		"last_decision":  snap.LastDecision,
	})
}

// ── Agent Lifecycle ──────────────────────────────────────

func (h *actionHandler) handleAction(w http.ResponseWriter, r *http.Request, agentID, action string, princ *ares_security.Principal, fn func(context.Context, string) error) {
	w.Header().Set("Content-Type", "application/json")
	if err := fn(r.Context(), agentID); err != nil {
		h.auditAction(action, agentID, princ, false)
		// B23: Map error to proper HTTP status; don't leak raw err.Error().
		status := http.StatusInternalServerError
		msg := "internal server error"
		switch {
		case errors.Is(err, runtime.ErrAgentNotFound):
			status = http.StatusNotFound
			msg = "agent not found"
		case errors.Is(err, runtime.ErrRuntimeStopped):
			status = http.StatusServiceUnavailable
			msg = "runtime is stopped"
		case errors.Is(err, runtime.ErrAgentAlreadyRegistered):
			status = http.StatusConflict
			msg = "agent already registered"
		case errors.Is(err, runtime.ErrNilAgent), errors.Is(err, runtime.ErrNilFactory):
			status = http.StatusBadRequest
			msg = "invalid agent specification"
		}
		w.WriteHeader(status)
		writeJSON(w, map[string]any{
			"action": action, "agent": agentID, "error": msg, "status": "error",
		})
		return
	}
	h.auditAction(action, agentID, princ, true)
	writeJSON(w, map[string]any{
		"action": action, "agent": agentID, "success": true,
		"message": action + " agent " + agentID + " succeeded",
	})
}

// ── Chaos Engineering ────────────────────────────────────

func (h *actionHandler) handleChaos(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal, chaosType string) {
	w.Header().Set("Content-Type", "application/json")

	// W5 RBAC: destructive chaos (random-kill/kill-all/recover) requires the
	// admin permission; the emergency stop is guarded by its own X-Chaos-Token
	// below and is exempt. Previously every authenticated principal (operator
	// JWT or API key) could trigger them — the declared "chaos is RoleAdmin
	// only" policy was never enforced.
	if chaosType != "stop" && !ares_security.HasPermission(princ.Role, ares_security.PermAdmin) {
		h.auditAction("chaos-"+chaosType, "denied", princ, false)
		w.WriteHeader(http.StatusForbidden)
		writeJSON(w, map[string]any{"error": "insufficient role: chaos operations require admin permission"})
		return
	}

	switch chaosType {
	case "stop":
		// Emergency stop for the live chaos loop (REVIEW #12 Phase 2). The
		// endpoint is armed only when the process was started with a stop
		// token — an empty configured token means live chaos is not armed
		// and there is nothing to stop, so report 503 instead of silently
		// accepting.
		if h.chaosStopToken == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"error":"chaos stop endpoint not armed (stop_token empty)"}`) // best-effort body; status code carries the contract
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Chaos-Token")), []byte(h.chaosStopToken)) != 1 {
			h.auditAction("chaos-stop", "live-loop", princ, false)
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"error":"invalid X-Chaos-Token"}`) // best-effort body
			return
		}
		liveChaosCtl.RequestStop()
		h.auditAction("chaos-stop", "live-loop", princ, true)
		_, _ = fmt.Fprint(w, `{"status":"stopping","message":"live chaos loop will exit"}`) // best-effort body

	case "random-kill":
		// P1 unified lifecycle: when the peer kernel exists, kill a fabric
		// agent so the death flows through the REAL kernel recovery chain
		// (agent.killed → lease expiry → requeue → replacement) instead of
		// the legacy runtime's resurrection.
		if h.kernel != nil {
			target, err := chaosKillRandomFabric(r.Context(), h.kernel)
			if err != nil {
				h.auditAction("chaos-random-kill", "unknown", princ, false)
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": err.Error()})
				return
			}
			h.auditAction("chaos-random-kill", target, princ, true)
			writeJSON(w, map[string]any{
				"chaos": "random-kill", "target": target, "success": true,
				"message": "chaos: killed fabric agent " + target + " (kernel recovery will resume its tasks)",
			})
			return
		}
		agents := h.mgr.ListAgents()
		if len(agents) == 0 {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "no agents"})
			return
		}
		target := agents[rand.Intn(len(agents))]
		if err := h.mgr.StopAgent(r.Context(), target.ID); err != nil {
			h.auditAction("chaos-random-kill", target.ID, princ, false)
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		h.auditAction("chaos-random-kill", target.ID, princ, true)
		writeJSON(w, map[string]any{
			"chaos": "random-kill", "target": target.ID, "success": true,
			"message": "chaos: killed random agent " + target.ID,
		})
	case "kill-all":
		if h.kernel != nil {
			killed, failed, err := chaosKillAllFabric(r.Context(), h.kernel)
			if err != nil {
				h.auditAction("chaos-kill-all", "unknown", princ, false)
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": err.Error()})
				return
			}
			h.auditAction("chaos-kill-all", strings.Join(killed, ","), princ, true)
			writeJSON(w, map[string]any{
				"chaos": "kill-all", "killed": killed, "failed": failed, "success": true,
			})
			return
		}
		agents := h.mgr.ListAgents()
		killed := make([]string, 0, len(agents))
		for _, a := range agents {
			if err := h.mgr.StopAgent(r.Context(), a.ID); err == nil {
				killed = append(killed, a.ID)
			}
		}
		// B25: audit reflects whether ALL agents were stopped, not a blanket true.
		h.auditAction("chaos-kill-all", strings.Join(killed, ","), princ, len(killed) == len(agents))
		writeJSON(w, map[string]any{
			"chaos": "kill-all", "killed": killed, "success": len(killed) == len(agents),
		})
	case "recover":
		// Kernel semantics: what recovers is the TASK (durable intent), not
		// the agent (disposable cognition). Force one recovery sweep that
		// requeues every expired-lease task; the recovery loop spawns a
		// replacement executor on demand and resumes from checkpoint.
		if h.kernel != nil {
			requeued, err := chaosRecoverSweep(h.kernel)
			if err != nil {
				h.auditAction("chaos-recover", "unknown", princ, false)
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": err.Error()})
				return
			}
			h.auditAction("chaos-recover", strings.Join(requeued, ","), princ, true)
			writeJSON(w, map[string]any{
				"chaos": "recover", "recovered_tasks": requeued, "success": true,
				"message": "requeued expired-lease tasks; replacement executors resume from checkpoint",
			})
			return
		}
		agents := h.mgr.ListAgents()
		recovered := make([]string, 0, len(agents))
		needRecover := 0
		for _, a := range agents {
			if a.Status != "running" {
				needRecover++
				if err := h.mgr.RestartAgent(r.Context(), a.ID); err == nil {
					recovered = append(recovered, a.ID)
				}
			}
		}
		// B25: audit reflects whether ALL down agents were recovered, not a blanket true.
		ok := needRecover == 0 || len(recovered) == needRecover
		h.auditAction("chaos-recover", strings.Join(recovered, ","), princ, ok)
		writeJSON(w, map[string]any{
			"chaos": "recover", "recovered": recovered, "success": ok,
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{
			"error":     "unknown chaos type: " + chaosType,
			"available": []string{"random-kill", "kill-all", "recover"},
		})
	}
}

// ── Tool API ─────────────────────────────────────────────

type callToolRequest struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

func (h *actionHandler) handleCallTool(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal) {
	w.Header().Set("Content-Type", "application/json")
	var req callToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if req.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "name is required"})
		return
	}

	if h.tools != nil {
		result, err := h.tools.Execute(r.Context(), req.Name, req.Params)
		if err != nil {
			h.auditAction("call_tool", req.Name, princ, false)
			// Distinguish "tool not found" from a real execution failure so
			// callers get an accurate error instead of a blanket 404.
			if _, ok := h.tools.Get(req.Name); ok {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(w, map[string]any{
					"error": "tool execution failed: " + err.Error(),
				})
			} else {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]any{
					"error": "tool not found: " + req.Name,
					"tools": h.tools.List(),
				})
			}
			return
		}
		h.auditAction("call_tool", req.Name, princ, true)
		writeJSON(w, map[string]any{
			"tool": req.Name, "success": result.Success, "data": result.Data,
		})
		return
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	writeJSON(w, map[string]any{"error": "no tool registry"})
}

func (h *actionHandler) handleListTools(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if h.tools == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "no tool registry"})
		return
	}
	names := h.tools.List()
	writeJSON(w, map[string]any{
		"tools": names,
		"count": len(names),
	})
}

// ── MCP Tool API (migrated from internal/monitoring, monitoring.md Phase 4) ──

// handleListMCPTools returns the available tools with descriptions, matching
// the shape the old monitoring gin /api/mcp/tools endpoint produced.
func (h *actionHandler) handleListMCPTools(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if h.tools == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "no tool registry"})
		return
	}
	infos := h.tools.ListTools()
	if infos == nil {
		infos = []api_tools.ToolInfo{}
	}
	writeJSON(w, infos)
}

// handleCallMCPTool invokes an MCP tool by name. The outcome is audited after
// the call runs so failures are recorded as such (same contract as the old
// monitoring handler).
func (h *actionHandler) handleCallMCPTool(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal, name string) {
	w.Header().Set("Content-Type", "application/json")
	var args map[string]any
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil && !errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid request body"})
			return
		}
	}
	result, err := h.tools.Execute(r.Context(), name, args)
	h.auditAction("call_mcp_tool", name, princ, err == nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"tool_name": name,
		"is_error":  !result.Success,
		"output":    map[string]any{"success": result.Success, "data": result.Data},
	})
}

// ── Collaboration Graph API ─────────────────────────────

// graphSubmissionRequest is the POST /api/graphs payload: an explicit DAG of
// capability nodes with dependency edges. schema_version guards future wire
// evolution (code_rules_v2 §6.1); only version 1 is accepted today.
type graphSubmissionRequest struct {
	SchemaVersion int `json:"schema_version"`
	// RunID is accepted for wire back-compat but IGNORED — the server always
	// generates the run id (see handleSubmitGraph) to guarantee task-id
	// uniqueness and cross-caller isolation.
	RunID string          `json:"run_id,omitempty"`
	Nodes []graphNodeSpec `json:"nodes"`
	Edges []graphEdgeSpec `json:"edges"`
}

// collabRunSeq makes server-generated run ids unique even within a single
// nanosecond tick (UnixNano is not collision-free under concurrency).
var collabRunSeq uint64

// handleSubmitGraph executes a submitted collaboration graph through the
// kernel fabric and returns each node's output. Validation happens BEFORE any
// task is created: unknown capabilities are rejected up front so callers get
// a precise error instead of a half-executed graph stuck on
// no-capable-candidate.
func (h *actionHandler) handleSubmitGraph(w http.ResponseWriter, r *http.Request, princ *ares_security.Principal) {
	w.Header().Set("Content-Type", "application/json")
	if h.kernel == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "peer runtime not active"})
		return
	}
	var req graphSubmissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	if req.SchemaVersion != 1 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": fmt.Sprintf("unsupported schema_version %d (want 1)", req.SchemaVersion)})
		return
	}
	if len(req.Nodes) == 0 || len(req.Nodes) > 1024 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": "nodes must be between 1 and 1024"})
		return
	}
	// Aligned with the sdk.Graph builder cap so both submission paths share
	// one semantic boundary (fusion plan).
	const maxSubmissionEdges = 4096
	if len(req.Edges) > maxSubmissionEdges {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": fmt.Sprintf("edges must not exceed %d", maxSubmissionEdges)})
		return
	}
	caps := map[string]bool{}
	for _, c := range h.kernel.scheduler.Capabilities() {
		caps[c] = true
	}
	for _, n := range req.Nodes {
		if !caps[n.Capability] {
			h.auditAction("submit_graph", n.Capability, princ, false)
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{
				"error":                  "no peer executor declares capability " + n.Capability,
				"available_capabilities": h.kernel.scheduler.Capabilities(),
			})
			return
		}
	}

	// The server ALWAYS generates the run id: caller-supplied ids colliding
	// with live/completed fabric tasks would surface as 500s (ErrTaskExists)
	// for what is a caller mistake — and across callers they have no
	// isolation. Callers correlate via the returned graph_id / task_ids.
	//
	// UnixNano alone is NOT unique — two submissions in the same nanosecond
	// (high concurrency, or coarse clock platforms) would collide and the
	// second's first Create would hit ErrTaskExists → a spurious 500. A
	// process-wide atomic sequence closes that window deterministically.
	runID := fmt.Sprintf("g%d-%d", time.Now().UnixNano(), atomic.AddUint64(&collabRunSeq, 1))
	outputs, taskIDs, err := runCollabGraph(r.Context(), h.kernel, runID, req.Nodes, req.Edges)
	status := http.StatusOK
	ok := err == nil
	if !ok {
		// Error taxonomy (see collab_graph.go) — each class is a distinct HTTP
		// status so callers can distinguish a mistake from a partial result
		// from a fault, and drive retry logic accordingly:
		//   ErrGraphInvalid    → 400  caller's DAG is malformed
		//   ErrGraphNodeFailed → 422  DAG ran, a node's work failed (don't
		//                             blindly retry — the graph was accepted)
		//   ErrGraphTimeout    → 504  DAG did not settle within the bound
		//   (default)          → 500  genuine infrastructure fault
		switch {
		case errors.Is(err, ErrGraphInvalid):
			status = http.StatusBadRequest
		case errors.Is(err, ErrGraphNodeFailed):
			status = http.StatusUnprocessableEntity
		case errors.Is(err, ErrGraphTimeout):
			status = http.StatusGatewayTimeout
		default:
			status = http.StatusInternalServerError
		}
	}
	h.auditAction("submit_graph", runID, princ, ok)
	w.WriteHeader(status)
	resp := map[string]any{
		"graph_id": runID,
		"task_ids": taskIDs,
		"outputs":  outputs,
		"success":  ok,
	}
	if !ok {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}
