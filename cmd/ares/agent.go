// agent — merged CLI source: content, roughly in this order, comes from the
// former cmd/ares files: actions.go, peer_mode.go, peer_agents.go, tools.go,
// mcp.go, mcp_null.go, dag_execution.go, session_admission.go, collab_graph.go.
//
// actions.go's file-level //nolint:errcheck was intentionally not carried
// over: errcheck reports no hits here today — response writes are either
// checked (see writeJSON) or explicit `_, _ =` best-effort bodies.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	api_tools "github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/agentsyscall"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	llm "github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/llm/output"
	"github.com/Timwood0x10/ares/internal/runtime"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
	"github.com/Timwood0x10/ares/internal/tools/discovery"
	"github.com/Timwood0x10/ares/internal/tools/envcap"
	"github.com/Timwood0x10/ares/internal/tools/planner"
	builtintools "github.com/Timwood0x10/ares/internal/tools/resources/builtin"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
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
// sink (these paths were previously API-key-only and un-audited because
// actionHandler intercepted the gin routes).
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
	// lifecycle is the evolution StrategyLifecycle. It powers
	// POST /api/evolution/approve (manual gate release); nil disables the
	// endpoint with 503 "evolution lifecycle not active".
	lifecycle *evolution.StrategyLifecycle
	// cost serves the LLM cost dashboard API: /api/v1/observability/cost*
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
	// is safe only because serve defaults to a loopback bind. The panel
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

// checkAuthRead gates the JSON read surfaces at READ permission: a valid
// JWT with read permission or the legacy API key (a write key may read).
// When auth is not configured at all it allows the request — the same policy
// the introspect surface documented before, safe only under the loopback
// default bind. Returns false after writing the 401/403 response.
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
// Auth policy: the JSON feed — task payloads, raw events, live
// scheduler state — requires READ credentials whenever auth is configured
// (checkAuthRead); the panel HTML (/introspect), the root redirect and
// /metrics stay open (the UI carries no data itself; metrics follow the
// scraper convention). With auth unconfigured every read route is open,
// which is safe only under the loopback default bind.
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
		// JSON feed: task payloads and raw events — read-gated.
		if !h.checkAuthRead(w, r) {
			return true
		}
		h.intro.ServeHTTP(w, r)
		return true
	case path == "/metrics":
		// Prometheus scrape endpoint (the old :8090 dashboard server mounted
		// /metrics; re-mounted here so scraping the ARES runtime survives
		// the dashboard deletion).
		observability.MetricsHTTPHandler().ServeHTTP(w, r)
		return true
	case h.cost != nil && (strings.HasPrefix(path, "/api/v1/observability/cost") ||
		path == "/api/v1/observability/dashboard"):
		// LLM cost dashboard (read-only GET). The mux is built once via
		// buildCostMux in the construction literal — rebuilding per request
		// was pure waste. Cost data is read-gated too.
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

	// Limit request body on all POST endpoints to 1MB to prevent
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

	// Evolution governance: POST /api/evolution/approve (manual gate).
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

	// Tool API: GET /api/tools — read-gated: the tool inventory is
	// reconnaissance surface, so it requires READ credentials when auth is
	// configured (same policy as the introspect JSON feed).
	if r.Method == "GET" && path == "/api/tools" {
		if !h.checkAuthRead(w, r) {
			return
		}
		h.handleListTools(w)
		return
	}

	// MCP tool API (migrated from the old gin server into the actionHandler
	// so the control plane stays unified):
	//   GET  /api/mcp/tools           → list available tools (read-gated)
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

	// Collaboration graph submission: a caller posts
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
	// the introspect feed (one policy across equally sensitive read
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

// ── Evolution Governance ─────────────────────────────────

// handleEvolutionApprove promotes the candidate held in SHADOW by the
// gates.require_manual_approval manual gate. Submit only HOLDS the
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
		// Map error to proper HTTP status; don't leak raw err.Error().
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

	// RBAC: destructive chaos (random-kill/kill-all/recover) requires the
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
		// Emergency stop for the live chaos loop. The
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
		// Unified lifecycle: when the peer kernel exists, kill a fabric
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
		// audit reflects whether ALL agents were stopped, not a blanket true.
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
		// audit reflects whether ALL down agents were recovered, not a blanket true.
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

// ── MCP Tool API (migrated from internal/monitoring) ──

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
// evolution; only version 1 is accepted today.
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
	// one semantic boundary.
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

// peerTaskSeq is a monotonic sequence for peer-mode task IDs (the old tracker
// counter hack is gone: the shared LoadTracker is scheduler-internal now).
var peerTaskSeq atomic.Int64

// normalizedPeers resolves the flat peer population from config. The
// agents.peers structure is the DEFAULT; when it is empty (legacy config),
// the legacy agents.sub entries are normalized into peers (each sub's single
// Type becomes its only capability). Returns an empty slice when neither is
// configured (the caller reports it as an error).
func normalizedPeers(cfg *ares_config.Config) []ares_config.PeerAgentConfig {
	if len(cfg.Agents.Peers) > 0 {
		return cfg.Agents.Peers
	}
	peers := make([]ares_config.PeerAgentConfig, 0, len(cfg.Agents.Sub))
	for _, s := range cfg.Agents.Sub {
		peers = append(peers, ares_config.PeerAgentConfig{
			ID:           s.ID,
			Capabilities: []string{s.Type},
			Priority:     s.Priority,
		})
	}
	return peers
}

// createPeerAgents builds a set of peer agents WITHOUT a Leader ("Leader OFF"
// startup mode): a group of equal agents competes for tasks via
// capability-based scheduling, with no privileged orchestrator. Each
// configured sub-agent is spawned into the Agent Fabric WITH its execution
// body (the shared L2 router cognition) and its distilled experience prior,
// so the scheduler's candidate pool — queried live from the fabric — is
// exactly the set of real, executable agents. There is no second
// registration table to keep in sync: spawn/kill take effect on the next
// scheduler drain.
//
// The spawn_agent / create_task syscalls are wired into the shared ToolBinder
// so every agent can autonomously decide to decompose work and spawn peers.
// The Kernel enforces quota/capability validation on every spawn.
//
//nolint:gocyclo // createPeerAgents is a wiring hub (like runServe): it assembles the peer-mode kernel from Task Fabric, Agent Fabric, scheduler, evolution feedback, syscalls, recovery and the lifecycle in one function. Each branch is a distinct wiring step; splitting it would spread one assembly across helpers without reducing the decisions.
func createPeerAgents(
	ctx context.Context,
	cfg *ares_config.Config,
	comp *ares_bootstrap.Components,
	llmAdapter output.LLMAdapter,
	chatClient sub.ChatClient,
	toolBinder sub.ToolBinder,
	store ares_events.EventStore,
	strategySrc agents.StrategySource,
	expRepo repositories.ExperienceRepositoryInterface,
) ([]sub.Agent, *kernelHandle, error) {
	kernel := &kernelHandle{}

	// The flat Peers structure is the DEFAULT agent source; the legacy
	// Sub structure remains as the fallback so older configs keep working.
	peers := normalizedPeers(cfg)

	// Build sub-agent identities from the flat peer population.
	subAgents := createPeerSubAgents(peers, store)

	// Roles have no consumer anymore (the executor role-pinning and
	// the chat body that read them are both deleted) — peers run roleless.
	if len(subAgents) == 0 {
		return nil, nil, errors.New("peer mode: no peer agents configured (agents.peers or agents.sub)")
	}

	// Assemble the Kernel: Task Fabric + Agent Fabric + scheduler. This
	// mirrors flipKernelToTaskFabric but runs directly at startup (no
	// legacy path to flip from).
	kernel.fabric = taskfabric.NewFabric()
	// Stamp every submitted task with the strategy that was active at
	// submission time (evolution loop closure), so runtime fitness samples
	// stay attributed to the strategy that produced them across promotes.
	// Cheap + non-blocking: one store read per Create on the submission path.
	if strategySrc != nil {
		kernel.fabric = kernel.fabric.WithStrategyStamp(func() string {
			stampCtx, stampCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer stampCancel()
			st, err := strategySrc.GetActiveStrategy(stampCtx)
			if err != nil || st == nil {
				return ""
			}
			return st.ID
		})
	}
	if store != nil {
		kernel.fabric = kernel.fabric.WithEventStore(store)
		// Rebuild in-memory tasks from the durable task.* log BEFORE the
		// scheduler starts draining: restoring after the first Acquire would
		// reset tasks created in this process lifetime. Fail-loud — silently
		// continuing would drop tasks the log says exist.
		if err := kernel.fabric.RestoreFromStore(ctx); err != nil {
			return nil, nil, fmt.Errorf("peer mode: restore task fabric from event store: %w", err)
		}
	}
	// Experience-derived confidence prior — recorded skill/task outcomes
	// sharpen scheduling when the same pattern recurs. Nil (skills disabled)
	// keeps declared confidences.
	if expSrc := resolveExperienceConfidence(comp); expSrc != nil {
		kernel.fabric = kernel.fabric.WithConfidenceSource(expSrc)
	}

	// The static sub.Agent executor pool is gone. Its entries were
	// dead in peer mode — the scheduler skips static registrations whenever
	// the agent fabric is wired (the fabric's live population is the single
	// candidate source) and recovery-bound tasks resolve through
	// RegisterExecutorForTask instead. The map stays non-nil because the
	// scheduler copies it at construction; an empty pool simply means
	// "fabric only", which the drain path was designed for.
	kernel.executors = make(map[string]CapabilityExecutor, len(subAgents))

	// Build the candidate list for the fabric dispatcher. The full declared
	// capability set (Caps) is offered to the scorer so a task matching ANY
	// capability is schedulable to the peer.
	subCaps := make([]subAgentCapability, 0, len(peers))
	for _, p := range peers {
		typ := ""
		if len(p.Capabilities) > 0 {
			typ = p.Capabilities[0]
		}
		subCaps = append(subCaps, subAgentCapability{ID: p.ID, Type: typ, Caps: append([]string(nil), p.Capabilities...)})
	}

	// Assemble the kernel dispatcher with the Task Fabric path as the active
	// path (no legacy leader track: the flag starts at PolicyTaskFabric).
	kernelDispatcher, kernelFlag := wireKernelDispatcher(subCaps)
	kernel.dual = kernelDispatcher
	kernel.flag = kernelFlag

	// One shared load tracker for the scheduler.
	tracker := newLoadTracker()
	kernel.tracker = tracker

	// Enable real Task Fabric execution (not scoring mode).
	enableKernelExecution(kernel.dual, kernel.fabric)

	// Start the scheduler.
	sched := NewKernelScheduler(kernel.fabric, kernel.executors, tracker)
	if store != nil {
		sched.WithEventStore(store)
	}
	// Honor the YAML kernel.max_concurrent (0/unset = auto). The old literal
	// WithMaxConcurrent(0) relied on the auto fallback, which stopped at
	// ExecutorCount() — empty by design in peer mode — and collapsed to 1,
	// so every drain ran ONE quantum at a time despite fabric candidates
	// existing. With the fixed fallback chain, 0 now means "parallelism =
	// live fabric candidates"; a positive value caps it explicitly.
	if cfg.Kernel.MaxConcurrent > 0 {
		sched.WithMaxConcurrent(cfg.Kernel.MaxConcurrent)
	}
	// Honor the YAML kernel.poll_interval. Previously the config field was
	// never injected — the scheduler always drained on the 500ms default.
	if d := parseKernelPollInterval(cfg.Kernel.PollInterval); d > 0 {
		sched.PollInterval = d
	}
	// Optional snappier leases for chaos/recovery demos (#panel): a dead
	// agent's tasks requeue after lease_ttl instead of the 5-minute default.
	if ttl := parseKernelLoopConfig(cfg).LeaseTTL; ttl > 0 {
		sched.WithTTL(ttl)
	}
	kernel.scheduler = sched
	kernel.flipped = true

	// Strategy-shadow runs replay-only. The real-execution A/B runner
	// (chat tool-loop quanta) died with ReAct; strategy judgment is
	// runtime fitness回灌 + canary metrics. The sampler's replay fallback needs
	// no feeder and no scheduler hook, so there is nothing to wire here.

	// Evolution feedback loop: record execution outcomes per agent +
	// capability, and periodically push the derived confidence back into the
	// tracker so the next Schedule prefers historically-successful executors.
	// The loop now also writes the zero-LLM deterministic score back
	// to the active strategy's Score field via the StrategyStore, so the
	// GA's fitness signal tracks real execution outcomes without any LLM
	// call.
	attribution := aresrecovery.NewExecutionAttribution()
	sched.WithAttribution(attribution)
	feedback := aresrecovery.NewEvolutionFeedbackAdapter(attribution, tracker)

	// Wire the zero-LLM score provider into the EvolutionScheduler so
	// task.completed/failed events feed the deterministic aggregate score
	// (from attribution) instead of the constant 1.0/0.0. The provider reads
	// the same attribution that the feedback loop writes to, so the score
	// window reflects real execution quality (latency, retries, recovery).
	if comp.Evolution != nil {
		if sched, ok := comp.Evolution.Scheduler.(*evolution.EvolutionScheduler); ok && sched != nil {
			sched.SetScoreProvider(
				aresrecovery.NewAttributionScoreProvider(attribution),
			)
		}
	}

	// Loop closure: make the "independent scorer wired" shadow gate real.
	// bootstrap_steps.go set DeterministicScorerEnabled=true so hasScorer passed
	// and the shadow gate was registered as "independent scorer wired" — but
	// buildShadowEvaluator only sets a shadow scorer when an LLM scorer exists.
	// With llmScorer==nil the evaluator's scorer stayed nil, the ShadowSampler
	// no-op'd, and the gate rejected every candidate fail-closed forever (a gate
	// that claims evidence but never gathers it).
	//
	// The scorer must DISCRIMINATE per strategy, otherwise the defect only
	// moves: one global attribution score returns the same number for the
	// candidate and the active strategy, every comparison is an exact tie
	// (ShadowWon requires shadow > active), the win rate is 0.0 and the gate
	// still rejects everything. So the evidence source is the ReplayScorer: each
	// strategy is scored by the mean of ITS OWN KindFitness records that the
	// RuntimeObserver already writes per finished task, read over a distinct
	// time window per comparison — real per-strategy evidence, zero LLM calls.
	// The attribution-derived deterministic score supplies the
	// cold-start prior for a strategy with no history in a window, so the same
	// execution quality the GA rewards also anchors the shadow comparison.
	if comp.NewEvolution != nil && comp.NewEvolution.ShadowEvaluator != nil {
		det := aresrecovery.NewDeterministicScorer()
		// The replay query limit is configurable (evolution.shadow.
		// replay_query_limit). Zero keeps the default (200) — a config that
		// never mentions it behaves exactly as before.
		replay := evolution.NewReplayScorer(comp.EvidenceStore, func() float64 {
			return det.ScoreAttribution(attribution)
		}, evolution.WithReplayQueryLimit(cfg.Evolution.Shadow.ReplayQueryLimit))
		// Without an evidence store replay degrades to prior-vs-prior, i.e.
		// the tie deadlock above. Leave the scorer unset in that case so the
		// shadow gate stays honestly fail-closed instead of judging on ties.
		if replay.HasStore() {
			comp.NewEvolution.ShadowEvaluator.SetShadowScorer(replay.Score)
		}
	}

	// Wrap the confidence-injection adapter with score write-back.
	// The strategyScoreAdapter bridges to evolution.StrategyStore without
	// creating a circular import (aresrecovery cannot import evolution).
	var scoreWriter aresrecovery.StrategyScoreWriter
	if comp.NewEvolution != nil {
		scoreWriter = newStrategyScoreAdapter(comp.NewEvolution.StrategyStore)
	}
	scoredFeedback := aresrecovery.NewScoredFeedbackAdapter(feedback, nil, scoreWriter)
	runBackground(ctx, comp, "evolution-feedback", func(loopCtx context.Context) error {
		aresrecovery.RunScoredFeedbackLoop(loopCtx, scoredFeedback, 10*time.Second)
		return nil
	})

	// Collaboration-graph janitor: reclaim terminal residue left by fail-fast
	// / timeout submissions off the hot path (per-submission cleanup handles
	// the common case; this catches siblings that were in-flight then).
	runBackground(ctx, comp, "collab-gc", func(loopCtx context.Context) error {
		runCollabGCLoop(loopCtx, kernel.fabric, 60*time.Second)
		return nil
	})

	// Assemble the Lifecycle pillar (agentfabric + aresrecovery).
	// Wire the agent-fabric lifecycle sink into the shared event bus (#panel
	// feedback): deaths/spawns/suspensions must reach the introspection feed
	// the moment they happen, not only via lease-expiry downstream. Mapping to
	// existing bus types keeps consumers uniform (spawned/resumed → started;
	// killed/suspended/retired → stopped with reason).
	agentBus := &fabricEventSink{store: store}
	agents := agentfabric.NewFabric().WithEventSink(agentBus)
	if len(cfg.Kernel.Resources) > 0 {
		agents = agents.WithResourceBudget(cfg.Kernel.Resources)
	}
	kernel.agents = agents

	// The DAG execution gate (kernel.dag_execution in config).
	// Zero/absent config = legacy ReAct behavior (chat cognition for every
	// peer, L2 machinery test-only).
	//
	// Single execution path — the router body is always built.
	// The planner needs session-scoped dependencies (registry, fabric reader)
	// that are constructed here.
	var peerRouter agentfabric.Cognition
	sessionReg := agentfabric.NewSessionRegistry()

	// Read the L1 ToolClass DAG from the evolution components so
	// the planner can check enabled/budget/prior before growing L2
	// tool nodes. Nil when no tools are registered (permissive).
	var l1DAG *engine.MutableDAG
	if comp.NewEvolution != nil {
		l1DAG = comp.NewEvolution.ToolClassDAG()
	}

	planner, err := agentfabric.NewPlannerCognition(agentfabric.PlannerDeps{
		ChatClient: chatClient, // sub.ChatClient satisfies agentfabric.ChatClient
		ToolBinder: toolBinder, // sub.ToolBinder satisfies agentfabric.ToolBinder
		Sessions:   sessionReg,
		Fabric:     kernel.fabric,
		L1DAG:      l1DAG,
		// The planner is the evolution strategy actuator after
		// ReAct — deployed prompt/params steer plan growth.
		StrategySource: strategySrc,
		// Operator-tunable growth-depth guard (0/absent = default).
		MaxDepth: resolveMaxPlanDepth(cfg.Kernel.DAGExecution),
		Logger:   slog.Default(),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("peer mode: create planner cognition: %w", err)
	}
	peerRouter = agentfabric.NewRouterCognitionWithPlanner(toolBinder, planner, sessionReg, slog.Default())

	// The registry is always wired, so the submission path always
	// admits sessions. There is no gate-off legacy mode anymore.
	kernel.sessionReg = sessionReg

	// Terminal-task reaper for L2 session tasks. Every grown node is
	// a fabric task and the fabric never self-harvests, so without this
	// loop the in-memory task map grows monotonically across a long-lived
	// serve (a known named cost). The registry is the keep-set authority: a
	// live session's tasks are its readable history (decision C) and are
	// never harvested; only tasks of released sessions die, after the
	// configured grace window.
	sessionReaper := taskfabric.NewReaperWithKeep(kernel.fabric, "sess/",
		resolveReaperGrace(cfg.Kernel.DAGExecution), sessionKeepSet(sessionReg))
	runBackground(ctx, comp, "l2-reaper", func(loopCtx context.Context) error {
		sessionReaper.Run(loopCtx.Done(), time.Minute)
		return nil
	})
	slog.InfoContext(ctx, "peer mode: L2 session task reaper wired",
		"grace", sessionReaper.GracePeriod())

	// Session idle-TTL sweeper. The keep-set only lets a session's
	// tasks die when the session itself dies, and the only death signals
	// were "answer completed" / "admission rolled back" — an abandoned
	// session (client gone, planner loop stuck, answer quantum dying
	// before its release) pinned its terminal tasks forever. Releasing on
	// idle turns the leak bound into TTL + reaper grace; active sessions
	// are untouchable because every quantum refreshes their last-access
	// through GetSession.
	idleTTL := resolveSessionIdleTTL(cfg.Kernel.DAGExecution)
	effectiveTTL := idleTTL
	if effectiveTTL <= 0 {
		effectiveTTL = agentfabric.DefaultSessionIdleTTL
	}
	runBackground(ctx, comp, "session-idle-ttl", func(loopCtx context.Context) error {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return nil
			case <-ticker.C:
				if ids := sessionReg.SweepExpired(idleTTL); len(ids) > 0 {
					slog.InfoContext(loopCtx, "peer mode: released idle sessions past TTL",
						"count", len(ids), "ttl", effectiveTTL, "sessions", ids)
				}
			}
		}
	})
	slog.InfoContext(ctx, "peer mode: session idle-TTL sweeper wired", "ttl", effectiveTTL)

	// answer-failure session release. The answer node is the session's
	// ONLY terminal exit: when the answer task itself dies terminally
	// (retry budget exhausted by a failing executor), no successor can
	// reference the session graph again, yet nothing on that path called
	// ReleaseSession — the idle TTL (above) was the sole cleanup, pinning
	// every terminal task of the dead session for the full 30min. This
	// subscription closes the loop: a terminal task.failed whose capability
	// is ares/answer releases the session immediately, so the reaper
	// harvests after the normal grace window instead of after the TTL.
	if store != nil {
		runBackground(ctx, comp, "answer-fail-release", func(loopCtx context.Context) error {
			ch, err := store.Subscribe(loopCtx, ares_events.EventFilter{
				Types: []ares_events.EventType{ares_events.EventTaskFailed},
			})
			if err != nil {
				slog.WarnContext(loopCtx, "peer mode: answer-fail release subscription failed, idle TTL remains the backstop", "error", err)
				return nil
			}
			for {
				select {
				case <-loopCtx.Done():
					return nil
				case ev, ok := <-ch:
					if !ok {
						return nil
					}
					releaseSessionOnAnswerFailure(loopCtx, sessionReg, ev)
				}
			}
		})
	}

	// Every peer advertises the single L2 capability set via
	// peerCapabilities below. There is no legacy partition anymore.

	// Configured sub-agents ARE the fabric's dynamic population — each is
	// spawned WITH its execution body (the shared L2 router cognition) and
	// its distilled experience prior, instead of living only in the
	// static executor
	// registry. The scheduler queries the fabric on every drain, so this
	// is the single registration point: a future kill/retire immediately
	// removes the candidate, and the recovery/chaos loops manage the SAME
	// population they recover.
	for _, sa := range subAgents {
		if sa == nil {
			continue
		}
		sa := sa // capture for the closure (spawn is synchronous, but keep the
		// loop-scoped binding local for the CognitionFactory below)
		if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
			Identity:     sa.ID(),
			Capabilities: peerCapabilities(toolBinder.ListTools()),
			// The execution body is always the L2 router — a fabric
			// agent is fully self-contained (LLM + tools), no sub.Agent
			// wrapper, no ReAct loop.
			CognitionFactory: func([]string) agentfabric.Cognition {
				return peerRouter
			},
			ExperiencePrior: loadExperiencePrior(ctx, expRepo, sa.ID()),
		}); err != nil {
			return nil, nil, fmt.Errorf("peer mode: spawn agent %q into fabric: %w", sa.ID(), err)
		}
	}

	policy := aresrecovery.DefaultRestartPolicy()
	if cfg.Kernel.MaxRestarts > 0 {
		policy.MaxRestarts = cfg.Kernel.MaxRestarts
	}
	kernel.recovery = aresrecovery.New(kernel.fabric, agents, policy)
	sched.WithGovernance(agents)
	// The scheduler's candidate pool includes every live, IDLE, executable
	// fabric agent — the configured peers spawned above, plus any spawned via
	// the spawn_agent syscall. Static registered executors (recovery-bound)
	// still win by skip logic in appendFabricCandidates.
	sched.WithAgentFabric(agents)

	// Wire the spawn_agent / create_task syscalls into the shared ToolBinder.
	// Every agent's LLM executor sees these tools alongside the built-in
	// tools, so it can autonomously decide to spawn peers and create tasks.
	kernelSyscall := agentsyscall.NewKernel(
		agents,
		kernel.fabric,
		func(agentID, capability string) agentsyscall.Executor {
			// Syscall-spawned peers execute through the L2 router —
			// the same body as configured peers. No ReAct executor.
			return &peerExecutorAdapter{id: agentID, typ: models.AgentType(capability), cog: peerRouter}
		},
		// No scheduler registration here. The static pool is
		// skipped whenever the agent fabric is wired, so registering
		// syscall-spawned agents was a no-op for normal drains — and the
		// agentsyscall.Executor half (the factory return above, which
		// powers spawn_agent/create_task/ask_agent) is untouched.
		func(string, agentsyscall.Executor) {},
		// Plan loops started via the create_plan loop option must be
		// bounded by the serve lifetime, not the individual tool call.
		agentsyscall.WithLoopLifetime(ctx),
	)
	agentsyscall.BindTools(toolBinder, kernelSyscall)
	// Retain the syscall Kernel on the kernel handle so the collaboration IPC
	// bridge (built later in setupPeerRegistry) can inject ipc.Send into
	// ask_agent (Step Y.2-ACT).
	kernel.syscalls = kernelSyscall
	log.Printf("peer mode: spawn_agent / create_task / ask_agent syscalls wired into tool binder")

	// Inject agent priorities into the tracker (thread priority).
	for _, p := range peers {
		if p.Priority > 0 {
			tracker.SetPriority(p.ID, p.Priority)
		}
	}

	// Start the scheduler and recovery loop. The recovery loop wires a REAL
	// executor factory (newPeerExecutor — full sub.Agent with LLM + tools) and
	// binds each replacement to exactly the task it was spawned for
	// (RegisterExecutorForTask), so a dead agent's task is resumed by a real
	// cognitive process — not a canned-success stub, and never at the expense
	// of a brand-new task.
	// Runtime plugin ecosystem closure: the PluginBus hooks the scheduler's
	// quantum boundary (observer/checkpoint/tool plugins observe every
	// Schedule→Acquire→RunQuantum). The adapter lives in runtime_bridge.go —
	// the kernel stays free of any runtime import (§0.3 dependency rule).
	// The loop knobs are parsed ONCE here and shared with the recovery loop
	// below (a second parse would waste work and risk drift).
	kernelLoopCfg := parseKernelLoopConfig(cfg)
	kernel.pluginBus = startPluginBus(ctx, store, sched, kernelLoopCfg)

	// The scheduler drain loop and the recovery loop run as managed
	// background loops and hand their lifecycle to the System Runtime
	// adapter (stop = cancel, wait = join the goroutine). The loop context
	// is pre-derived from the serve ctx — NOT from the context runBackground
	// passes — so the adopt-time Stop hook owns a cancel that works
	// independently of which managed pool ended up running the goroutine.
	schedCtx, schedCancel := context.WithCancel(ctx)
	schedDone := make(chan struct{})
	runBackground(ctx, comp, sysCompScheduler, func(context.Context) error {
		defer close(schedDone)
		sched.Run(schedCtx)
		return nil
	})
	kernel.schedulerStop = schedCancel
	kernel.schedulerDone = schedDone

	recCtx, recCancel := context.WithCancel(ctx)
	recDone := make(chan struct{})
	// Bind the scheduler's stale-winner hint to this recovery loop. When a
	// leased task's winner dies with no capable replacement, the scheduler
	// releases the task and kicks a sweep here, so the replacement execution
	// body is bound within one drain instead of one full lease TTL.
	recoveryKick, recoveryHint := newRecoveryKick()
	recoveryLoopCfg := kernelLoopCfg
	recoveryLoopCfg.RecoveryKick = recoveryKick
	sched.WithRecoveryHint(recoveryHint)
	runBackground(ctx, comp, sysCompRecovery, func(context.Context) error {
		defer close(recDone)
		runKernelRecoveryLoop(recCtx, store, kernel.recovery, recoveryLoopCfg,
			func(taskID, agentID string, executor CapabilityExecutor) {
				sched.RegisterExecutorForTask(taskID, agentID, executor)
			},
			func(agentID, capability string) CapabilityExecutor {
				// Recovery-bound tasks bypass the candidate pool,
				// so dispatch per task. Every task is L2 now — the router
				// serves all of them; the newPeerExecutor fallback below
				// is wiring-error insurance only (also cognition-backed,
				// never ReAct).
				if body := selectRecoveryBody(peerRouter, capability); body != nil {
					exec, err := newCognitionExecutor(agentID, models.AgentType(capability), body)
					if err == nil {
						return exec
					}
					slog.WarnContext(ctx, "peer mode: recovery executor L2 dispatch failed, falling back",
						"agent_id", agentID, "capability", capability, "error", err)
				}
				return newPeerExecutor(agentID, models.AgentType(capability), peerRouter)
			},
			sched.HasCapableExecutor,
		)
		return nil
	})
	kernel.recoveryStop = recCancel
	kernel.recoveryDone = recDone

	log.Printf("peer mode: %d peer agents registered, Kernel scheduler started (no leader)", len(subAgents))
	return subAgents, kernel, nil
}

// newPeerExecutor creates the sub.Agent identity for a dynamically spawned
// peer agent. The execution body is the shared L2 router (passed in) —
// a spawned agent is a real cognitive process, not a stub, and never ReAct.
func newPeerExecutor(
	agentID string,
	capability models.AgentType,
	cog agentfabric.Cognition,
) sub.Agent {
	handler := sub.NewMessageHandler(agentID)
	return sub.New(
		agentID,
		capability,
		&cognitionExecutor{id: agentID, typ: capability, cog: cog},
		handler,
		nil,
		nil,
		&sub.SubAgentConfig{
			Config: base.Config{
				ID:   agentID,
				Type: capability,
			},
			EnableTools: true,
		},
	)
}

// loadExperiencePrior loads the most recent distilled experience for the
// agent and returns it as the spawn prior (memory distill onto the agent
// lifecycle — async distillation feeds an experience
// store queried at spawn time and injected as a prior).
// The prior is injected as SpawnSpec.ExperiencePrior so the agent starts with
// reusable distilled experience as its cognitive context instead of a blank
// slate. Returns nil when the repo is unavailable, the agent has no distilled
// experience yet, or the query fails — a nil prior is the zero-value
// contract, never a startup error.
func loadExperiencePrior(ctx context.Context, expRepo repositories.ExperienceRepositoryInterface, agentID string) any {
	if expRepo == nil {
		return nil
	}
	exps, err := expRepo.ListByAgent(ctx, agentID, ares_events.DefaultTenantID, 1)
	if err != nil || len(exps) == 0 {
		return nil
	}
	exp := exps[0]
	return map[string]any{
		"type":        exp.Type,
		"problem":     exp.Problem,
		"solution":    exp.Solution,
		"constraints": exp.GetConstraints(),
	}
}

// submitPeerTask creates a task directly in the Task Fabric for the peer-agent
// runtime (no leader dispatch). This is the entry point for user-submitted
// work: the task enters READY and the Kernel scheduler picks it up via the
// normal Schedule → Acquire → RunQuantum path.
//
// It is exposed as POST /api/tasks on the serve HTTP layer (actionHandler),
// closing the user-submission loop: a request reaches the fabric and the
// scheduler executes it — no leader and no autopilot involved.
//
// Single execution path. EVERY submission becomes an L2 session task:
//   - session-less payloads are auto-admitted into a fresh session (the
//     capability argument is normalized to ares/plan with a warn log);
//   - the envelope always carries SessionID, so the planner's first quantum
//     finds a live graph and no session-less legacy task can exist.
//
// There is no legacy path anymore — a submission that cannot be admitted
// fails fast instead of degrading into an unrunnable task.
// planCapability is the submission capability in the single-L2-path world
// (every submitted task is the first plan quantum of its session).
const planCapability = "ares/plan"

// answerCapability is the terminal L2 node: the session's sole exit. A
// terminal failure of an ares/answer task means no successor can reach the
// session graph (the answer-failure release key, see
// releaseSessionOnAnswerFailure).
const answerCapability = "ares/answer"

func submitPeerTask(ctx context.Context, kernel *kernelHandle, capability string, payload map[string]any) (string, error) {
	if kernel == nil || kernel.fabric == nil {
		return "", errors.New("peer mode: kernel fabric not wired")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	// Normalize every submission onto the L2 session path.
	sessionID, _ := payload["session_id"].(string)
	prompt, _ := payload["input"].(string)
	if sessionID == "" {
		sessionID = fmt.Sprintf("sess-auto-%d", peerTaskSeq.Add(1))
		payload["session_id"] = sessionID
	}
	if capability != planCapability {
		slog.InfoContext(ctx, "peer mode: capability normalized to single L2 execution path",
			"from", capability, "to", planCapability, "session_id", sessionID)
		capability = planCapability
	}
	if err := ensureSessionAdmission(ctx, kernel, sessionID, prompt); err != nil {
		return "", err
	}
	taskID := fmt.Sprintf("peer-plan-%d", peerTaskSeq.Add(1))

	env := &taskfabric.CheckpointEnvelope{
		Payload: payload,
	}
	// SessionID is always stamped (auto-admitted above), so the
	// plannerCognition always finds a live per-session L2 graph.
	env.SessionID = sessionID
	task := &taskfabric.Task{
		ID:         taskID,
		Capability: capability,
		// Origin stays "" — this is a root task (user-submitted work), no
		// agent caller. Agent-created tasks get their Origin from the
		// create_task syscall's tool context (kernel.CallerID).
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
		Checkpoint:  env,
	}
	if err := kernel.fabric.Create(task); err != nil {
		return "", fmt.Errorf("peer mode: create task: %w", err)
	}
	log.Printf("peer mode: submitted task %q (%s) → READY", taskID, capability)
	return taskID, nil
}

// ── Kernel-path chaos (unified lifecycle) ───────────────────────────
//
// The /api/chaos/* endpoints previously killed agents through the legacy
// ares_runtime manager pool, which has its own resurrection semantics — a
// SECOND lifecycle next to the kernel's agentfabric + aresrecovery pair.
// These helpers retarget chaos at the kernel fabric so an injected death
// exercises the REAL recovery chain: agent.killed → lease expiry → requeue →
// replacement executor resumes from checkpoint. The legacy mgr path remains
// only as a fallback for non-peer deployments.

// chaosKillRandomFabric kills one uniformly-chosen LIVE agent in the kernel's
// Agent Fabric and returns its id. Agents already dead (killed earlier) are
// skipped; killing an empty fabric is a caller-visible error.
func chaosKillRandomFabric(ctx context.Context, k *kernelHandle) (string, error) {
	if k == nil || k.agents == nil {
		return "", errors.New("peer mode: kernel agent fabric not wired")
	}
	live := liveFabricAgents(k.agents)
	if len(live) == 0 {
		return "", errors.New("peer mode: no live agents in the fabric")
	}
	target := live[rand.Intn(len(live))]
	if err := k.agents.Kill(ctx, target); err != nil {
		return "", fmt.Errorf("peer mode: kill %s: %w", target, err)
	}
	log.Printf("peer mode: chaos killed agent %q — lease expiry + replacement recovery will follow", target)
	return target, nil
}

// chaosKillAllFabric kills every LIVE agent in the kernel's Agent Fabric.
// It returns separate killed/failed lists because chaos engineering cares
// precisely about what did NOT die: a per-agent Kill error is logged AND
// surfaced instead of being silently skipped. err != nil is reserved for
// "the fabric itself is not wired", mirroring chaosKillRandomFabric.
func chaosKillAllFabric(ctx context.Context, k *kernelHandle) (killed, failed []string, err error) {
	if k == nil || k.agents == nil {
		return nil, nil, errors.New("peer mode: kernel agent fabric not wired")
	}
	killed = make([]string, 0)
	failed = make([]string, 0)
	for _, id := range liveFabricAgents(k.agents) {
		if kerr := k.agents.Kill(ctx, id); kerr != nil {
			log.Printf("peer mode: chaos kill-all failed for %q: %v", id, kerr)
			failed = append(failed, id)
			continue
		}
		killed = append(killed, id)
	}
	return killed, failed, nil
}

// chaosRecoverSweep forces one recovery sweep over the kernel's task fabric:
// every expired-lease task is requeued to READY so the scheduler (and, when
// no capable executor remains, the replacement factory) can pick it up.
//
// The two outcomes are deliberately distinct: an unwired recovery subsystem
// is an ERROR (operators must never see success with zero work done when the
// sweeper does not exist), while an empty result is a NORMAL response
// meaning nothing had expired. Returns the requeued task ids — the kernel
// recovers TASKS, not agents, because agents are disposable cognition and
// tasks are durable intent.
func chaosRecoverSweep(k *kernelHandle) ([]string, error) {
	if k == nil || k.recovery == nil {
		return nil, errors.New("peer mode: kernel recovery not wired")
	}
	requeued := k.recovery.RequeueExpiredLeases()
	if len(requeued) > 0 {
		log.Printf("peer mode: chaos recover sweep requeued %d expired task(s)", len(requeued))
	}
	return requeued, nil
}

// liveFabricAgents lists fabric ids that still resolve to a live agent
// (Get errors after Kill).
func liveFabricAgents(agents *agentfabric.Fabric) []string {
	live := make([]string, 0)
	for _, id := range agents.Agents() {
		if _, err := agents.Get(id); err == nil {
			live = append(live, id)
		}
	}
	return live
}

// peerExecutorAdapter satisfies the agentsyscall.Executor interface over an
// agentfabric Cognition (the L2 router). It is the same field-for-field
// StepOutcome translation as cognitionExecutor, but for the syscall contract
// instead of the scheduler contract (the two StepOutcome types differ, so one
// struct cannot implement both).
// (interface defined at the consumer).
type peerExecutorAdapter struct {
	id  string
	typ models.AgentType
	cog agentfabric.Cognition
}

// ID returns the agent's ID.
func (a *peerExecutorAdapter) ID() string { return a.id }

// Type returns the agent's type.
func (a *peerExecutorAdapter) Type() models.AgentType { return a.typ }
func (a *peerExecutorAdapter) ExecuteStep(ctx context.Context, task *models.Task) (*agentsyscall.StepOutcome, error) {
	out, err := a.cog.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return &agentsyscall.StepOutcome{}, nil
	}
	return &agentsyscall.StepOutcome{
		Done:       out.Done,
		Checkpoint: out.Checkpoint,
		Result:     out.Result,
	}, nil
}

// fabricEventSink forwards agentfabric lifecycle records onto the shared
// ares_events bus so observability consumers (introspection feed, archive)
// see agent deaths and revivals in real time.
type fabricEventSink struct {
	store ares_events.EventStore
}

// Emit implements agentfabric.EventSink.
func (f *fabricEventSink) Emit(ctx context.Context, ev agentfabric.AgentEvent) error {
	if f == nil || f.store == nil {
		return nil
	}
	busType := ares_events.EventAgentStarted
	reason := string(ev.Type)
	switch ev.Type {
	case agentfabric.EventAgentSpawned, agentfabric.EventAgentResumed:
		reason = ""
	case agentfabric.EventAgentSuspended, agentfabric.EventAgentRetired,
		agentfabric.EventAgentKilled:
		busType = ares_events.EventAgentStopped
	}
	payload := map[string]any{
		"agent_id": ev.AgentID,
	}
	if reason != "" {
		payload["reason"] = reason
	}
	if ev.ParentID != "" {
		payload["parent"] = ev.ParentID
	}
	return f.store.Append(ctx, ev.AgentID, []*ares_events.Event{{
		Type:       busType,
		ModuleName: "agentfabric",
		Payload:    payload,
		Timestamp:  ev.At,
	}}, 0)
}

// resolveExperienceConfidence wires the skill catalog's experience store as
// the task fabric's confidence prior. A nil catalog (skills
// disabled) keeps the fabric's declared confidences untouched.
//
// Args:
//   - comp: the bootstrap components carrying the live skill catalog.
//
// Returns:
//   - taskfabric.ConfidenceSource: the catalog-backed prior, or nil.
func resolveExperienceConfidence(comp *ares_bootstrap.Components) taskfabric.ConfidenceSource {
	if comp == nil || comp.SkillCatalog == nil {
		return nil
	}
	return ares_skills.NewExperienceConfidenceSource(comp.SkillCatalog.Experience())
}

// cognitionExecutor adapts an agentfabric Cognition to every executor
// contract in play, so the translation lives exactly once:
//   - sub.TaskExecutor (plus subAgent's structural stepExecutor check):
//     Execute runs a single quantum and translates the outcome; completion
//     is driven by the scheduler draining quanta, never by looping here.
//     RegisterFallback is a no-op (no fallback loop exists anymore).
//   - kernel.CapabilityExecutor (recovery-bound tasks): ID/Type/ExecuteStep.
//     Done/Checkpoint/Result ride opaquely, so both chat resume checkpoints
//     and L2 planner quanta survive the boundary.
//
// A nil body fails loud: identity-only agents (peer registry shells) must
// never be driven. (The syscall contract needs its own struct — see
// peerExecutorAdapter above — because its StepOutcome type differs.)
type cognitionExecutor struct {
	id  string
	typ models.AgentType
	cog agentfabric.Cognition
}

// newCognitionExecutor builds a recovery-bound executor over the given
// execution body. A nil body is a wiring error, surfaced at construction.
func newCognitionExecutor(agentID string, capability models.AgentType, cog agentfabric.Cognition) (*cognitionExecutor, error) {
	if cog == nil {
		return nil, fmt.Errorf("peer mode: recovery executor %q has no execution body", agentID)
	}
	return &cognitionExecutor{id: agentID, typ: capability, cog: cog}, nil
}

// ID implements kernel.CapabilityExecutor.
func (e *cognitionExecutor) ID() string { return e.id }

// Type implements kernel.CapabilityExecutor.
func (e *cognitionExecutor) Type() models.AgentType { return e.typ }

// Execute implements sub.TaskExecutor: a single quantum through the wrapped
// cognition.
func (e *cognitionExecutor) Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error) {
	if e.cog == nil {
		return nil, fmt.Errorf("peer mode: executor %q has no execution body (identity-only agent must not be driven)", e.id)
	}
	out, err := e.cog.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	if out != nil && out.Done && out.Result != nil {
		return out.Result, nil
	}
	// Single-quantum pass-through: not done means the scheduler resumes it.
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.Success = false
	res.Reason = "quantum yielded; resume via scheduler"
	return res, nil
}

// RegisterFallback implements sub.TaskExecutor. No-op: no fallback loop.
func (e *cognitionExecutor) RegisterFallback(models.AgentType, sub.FallbackHandler) {
}

// ExecuteStep implements the quantum path shared by subAgent's structural
// stepExecutor check and kernel.CapabilityExecutor.
func (e *cognitionExecutor) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	if e.cog == nil {
		return nil, fmt.Errorf("peer mode: executor %q has no execution body (identity-only agent must not be driven)", e.id)
	}
	out, err := e.cog.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	return &sub.StepOutcome{Done: out.Done, Checkpoint: out.Checkpoint, Result: out.Result}, nil
}

// createPeerSubAgents builds the sub.Agent identities for the flat peer
// population (cfg.Agents.Peers). These are identity shells for the
// peer registry/IPC — execution flows through fabric-spawned router
// cognitions, so each shell carries a body-less adapter that fails loud if
// ever driven (it never is: the static scheduler pool is gone and one-shot
// Execute has no production callers).
//
// No heartbeat monitor, no message queue —
// the fabric owns scheduling and lifecycle.
func createPeerSubAgents(
	peers []ares_config.PeerAgentConfig,
	store ares_events.EventStore,
) []sub.Agent {
	agents := make([]sub.Agent, 0, len(peers))
	for _, p := range peers {
		typ := ""
		if len(p.Capabilities) > 0 {
			typ = p.Capabilities[0]
		}
		handler := sub.NewMessageHandler(p.ID)
		agent := sub.New(
			p.ID,
			models.AgentType(typ),
			&cognitionExecutor{id: p.ID},
			handler,
			nil, // message queue: the fabric owns scheduling; no AHP queue loop
			nil, // heartbeat monitor: no Process/Launch lifecycle in peer mode
			&sub.SubAgentConfig{
				Config: base.Config{
					ID:   p.ID,
					Type: models.AgentType(typ),
				},
				EnableTools: true,
			},
			sub.WithEventStore(store),
		)
		agents = append(agents, agent)
	}
	return agents
}

// createChatClient creates a FailoverClient from the LLM config for Chat API support.
func createChatClient(cfg *ares_config.Config) (sub.ChatClient, error) {
	configs := make([]*llm.Config, 0, 1+len(cfg.LLM.Fallbacks))
	configs = append(configs, &llm.Config{
		Provider:  cfg.LLM.Provider,
		APIKey:    cfg.LLM.APIKey,
		BaseURL:   cfg.LLM.BaseURL,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	for _, fb := range cfg.LLM.Fallbacks {
		provider := fb.Provider
		if provider == "" {
			provider = "openai"
		}
		configs = append(configs, &llm.Config{
			Provider:  provider,
			APIKey:    fb.APIKey,
			BaseURL:   fb.BaseURL,
			Model:     fb.Model,
			Timeout:   fb.Timeout,
			MaxTokens: fb.MaxTokens,
		})
	}

	timeout := time.Duration(cfg.LLM.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	rate := cfg.LLM.ScorerAPIRate
	burst := cfg.LLM.ScorerAPIBurst
	return llm.NewFailoverClient(configs, timeout, rate, burst)
}

// nativeToolsEnvVar names the comma-separated allowlist of host commands to
// discover and register as tools (primitive 7: native command discovery).
// Empty disables discovery so hosts without the commands degrade gracefully.
const nativeToolsEnvVar = "ARES_NATIVE_TOOLS"

// nativeToolsAllowlist parses the ARES_NATIVE_TOOLS env var into a cleaned
// allowlist of host command names. Returns an empty slice when unset/blank so
// callers disable native discovery gracefully. This is the single security
// boundary: only listed commands are ever probed or executed.
func nativeToolsAllowlist() []string {
	raw := strings.TrimSpace(os.Getenv(nativeToolsEnvVar))
	if raw == "" {
		return nil
	}
	allowlist := make([]string, 0)
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			allowlist = append(allowlist, name)
		}
	}
	return allowlist
}

// registerNativeTools probes the allowlisted host commands via `command -v` +
// `--help` and registers the ones present into the internal registry. Only
// commands explicitly listed in ARES_NATIVE_TOOLS are ever probed or executed
// (allowlist security boundary); non-existent commands are skipped.
func registerNativeTools(ctx context.Context, internalReg *core.Registry) error {
	allowlist := nativeToolsAllowlist()
	if len(allowlist) == 0 {
		return nil
	}

	d := discovery.NewDiscoverer(allowlist)
	tools, err := d.Discover(ctx)
	if err != nil {
		return fmt.Errorf("native tools: discover: %w", err)
	}
	registered := 0
	for _, t := range tools {
		if err := internalReg.Register(t); err != nil {
			fmt.Printf("native tool: failed to register %q: %v\n", t.Name(), err)
			continue
		}
		registered++
	}
	fmt.Printf("native tools registered: %d (allowlist: %v)\n", registered, allowlist)
	return nil
}

// newToolRegistry creates the public tool registry with built-in + custom tools.
// The file tool is sandboxed to ARES_WORKSPACE_DIR (or the current working
// directory if the env var is unset) to prevent path-traversal attacks.
func newToolRegistry() (*api_tools.Registry, error) {
	r := api_tools.NewRegistry()
	workspaceDir := os.Getenv("ARES_WORKSPACE_DIR")
	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
	}
	if err := api_tools.RegisterBuiltinTools(r, api_tools.WithFileSandboxDir(workspaceDir)); err != nil {
		return nil, err
	}
	return r, nil
}

// newToolBinder creates a sub.ToolBinder bridged from the internal core.Registry.
func newToolBinder(internalReg *core.Registry) sub.ToolBinder {
	binder := sub.NewToolBinder()
	binder.BridgeFromRegistry(internalReg)
	return binder
}

// registerCapabilitySearch wires the environment-capability searcher (envcap)
// as the `search_capabilities` tool and registers it into the internal
// registry — envcap.NewSearcher was previously constructed nowhere in serve.
// This completes the SKILLS progressive-disclosure story: the memory manager
// surfaces a resident skill block, and this tool lets the agent actively search
// across the environment's capabilities, returning name + one-line description
// with details loaded on demand.
//
// Two sources are wired: registered tools (the registry itself) and skills (the
// bootstrap-seeded registry). Native commands are deliberately NOT wired as a
// separate discovery source here because registerNativeTools has already
// registered each allowlisted command as a CommandTool in the same registry —
// so they surface through the registry (KindTool). Wiring a second Discoverer
// would double-list every command and re-probe the host (command -v + --help)
// on every search call.
//
// skillReg may be nil (skills disabled) — the searcher simply skips that source.
func registerCapabilitySearch(internalReg *core.Registry, skillReg *skills.Registry) error {
	searcher := envcap.NewSearcher(envcap.NewRegistryLister(internalReg), skillReg, nil)
	tool := envcap.NewSearchTool(searcher)
	if err := internalReg.Register(tool); err != nil {
		return fmt.Errorf("register capability search tool: %w", err)
	}
	return nil
}

// newPlannerBridge wires the capability planner into a ToolExecutionBridge.
// The bridge provides intent-based tool fallback when agents call unknown tools.
// If planner dependencies are missing, it returns nil (no bridge) gracefully.
func newPlannerBridge(internalReg *core.Registry) *planner.ToolExecutionBridge {
	// Create a tool provider from the registry and build the planner.
	provider := planner.NewRegistryProvider(internalReg)
	resolver, err := planner.NewToolResolver(provider)
	if err != nil {
		fmt.Printf("planner: resolver: %v\n", err)
		return nil
	}

	evStore := planner.NewMemoryEvidenceStore()
	p, err := planner.NewPlanner(
		planner.NewRuleBasedAnalyzer(),
		planner.NewCapabilityPlanner(),
		resolver,
		planner.NewEvidenceScorer(evStore),
		planner.NewExecutionPlanner(),
		evStore,
	)
	if err != nil {
		fmt.Printf("planner: new: %v\n", err)
		return nil
	}

	bridge, err := planner.NewToolExecutionBridge(internalReg, p, evStore)
	if err != nil {
		fmt.Printf("planner: bridge: %v\n", err)
		return nil
	}
	return bridge
}

// setupMCP registers builtin and MCP tools into the internal registry and
// bridges them into the public registry. It reuses the MCP manager created
// by Bootstrap (comp.MCP) instead of creating a second manager, so server
// connections are not duplicated and the single manager's Stop hook (already
// registered at shutdown) covers every connection.
func setupMCP(_ context.Context, mcpMgr *ares_mcp.MCPManager, registry *api_tools.Registry, deps builtintools.GeneralToolsDeps) (*core.Registry, error) {
	internalReg := core.NewRegistry()

	// Register builtin general tools into the internal registry so sub-agents
	// receive them through the ToolBinder (closure of the tools module).
	// Real backends (knowledge store adapter, memory manager, LLM client) are
	// injected via deps so the knowledge/memory/planning tools are usable,
	// not just nil-guarded.
	if err := builtintools.RegisterGeneralTools(internalReg, deps); err != nil {
		return internalReg, fmt.Errorf("register general tools: %w", err)
	}

	// Copy tools from the bootstrap-created MCP manager into the internal
	// registry so sub-agents and the dashboard see MCP tools. The manager was
	// already started by Bootstrap; no second manager is created here.
	if mcpMgr != nil {
		for _, tool := range mcpMgr.RegisteredTools() {
			t := tool
			if err := internalReg.Register(t); err != nil {
				fmt.Printf("MCP bridge: failed to register tool %s: %v\n", t.Name(), err)
			}
		}
	}

	// Bridge: register all internal tools (builtin + MCP) into the public
	// api/tools registry so the dashboard sees them regardless of whether MCP
	// servers are configured.
	for _, name := range internalReg.List() {
		tool, ok := internalReg.Get(name)
		if !ok || tool == nil {
			continue
		}
		t := tool
		if err := registry.Register(api_tools.ToolFunc{
			ToolName: t.Name(),
			ToolDesc: t.Description(),
			Fn: func(ctx context.Context, params map[string]any) (any, error) {
				res, err := t.Execute(ctx, params)
				if err != nil {
					return nil, err
				}
				return res.Data, nil
			},
		}); err != nil {
			fmt.Printf("MCP bridge: failed to register tool %s: %v\n", t.Name(), err)
		}
	}

	return internalReg, nil
}

var mcpNullCmd = &cobra.Command{
	Use:   "mcp-null",
	Short: "Start minimal MCP null server (stdio)",
	Long: `Starts a minimal MCP server with an echo tool over stdio transport.
Useful for demos and testing the MCP protocol without external tools.`,
}

var mcpNullServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP null server",
	RunE: func(cmd *cobra.Command, args []string) error {
		server := ares_mcp.NewMCPServer(
			ares_mcp.Implementation{Name: "ares_mcp-null", Version: "1.0.0"},
			ares_mcp.NewStdioServerTransport(),
		)

		echoSchema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"message": {"type": "string"}
			},
			"required": ["message"]
		}`)

		err := server.RegisterTool("echo", "Echoes back the input (no-op for demos)", echoSchema,
			func(ctx context.Context, args map[string]any) (*ares_mcp.ToolCallResult, error) {
				msg, _ := args["message"].(string)
				return &ares_mcp.ToolCallResult{
					Content: []ares_mcp.ContentBlock{
						{Type: "text", Text: fmt.Sprintf("ares_mcp-null: %s", msg)},
					},
				}, nil
			})
		if err != nil {
			return fmt.Errorf("register echo tool: %w", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

		sigEg, sigCtx := errgroup.WithContext(ctx)
		sigEg.Go(func() error {
			select {
			case <-sigCh:
				cancel()
				return nil
			case <-sigCtx.Done():
				return sigCtx.Err()
			}
		})

		if err := server.Serve(ctx); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		// server.Serve returned nil (clean shutdown); cancel the signal
		// context so sigEg.Wait() does not block forever waiting for a
		// signal that will never arrive.
		cancel()
		if err := sigEg.Wait(); err != nil {
			return fmt.Errorf("signal handler: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(mcpNullCmd)
	mcpNullCmd.AddCommand(mcpNullServeCmd)
}

// resolveMaxPlanDepth maps the configured plan-depth cap onto the planner's
// MaxDepth. Zero/negative means "planner default"
// (agentfabric.DefaultMaxPlanDepth): validation rejects negatives at load
// time, and the planner itself treats non-positive as default, so an invalid
// value can never widen or remove the guard even if it reaches the resolver.
func resolveMaxPlanDepth(c ares_config.DAGExecutionConfig) int {
	if c.MaxPlanDepth <= 0 {
		return agentfabric.DefaultMaxPlanDepth
	}
	return c.MaxPlanDepth
}

// resolveReaperGrace maps the configured terminal-task reaper grace onto a
// duration. Zero/absent passes through as 0, which the reaper itself
// defaults to 30s — the single default lives in taskfabric, not here. A
// negative cannot reach the resolver (Validate rejects it at load), and the
// reaper treats non-positive as its default, so a bad value can never
// disable the grace window.
func resolveReaperGrace(c ares_config.DAGExecutionConfig) time.Duration {
	if c.ReaperGrace <= 0 {
		return 0
	}
	return c.ReaperGrace
}

// resolveSessionIdleTTL maps the configured session idle TTL onto a
// duration. Zero/absent passes through as 0, which the registry sweep
// defaults to agentfabric.DefaultSessionIdleTTL — the single default lives
// in agentfabric, not here. A negative cannot reach the resolver (Validate
// rejects it at load), and the sweep treats non-positive as the default, so
// a bad value can never disable the TTL.
func resolveSessionIdleTTL(c ares_config.DAGExecutionConfig) time.Duration {
	if c.SessionIdleTTL <= 0 {
		return 0
	}
	return c.SessionIdleTTL
}

// peerCapabilities builds one peer's advertised capability set: the
// single L2 set (ares/root, ares/plan, ares/answer, tool/<name> per bound
// tool) and deliberately NOT the primary type — there is no legacy traffic
// anymore, so every peer serves the whole L2 set and the canary partition
// is retired with the gate.
func peerCapabilities(toolNames []string) []string {
	caps := []string{"ares/root", planCapability, answerCapability}
	for _, name := range toolNames {
		if name == "" {
			continue
		}
		caps = append(caps, "tool/"+name)
	}
	return caps
}

// selectRecoveryBody picks the recovery-bound execution body for one task
// (the L2 router for L2 session tasks, nil when there is no router
// or the capability is not L2-routable (caller falls back to a freshly
// built executor — post-D also cognition-backed, never ReAct).
// Recovery-bound tasks bypass the normal candidate pool, so the dispatch
// must happen here, per task, or a rescued task would run on the wrong body.
func selectRecoveryBody(router agentfabric.Cognition, capability string) agentfabric.Cognition {
	if router == nil {
		return nil
	}
	if !agentfabric.IsL2Capability(capability) {
		return nil
	}
	return router
}

// ensureSessionAdmission admits one L2 session before its first task is
// created: register the session graph, subscribe it to the shared
// incremental compiler, and compile the root task the planner's first
// quantum falls back to.
//
// The caller is submitPeerTask, and only when the request carries a
// session_id AND the gate wired a registry (nil registry = gate off =
// legacy path, session payloads stay envelope-only). Admission is idempotent:
// resubmitting into a live session is a multi-turn continuation, not an
// error — the existing session is reused and no duplicate root is compiled.
//
// Failures are fail-fast (nothing half-created): a session the caller asked
// for but we cannot admit must not silently degrade into an unrunnable
// task. Anything InitSession registered before the failure is released
// again, so a retry starts clean.
func ensureSessionAdmission(ctx context.Context, kernel *kernelHandle, sessionID, prompt string) error {
	if kernel == nil || sessionID == "" {
		return nil
	}
	// Single execution path. A session that cannot be admitted must
	// fail fast — a session-scoped task without a live graph is unrunnable.
	// (The old gate-off silent skip is gone with the gate.)
	if kernel.sessionReg == nil {
		return fmt.Errorf("peer mode: cannot admit session %q without a session registry", sessionID)
	}
	// A session ID containing "/" breaks the reaper keep-set —
	// SessionIDFromNode reverse-parses at the first slash, so "a/b" maps
	// its tasks back to a session "a" that is not live, and the reaper
	// harvests a LIVE session's readable history once the grace window
	// passes (the exact decision-C accident, triggered by pure client
	// input). Reject at the admission boundary, same level as the empty
	// ID; the registry enforces the same contract as a backstop.
	if strings.Contains(sessionID, "/") {
		return fmt.Errorf("peer mode: session id %q must not contain a slash", sessionID)
	}
	if _, err := kernel.sessionReg.GetSession(sessionID); err == nil {
		return nil
	} else if !errors.Is(err, agentfabric.ErrSessionNotFound) {
		return fmt.Errorf("peer mode: look up session %q: %w", sessionID, err)
	}
	if kernel.compileCoord == nil || kernel.fabric == nil {
		return fmt.Errorf("peer mode: cannot admit session %q without compile coordinator and fabric", sessionID)
	}

	// The compile subscription must outlive the submission request: tying it
	// to the request context would kill the projection the moment the HTTP
	// handler returns, while the session lives on.
	liveCtx := context.WithoutCancel(ctx)
	g, err := kernel.sessionReg.InitSession(liveCtx, sessionID, prompt, nil,
		func(subCtx context.Context, dag *engine.MutableDAG) (stop func()) {
			return kernel.compileCoord.SubscribeGraphEvents(subCtx, dag)
		})
	if err != nil {
		// A concurrent admitter may have won the race between our
		// GetSession and InitSession — re-check before failing.
		if errors.Is(err, agentfabric.ErrSessionAlreadyExists) {
			if _, err2 := kernel.sessionReg.GetSession(sessionID); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("peer mode: init session %q: %w", sessionID, err)
	}

	// Compile the root task the planner's first quantum reads (or falls
	// back to the payload input when still pending). An already-compiled
	// root means a retried admission after a partial failure — adopt it,
	// but ONLY while that root is still live (see below).
	rootStep := g.DAG().StepIndex()[g.Root()]
	if _, err := kernel.fabric.CompileNode(liveCtx, planprojection.ProjectStep(rootStep)); err != nil {
		if !errors.Is(err, taskfabric.ErrTaskExists) {
			releaseSessionQuietly(kernel, sessionID)
			return fmt.Errorf("peer mode: compile session %q root: %w", sessionID, err)
		}
		// An existing TERMINAL root does not belong to a retry — it
		// belongs to a previous session that already released under this
		// same ID (the natural client "continue the chat" behavior after
		// an answer). Adopting it would hand the new turn the old prompt
		// (rootCognition wrote its input into the envelope output) and let
		// same-named node tasks resolve to old tool outputs read as fresh
		// results — silently, with the keep-set then protecting the stale
		// tasks forever. The registry just told us this session is NOT
		// live, so no planner is reading those envelopes: harvest them
		// (the reaper's job, done early) and recompile clean.
		if stale, terr := kernel.fabric.Task(g.Root()); terr == nil &&
			(stale.State == taskfabric.StateCompleted || stale.State == taskfabric.StateFailed) {
			n := harvestReleasedSession(kernel.fabric, sessionID)
			slog.InfoContext(liveCtx, "peer mode: session re-admitted after release, harvested stale tasks before recompiling root",
				"session_id", sessionID, "harvested", n)
			if _, err := kernel.fabric.CompileNode(liveCtx, planprojection.ProjectStep(rootStep)); err != nil {
				releaseSessionQuietly(kernel, sessionID)
				return fmt.Errorf("peer mode: recompile session %q root: %w", sessionID, err)
			}
		}
	}
	slog.InfoContext(liveCtx, "peer mode: admitted L2 session",
		"session_id", sessionID, "root", g.Root())
	return nil
}

// harvestReleasedSession deletes every harvestable task under a released
// session's ID prefix: terminal (COMPLETED/FAILED) and READY tasks
// go; in-flight ones (LEASED/RUNNING/SUSPENDED) are refused by Delete and
// left for the reaper — they belong to work genuinely still running.
// Returns the number of tasks removed.
func harvestReleasedSession(fabric *taskfabric.Fabric, sessionID string) int {
	prefix := agentfabric.SessionTaskPrefix(sessionID)
	removed := 0
	for _, id := range fabric.IDs() {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if fabric.Delete(id) == nil {
			removed++
		}
	}
	return removed
}

// releaseSessionQuietly drops a half-admitted session during failure
// cleanup. The release itself is best-effort: the admission already failed,
// and a release miss only leaves a normal session behind for the reaper.
func releaseSessionQuietly(kernel *kernelHandle, sessionID string) {
	_ = kernel.sessionReg.ReleaseSession(sessionID)
}

// sessionKeepSet builds the reaper's keep predicate from the session
// registry: a task is kept while its owning session is still live.
// The registry is the single authority — an ID that parses as a session
// task but has no live session (released, or never admitted by this
// process) is harvestable once the grace window passes.
func sessionKeepSet(reg *agentfabric.SessionRegistry) func(taskID string) bool {
	return func(taskID string) bool {
		sid, ok := agentfabric.SessionIDFromNode(taskID)
		if !ok {
			return false
		}
		_, err := reg.GetSession(sid)
		return err == nil
	}
}

// releaseSessionOnAnswerFailure releases a session whose terminal answer
// task FAILED. The event payload carries the capability and the session id
// (taskfabric stamps both on must-persist events; task.failed is one), so
// the check is pure payload reading. Only the FAILED state releases: the
// requeue branch of fabric.Fail also records task.failed (state READY) while
// the retry budget still stands, and an answer that succeeds on retry must
// not lose its session. Only the answer node releases here: it is the
// session's sole terminal exit, so its terminal failure leaves the graph
// unreachable from any successor. A release miss (session already gone —
// released earlier, or reaped by the idle TTL) is logged, not an error: the
// postcondition — no live session — already holds.
func releaseSessionOnAnswerFailure(ctx context.Context, reg *agentfabric.SessionRegistry, ev *ares_events.Event) {
	if ev == nil || reg == nil {
		return
	}
	if c, _ := ev.Payload["capability"].(string); c != answerCapability {
		return
	}
	if s, _ := ev.Payload["state"].(string); taskfabric.TaskState(s) != taskfabric.StateFailed {
		return
	}
	sid, _ := ev.Payload["session_id"].(string)
	if strings.TrimSpace(sid) == "" {
		return
	}
	if err := reg.ReleaseSession(sid); err != nil {
		slog.WarnContext(ctx, "peer mode: answer-failure release found no live session",
			"session", sid, "error", err)
		return
	}
	slog.InfoContext(ctx, "peer mode: released session after terminal answer failure",
		"session", sid, "task_id", ev.StreamID)
}

// Collaboration graphs execute as KERNEL fabric tasks:
// every node is a durable task whose Dependencies express the edges, and the
// kernelscheduler drives them through the standard Schedule→Acquire→RunQuantum
// path. This is deliberately NOT a second engine — it is the same engine
// sdk.Graph compiles to, used directly because these fixed collaboration
// shapes (delegate = one node, pipeline = chain, orchestrate = fan-out with
// implicit join-by-dependencies) need no conditions or routing.

// graphNodeSpec is one executable vertex of a collaboration graph.
type graphNodeSpec struct {
	// ID is the caller-chosen node identifier (unique within the graph).
	ID string `json:"id"`
	// Capability selects which peer executor runs this node.
	Capability string `json:"capability"`
	// Input becomes the node task's payload["input"].
	Input any `json:"input,omitempty"`
}

// graphEdgeSpec is a directed dependency: to runs only after from completes.
type graphEdgeSpec struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// collabTimeout bounds one graph execution when the caller's ctx has no
// deadline (LLM-backed peers make an unbounded wait dangerous).
const collabTimeout = 10 * time.Minute

// Error taxonomy — the handler maps each class to a distinct HTTP status so
// callers can tell a mistake from a fault from a partial result:
//
//	ErrGraphInvalid    submission-time validation (bad DAG)      → 400
//	ErrGraphNodeFailed the DAG ran but a node's work failed      → 422
//	ErrGraphTimeout    the DAG did not settle within the bound   → 504
//	(anything else)    genuine infrastructure fault              → 500
//
// The distinction matters for caller retry logic: a 422 (a peer executor's
// work failed after exhausting retries) is NOT a server hiccup to blindly
// retry — the graph was accepted and executed; only the payload failed.
var (
	ErrGraphInvalid    = errors.New("collab graph invalid")
	ErrGraphNodeFailed = errors.New("collab graph node failed")
	ErrGraphTimeout    = errors.New("collab graph timed out")
)

// activeCollabRuns tracks run ids whose tasks are LIVE in this process, so the
// background janitor (runCollabGCLoop) can never harvest a run that is still
// executing. The invariant that makes this safe: a run reads its outputs and
// runs its own defer cleanup BEFORE it calls unmarkActiveRun (defer LIFO —
// unmark is registered first, cleanup second, so cleanup fires first). By the
// time a run unregisters, everything it still needed is already read; whatever
// terminal residue remains (in-flight siblings that finished after fail-fast/
// timeout) is exactly what the janitor should reclaim.
var activeCollabRuns sync.Map // runID → struct{}

func markActiveRun(id string)   { activeCollabRuns.Store(id, struct{}{}) }
func unmarkActiveRun(id string) { activeCollabRuns.Delete(id) }

// isProtectedByActiveRun reports whether id belongs to a run that is still
// executing in this process.
func isProtectedByActiveRun(id string) bool {
	protected := false
	activeCollabRuns.Range(func(rid, _ any) bool {
		if strings.HasPrefix(id, "collab-"+rid.(string)+"-") {
			protected = true
			return false
		}
		return true
	})
	return protected
}

// sweepStaleCollabTasks deletes leftover terminal tasks ("collab-" prefix):
// COMPLETED/FAILED/READY entries whose owning run already returned are pure
// garbage in a long-lived fabric. These residues arise only on fail-fast /
// timeout paths, where a run's in-flight siblings were undeletable at cleanup
// time and turned terminal afterwards. In-flight states (LEASED/RUNNING/
// SUSPENDED) are refused by Delete's guard and skipped; tasks of ACTIVE runs
// are skipped via activeCollabRuns so the janitor never races a live run.
//
// This runs on the BACKGROUND janitor (runCollabGCLoop), NOT on the submission
// hot path — a full IDs() scan must not tax every graph submission.
func sweepStaleCollabTasks(f *taskfabric.Fabric) int {
	removed := 0
	for _, id := range f.IDs() {
		if !strings.HasPrefix(id, "collab-") || isProtectedByActiveRun(id) {
			continue
		}
		tk, err := f.Task(id)
		if err != nil {
			continue
		}
		switch tk.State {
		case taskfabric.StateReady, taskfabric.StateCompleted, taskfabric.StateFailed:
			if derr := f.Delete(id); derr == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		log.Printf("peer mode: harvested %d stale collaboration task(s)", removed)
	}
	return removed
}

// runCollabGCLoop periodically harvests stale collaboration residue off the
// submission hot path. Submissions only clean up their OWN tasks (the defer in
// runCollabGraph); this loop reclaims the terminal siblings that a fail-fast /
// timeout left undeletable at that moment. It exits when ctx is cancelled.
func runCollabGCLoop(ctx context.Context, f *taskfabric.Fabric, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepStaleCollabTasks(f)
		}
	}
}

// runCollabGraph creates every node task in the kernel fabric and waits for
// the whole graph to settle, returning nodeID → textual output extracted from
// each task's completion checkpoint.
//
// Failure semantics: the first FAILED node aborts the wait and is returned as
// an error naming the node; sibling branches that already completed are still
// reported in the outputs map (partial results survive).
func runCollabGraph(ctx context.Context, k *kernelHandle, runID string, nodes []graphNodeSpec, edges []graphEdgeSpec) (outputs map[string]string, taskIDs map[string]string, err error) {
	if k == nil || k.fabric == nil {
		return nil, nil, errors.New("collab graph: kernel fabric not wired")
	}
	markActiveRun(runID)
	defer unmarkActiveRun(runID)

	ids := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.ID == "" || n.Capability == "" {
			return nil, nil, fmt.Errorf("%w: node requires id and capability", ErrGraphInvalid)
		}
		if ids[n.ID] {
			return nil, nil, fmt.Errorf("%w: duplicate node id %q", ErrGraphInvalid, n.ID)
		}
		ids[n.ID] = true
	}
	deps := make(map[string][]string, len(nodes))
	for _, e := range edges {
		if !ids[e.From] || !ids[e.To] {
			return nil, nil, fmt.Errorf("%w: edge %q→%q references an unknown node", ErrGraphInvalid, e.From, e.To)
		}
		deps[e.To] = append(deps[e.To], e.From)
	}
	if cyc := findCycle(ids, deps); cyc != "" {
		return nil, nil, fmt.Errorf("%w: dependency cycle involving %q", ErrGraphInvalid, cyc)
	}

	taskIDs = make(map[string]string, len(nodes)) // nodeID → taskID
	created := make([]string, 0, len(nodes))      // every task we Create, for cleanup
	defer func() {
		// Ephemeral lifecycle: submitted graphs must not leave
		// zombie entries in the long-lived fabric. Delete is best-effort —
		// in-flight (LEASED/RUNNING/SUSPENDED) tasks are refused by the guard
		// and finish naturally; their ids are unique so nothing collides.
		for _, tid := range created {
			if derr := k.fabric.Delete(tid); derr != nil && derr != taskfabric.ErrTaskNotFound {
				log.Printf("peer mode: cleanup %s: %v", tid, derr)
			}
		}
	}()
	for _, n := range nodes {
		tid := "collab-" + runID + "-" + n.ID
		taskIDs[n.ID] = tid
		// Dependencies are expressed in NODE ids in the submission wire
		// format but must reference real TASK ids in the fabric.
		nodeDeps := make([]string, 0, len(deps[n.ID]))
		for _, d := range deps[n.ID] {
			nodeDeps = append(nodeDeps, "collab-"+runID+"-"+d)
		}
		if err := k.fabric.Create(&taskfabric.Task{
			ID:           tid,
			Capability:   n.Capability,
			Dependencies: nodeDeps,
			// RetryPolicy.MaxRetries counts TOTAL attempts (taskfabric.CanRetry:
			// Attempts < MaxRetries), so 2 = first attempt + one retry. This is
			// the graph-submission default budget: cheap idempotent nodes absorb
			// one transient failure; per-node tuning (0 for expensive /
			// non-retryable capabilities, higher for jitter-sensitive ones) is a
			// future wire evolution and must ride the schema_version guard rather
			// than a silent magic number — see graphNodeSpec.
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
			Checkpoint: &taskfabric.CheckpointEnvelope{
				Payload: map[string]any{"input": n.Input},
			},
		}); err != nil {
			return nil, taskIDs, fmt.Errorf("collab graph %s: create node %q: %w", runID, n.ID, err)
		}
		created = append(created, tid)
	}
	log.Printf("peer mode: collaboration graph %s submitted (%d nodes)", runID, len(nodes))

	waitCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, collabTimeout)
		defer cancel()
	}

	outputs = make(map[string]string, len(nodes))
	pending := make([]string, 0, len(nodes))
	for _, n := range nodes {
		pending = append(pending, n.ID)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for len(pending) > 0 {
		progressed := false
		var failedNodes []string
		still := pending[:0]
		for _, nid := range pending {
			tk, err := k.fabric.Task(taskIDs[nid])
			if err != nil {
				return outputs, taskIDs, fmt.Errorf("collab graph %s: read node %q: %w", runID, nid, err)
			}
			switch tk.State {
			case taskfabric.StateCompleted:
				outputs[nid] = collabNodeOutput(tk)
				progressed = true
			case taskfabric.StateFailed:
				outputs[nid] = collabNodeOutput(tk)
				failedNodes = append(failedNodes, nid)
			default:
				still = append(still, nid)
			}
		}
		pending = still
		if len(failedNodes) > 0 {
			// Fail-fast: the deferred cleanup deletes every not-yet-
			// started READY sibling so it never runs. Quanta already RUNNING
			// finish naturally (cooperative model — no hard cancel exists);
			// their results go unread and their unique ids keep the fabric
			// collision-free.
			//
			// All nodes that FAILED in this scan are reported — a fan-out can
			// lose several workers in the same 20ms tick, and listing them all
			// (instead of whichever the scan happened to see last) keeps the
			// 422's error deterministic and actionable.
			names := strings.Join(failedNodes, ", ")
			return outputs, taskIDs, fmt.Errorf("%w: %s nodes [%s] failed", ErrGraphNodeFailed, runID, names)
		}
		if progressed {
			continue // re-scan immediately; more may have settled this instant
		}
		select {
		case <-waitCtx.Done():
			if errors.Is(waitCtx.Err(), context.Canceled) {
				return outputs, taskIDs, fmt.Errorf("collab graph %s: canceled: %w (%d/%d settled)",
					runID, waitCtx.Err(), len(outputs), len(nodes))
			}
			return outputs, taskIDs, fmt.Errorf("%w: %s (%d/%d nodes settled)",
				ErrGraphTimeout, runID, len(outputs), len(nodes))
		case <-ticker.C:
		}
	}
	return outputs, taskIDs, nil
}

// findCycle runs Kahn's topological sort over the dependency graph; any node
// left unresolved belongs to a cycle (or depends on one). Kernel-fabric
// dependencies are purely completion-driven, so an undetected cycle would
// park every member at READY until the caller times out — rejection at
// submission turns that runtime hang into a precise 400.
func findCycle(ids map[string]bool, deps map[string][]string) string {
	indegree := make(map[string]int, len(ids))
	adj := make(map[string][]string, len(ids))
	for id := range ids {
		indegree[id] = 0
	}
	for to, froms := range deps {
		indegree[to] += len(froms)
		for _, from := range froms {
			adj[from] = append(adj[from], to)
		}
	}
	queue := make([]string, 0, len(ids))
	for id, d := range indegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	resolved := 0
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		resolved++
		for _, nxt := range adj[id] {
			indegree[nxt]--
			if indegree[nxt] == 0 {
				queue = append(queue, nxt)
			}
		}
	}
	if resolved == len(ids) {
		return ""
	}
	for id, d := range indegree {
		if d > 0 {
			return id
		}
	}
	return ""
}

// collabNodeOutput extracts the executor's textual result from the completion
// checkpoint (the same envelope the dispatcher reads for reflux).
//
// API contract note: outputs carry ONLY the Reason summary text. Executors
// that place structured payloads under other checkpoint keys expose them via
// the task itself (query fabric.Task(id) + DecodeCheckpoint), not through
// this map — keep callers' expectations aligned with that boundary.
func collabNodeOutput(tk *taskfabric.Task) string {
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	if err != nil {
		return ""
	}
	step, ok := dc.StepCheckpoint.(map[string]any)
	if !ok {
		return ""
	}
	reason, _ := step["reason"].(string)
	return reason
}
