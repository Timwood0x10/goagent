package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	api_tools "github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/agentfabric"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/ares_runtime"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/ares_shutdown"
	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/llm/output"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

// evolutionLifecycleForServe returns the wired evolution lifecycle, or nil
// when the evolution pipeline is not active. Both the control-plane snapshot
// endpoint and the actionHandler approval endpoint share one instance.
func evolutionLifecycleForServe(comp *ares_bootstrap.Components) *evolution.StrategyLifecycle {
	if comp == nil || comp.NewEvolution == nil {
		return nil
	}
	return comp.NewEvolution.Lifecycle
}

// setupServeControlPlane builds the runtime introspection control plane
// (monitoring.md Phase 4): the intelligence engine (health/anomalies/insights,
// migrated from internal/dashboard) and the read-only control server that
// serves the old monitoring /api/agents + /api/health surface. The old
// MonitorPlugin / tabs / PluginBus bridge are gone — the introspection panel
// (internal/introspect) is the single observability surface.
func setupServeControlPlane(
	ctx context.Context,
	g *errgroup.Group,
	cfg *ares_config.Config,
	cfgStore *ares_config.ConfigStore,
	store ares_events.EventStore,
	peerKernel *kernelHandle,
	obs *ares_bootstrap.ObservabilityProviders,
	flightRecorder *flight.FlightRecorder,
	lifecycle *evolution.StrategyLifecycle,
) (*introspect.Engine, *introspect.ControlServer, error) {
	// Intelligence engine: observes the shared event stream (fed by the
	// dedicated goroutine below, migrated from dashboard.EventBridge) to
	// score health / detect anomalies.
	intelEngine := introspect.NewEngine(nil)
	log.Printf("intelligence engine started: system=%s anomalies=%d",
		intelEngine.SystemHealth().Level, len(intelEngine.Anomalies()))

	// Feed the intelligence engine from the shared event store. Independent of
	// the introspect panel sink: this subscription only powers
	// health/anomalies/insights. Best-effort — a broken subscribe is logged,
	// the engine just stays empty (deny-by-default health).
	if store != nil {
		g.Go(func() error {
			ch, err := store.Subscribe(ctx, ares_events.EventFilter{})
			if err != nil {
				log.Printf("[intel] event subscribe failed: %v", err)
				return nil
			}
			for {
				select {
				case <-ctx.Done():
					return nil
				case evt, ok := <-ch:
					if !ok {
						// Subscription closed on store shutdown: stop instead
						// of busy-spinning on a closed channel returning nil.
						return nil
					}
					introspect.FeedIntel(intelEngine, evt)
				}
			}
		})
	}

	// Read-only control server: /api/agents, /api/agents/:id, /api/health,
	// /api/anomalies, /api/insights. Agent source comes from the peer kernel's
	// agent fabric when the full kernel exists; otherwise the endpoints report
	// 503 (partial paths must still compile and serve).
	var agentsSource introspect.AgentSource
	if peerKernel != nil && peerKernel.agents != nil {
		agentsSource = &fabricAgentSource{fabric: peerKernel.agents}
	}
	var cfgOpt introspect.ControlServerOption
	if cfgStore != nil {
		cfgOpt = introspect.WithRuntimeConfig(func() (any, []map[string]any) {
			cfg := cfgStore.Current().Redacted()
			history := cfgStore.History()
			out := make([]map[string]any, 0, len(history))
			for _, h := range history {
				out = append(out, map[string]any{
					"time":    h.Time,
					"ok":      h.OK,
					"message": h.Message,
				})
			}
			return cfg, out
		})
	}
	opts := []introspect.ControlServerOption{
		introspect.WithIntel(intelEngine),
		cfgOpt,
	}
	// M3/M4 observability (migrated from the deleted dashboard :8090 server):
	// evolution trajectory / human feedback / cross-Fabric spans.
	if obs != nil {
		opts = append(opts, obs.IntrospectOptions()...)
	}
	// Flight-recorder read surfaces (migrated from the deleted dashboard
	// /flight/* endpoints): timeline / summary / graph / decisions /
	// diagnostics / genealogy.
	if flightRecorder != nil {
		opts = append(opts, introspect.WithFlight(introspect.NewFlightRecorderAdapter(flightRecorder)))
	}
	// P2-2: evolution lifecycle state snapshot at /api/evolution/lifecycle.
	if lifecycle != nil {
		opts = append(opts, introspect.WithLifecycleSnapshot(lifecycle))
	}
	server := introspect.NewControlServer(agentsSource, opts...)
	return intelEngine, server, nil
}

// fabricAgentSource adapts *agentfabric.Fabric to introspect.AgentSource so
// the control plane lists the live fabric population.
type fabricAgentSource struct {
	fabric *agentfabric.Fabric
}

// ListAgents implements introspect.AgentSource.
func (s *fabricAgentSource) ListAgents() []introspect.AgentView {
	views := s.fabric.AgentsView()
	out := make([]introspect.AgentView, 0, len(views))
	for _, v := range views {
		row := introspect.AgentView{
			ID:     v.Identity,
			Name:   v.Identity,
			Status: string(v.State),
		}
		if len(v.Capabilities) > 0 {
			row.Role = v.Capabilities[0]
		}
		out = append(out, row)
	}
	return out
}

// startServeHTTPAndHooks builds the console HTTP server, starts it in the
// background, and registers the graceful-shutdown hooks (HTTP → MCP → runtime
// → flight recorder) now that those components are initialized. It returns
// the started server so the caller can assign it to its signal-handler
// closure for a graceful Ctrl+C shutdown.
func startServeHTTPAndHooks(
	ctx context.Context,
	g *errgroup.Group,
	cfg *ares_config.Config,
	cfgStore *ares_config.ConfigStore,
	controlServer *introspect.ControlServer,
	intelEngine *introspect.Engine,
	mgr *ares_runtime.Manager,
	registry *api_tools.Registry,
	toolBinder sub.ToolBinder,
	shutdownMgr *ares_shutdown.Manager,
	comp *ares_bootstrap.Components,
	peerKernel *kernelHandle,
) (*http.Server, error) {
	addr := serverBindAddr(cfg.Server.Host, cfg.Server.Port)
	fmt.Println("=== ARES Console — Live Runtime ===")
	fmt.Printf("Console:  http://%s/introspect\n", displayServeHost(cfg.Server.Host, addr))
	fmt.Printf("LLM:      %s / %s\n", cfg.LLM.Provider, cfg.LLM.Model)
	fmt.Printf("Tools:    %v\n", toolBinder.ListTools())
	fmt.Println("Press Ctrl+C to stop.")
	fmt.Println()

	// The introspect read side (/api/v1/introspect/*) carries task payloads
	// with no auth of its own; a wildcard bind exposes it to the network.
	// Fail-safe posture: warn loudly (do not block startup) so operators who
	// deliberately opt into 0.0.0.0 without auth still see the exposure.
	if isWildcardHost(cfg.Server.Host) && !cfg.Security.AuthEnabled {
		log.Printf("WARNING: server.host %q binds all interfaces while security.auth_enabled is false — "+
			"the unauthenticated introspect read API (/api/v1/introspect/*) is reachable from the network; "+
			"set security.auth_enabled or bind localhost", cfg.Server.Host)
	}

	// API key for destructive endpoints (agents/chaos/tools). When empty,
	// all destructive requests are denied (deny-by-default). Configure via
	// ARES_API_KEY environment variable.
	serveAPIKey := os.Getenv("ARES_API_KEY")
	// One shared audit sink for the actionHandler, so auth decisions and
	// destructive actions land in the same process log stream.
	auditLogger := ares_security.NewAuditLogger(slog.Default())

	// The actionHandler intercepts agent/chaos/tool/MCP routes BEFORE the
	// read-only control server (introspect.ControlServer), so it must carry
	// the same credentials and audit sink. JWT is enabled when configured.
	// authMW enforces WRITE on destructive endpoints; readAuthMW enforces
	// READ on the JSON read surfaces (T7: introspect feed, tool inventories,
	// cost API) so enabling auth closes those too — not just the mutators.
	var authMW *ares_security.AuthMiddleware
	var readAuthMW *ares_security.AuthMiddleware
	if cfg.Security.AuthEnabled && cfg.Security.JWTSecret != "" {
		authMW = ares_security.NewAuthMiddleware([]byte(cfg.Security.JWTSecret), ares_security.PermWrite,
			ares_security.WithAudit(auditLogger))
		readAuthMW = ares_security.NewAuthMiddleware([]byte(cfg.Security.JWTSecret), ares_security.PermRead,
			ares_security.WithAudit(auditLogger))
	}
	handler := &actionHandler{
		inner: controlServer,
		// W1: LLM cost dashboard — the SAME instance the client's
		// MetricsTracer records into, so /api/v1/observability/* reflects
		// real LLM cost attribution (single source of truth). The mux is
		// built here too: serveIntrospect dereferences costMux whenever
		// cost is set, so the pair must be wired atomically.
		cost:     comp.LLM.CostDashboard,
		costMux:  buildCostMux(comp.LLM.CostDashboard),
		mgr:      mgr,
		tools:    registry,
		apiKey:   serveAPIKey,
		auth:     authMW,
		readAuth: readAuthMW,
		audit:    auditLogger,
		// Peer runtime kernel: powers the POST /api/tasks submission endpoint
		// (submitPeerTask).
		kernel: peerKernel,
		// Chaos emergency-stop credential (#12 Phase 2): POST /api/chaos/stop
		// requires a matching X-Chaos-Token header; empty disables the route.
		chaosStopToken: cfg.Kernel.Chaos.StopToken,
		// Runtime introspection panel (monitoring.md): UI + read API.
		intro: peerKernel.intro,
		// P2-4: evolution manual-approval gate (POST /api/evolution/approve).
		// Nil when the evolution pipeline is not wired — the endpoint then
		// reports 503 instead of silently swallowing approvals.
		lifecycle: evolutionLifecycleForServe(comp),
	}

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	// Start HTTP server; gracefully shut down on signal.
	g.Go(func() error {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTP server error: %w", err)
		}
		return nil
	})

	// Register graceful-shutdown hooks now that the server, MCP, and runtime
	// are initialized. Only the HTTP server stays here: MCP, Runtime, and
	// FlightRecorder teardown now lives in the System Runtime orchestrator
	// (Stage 9), which drives real Stop in reverse topological order during
	// the graceful shutdown sequence — removing the old duplicated teardown.
	if err := shutdownMgr.AddCallback(ares_shutdown.PhasePreShutdown, func(ctx context.Context) error {
		return httpSrv.Shutdown(ctx)
	}); err != nil {
		return nil, fmt.Errorf("register http shutdown hook: %w", err)
	}

	return httpSrv, nil
}

// shutdownSystemRuntime drives the System Runtime orchestration kernel through
// the same graceful shutdown so the managed component graph transitions to
// Stopped and the snapshot reflects the orderly teardown. Adapters now carry
// Stopper hooks (Stage 9), so the orchestrator stops MCP/Runtime/Flight in
// reverse topological order; nil guards keep this safe on the bootstrap-failure
// path. Extracted from runServe to keep its cyclomatic complexity within lint
// limits.
func shutdownSystemRuntime(compPtr *atomic.Pointer[ares_bootstrap.Components], ctx context.Context) {
	comp := compPtr.Load()
	if comp == nil || comp.SystemRuntime == nil {
		return
	}
	if err := comp.SystemRuntime.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "system_runtime shutdown error: %v\n", err)
	}
}

func loadServeConfig() (*ares_config.Config, error) {
	// Minimal setup: the user provides only the LLM endpoint (--llm-url) and
	// optionally the API key / model. Everything else — agents, memory, tools,
	// storage, kernel policy — is assembled by the runtime from defaults, so no
	// config file is required.
	if serveLLMURL != "" {
		cfg := ares_config.NewMinimalConfig(serveLLMURL, serveLLMKey, serveLLMModel)
		if serveHost != "" {
			cfg.Server.Host = serveHost
		}
		if servePort > 0 {
			cfg.Server.Port = servePort
		}
		log.Printf("serve: minimal config (llm-url only); runtime defaults for all subsystems")
		return cfg, nil
	}

	configPath := serveConfigPath
	if configPath == "" {
		for _, p := range []string{
			"ares.yaml",
			"./ares.yaml",
		} {
			if _, err := os.Stat(p); err == nil {
				configPath = p
				break
			}
		}
		if configPath == "" {
			configPath = "ares.yaml"
		}
		// Write the resolved path back so runServe's watcher starts for the
		// auto-detected config too (previously Watch only ran with an explicit
		// --config; hot-reload silently no-op'd on the default ares.yaml).
		serveConfigPath = configPath
	}

	cfg, err := ares_config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := ares_config.LoadFromEnv(cfg); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	// CLI flags win over env (SERVER_HOST/SERVER_PORT) and YAML: the explicit
	// argument is the most specific intent.
	if serveHost != "" {
		cfg.Server.Host = serveHost
	}
	if servePort > 0 {
		cfg.Server.Port = servePort
	}
	return cfg, nil
}

// validateServeConfig enforces the dependencies required by the full agent
// serving entry point before Bootstrap starts any component.
func validateServeConfig(cfg *ares_config.Config) error {
	if cfg == nil {
		return errors.New("serve: config is required")
	}
	return nil
}

// createLLMAdapterWithFallback creates an LLM adapter with fallback chain.
func createLLMAdapterWithFallback(cfg *ares_config.Config) (output.LLMAdapter, error) {
	factory := output.NewFactory()

	// Try primary
	primaryCfg := &output.Config{
		Provider:  cfg.LLM.Provider,
		APIKey:    cfg.LLM.APIKey,
		BaseURL:   cfg.LLM.BaseURL,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	}

	adapter, err := factory.Create(cfg.LLM.Provider, primaryCfg)
	if err == nil {
		log.Printf("LLM adapter created: provider=%s model=%s", cfg.LLM.Provider, cfg.LLM.Model)
		return adapter, nil
	}
	log.Printf("primary LLM failed, trying fallbacks: %v", err)

	// Try fallbacks from config
	for _, fb := range cfg.LLM.Fallbacks {
		fbCfg := &output.Config{
			Provider:  fb.Provider,
			APIKey:    fb.APIKey,
			BaseURL:   fb.BaseURL,
			Model:     fb.Model,
			Timeout:   fb.Timeout,
			MaxTokens: fb.MaxTokens,
		}
		if fbCfg.Provider == "" {
			fbCfg.Provider = "openai"
		}
		adapter, err = factory.Create(fbCfg.Provider, fbCfg)
		if err == nil {
			log.Printf("LLM fallback adapter created: provider=%s model=%s", fbCfg.Provider, fbCfg.Model)
			return adapter, nil
		}
		log.Printf("fallback LLM failed: provider=%s error=%v", fbCfg.Provider, err)
	}

	// Last resort: ollama local
	log.Print("all remote LLMs failed, falling back to local ollama")
	ollamaCfg := &output.Config{
		Provider:  "ollama",
		BaseURL:   "http://localhost:11434",
		Model:     "llama3.2",
		Timeout:   120,
		MaxTokens: 2048,
	}
	adapter, err = factory.Create("ollama", ollamaCfg)
	if err != nil {
		// Wrap the sentinel so callers can errors.Is(err, ErrNoLLMAdapter)
		// while still retaining the underlying adapter-creation error.
		return nil, fmt.Errorf("no LLM adapter available: %w (last attempt: %v)", ErrNoLLMAdapter, err)
	}
	log.Printf("LLM fallback to ollama: model=llama3.2")
	return adapter, nil
}

// ErrNoLLMAdapter is the sentinel returned by createLLMAdapterWithFallback when
// every configured provider (primary, fallbacks, and the local ollama last
// resort) fails to produce an adapter. Callers that need to distinguish "no
// LLM available" from other serve failures should use errors.Is(err,
// ErrNoLLMAdapter) — e.g. to surface a degraded-mode warning instead of a hard
// crash. (code_rules: prefer typed errors over string matching.)
var ErrNoLLMAdapter = errors.New("serve: no LLM adapter available")

// defaultServeHost is the fallback bind host when the config leaves
// server.host empty (a hand-built Config may skip setDefaults).
const defaultServeHost = "localhost"

// serverBindAddr resolves the HTTP listen address from the server config.
// The host is the real bind address (default "localhost"); empty falls back
// rather than silently widening to a wildcard bind.
func serverBindAddr(host string, port int) string {
	if host == "" {
		host = defaultServeHost
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// isWildcardHost reports whether host selects all network interfaces
// (the "0.0.0.0" wildcard; IPv6's "::" is also a wildcard form).
func isWildcardHost(host string) bool {
	switch host {
	case "0.0.0.0", "::":
		return true
	default:
		return false
	}
}

// displayServeHost picks the host to print on the startup console: a wildcard
// bind prints the loopback probe address, because connecting to 0.0.0.0
// directly does not work on every platform and the panel URL must be usable.
func displayServeHost(host, addr string) string {
	if isWildcardHost(host) {
		return "localhost:" + strconv.Itoa(portOf(addr))
	}
	return addr
}

// portOf extracts the numeric port from a host:port address.
func portOf(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}
