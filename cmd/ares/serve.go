// serve — merged CLI source: serve.go, serve_routine.go, serve_agents.go,
// serve_chaos.go, serve_live_dag.go, arena.go.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/peer"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	api_tools "github.com/Timwood0x10/ares/internal/apitools"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/ares_ratelimit"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/ares_shutdown"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	akf_mcp "github.com/Timwood0x10/ares/internal/knowledge/mcp"
	"github.com/Timwood0x10/ares/internal/llm/output"
	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/runtime"
	"github.com/Timwood0x10/ares/internal/runtime/archive"
	arena "github.com/Timwood0x10/ares/internal/runtime/arena"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	core_tools "github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// log is the package-level structured logger for the ares serve command.
var log = logger.Module("ares")

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start full agent monitoring with LLM + MCP + dashboard",
	Long: `Starts the full ARES peer-agent runtime with LLM integration,
MCP tools, and the monitoring dashboard.

Flags:
  --config  Path to config YAML (default: ares.yaml)
  --port    HTTP port for dashboard (overrides config)`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runServe()
	},
}

var (
	serveConfigPath string
	serveHost       string
	servePort       int
	serveLLMURL     string
	serveLLMKey     string
	serveLLMModel   string
)

func init() {
	rootCmd.AddCommand(serveCmd)
	serveCmd.Flags().StringVarP(&serveConfigPath, "config", "c", "", "Path to config YAML (optional; use --llm-url instead for minimal setup)")
	serveCmd.Flags().StringVar(&serveHost, "host", "", "HTTP bind address (overrides config; default 127.0.0.1 — use 0.0.0.0 to expose, requires auth)")
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 0, "HTTP port for dashboard (overrides config)")
	serveCmd.Flags().StringVar(&serveLLMURL, "llm-url", "", "LLM endpoint URL — minimal setup, no config file needed")
	serveCmd.Flags().StringVar(&serveLLMKey, "llm-api-key", "", "LLM API key (minimal setup)")
	serveCmd.Flags().StringVar(&serveLLMModel, "llm-model", "", "LLM model name (optional, provider default when empty)")
}

//nolint:gocyclo // runServe is the serve assembly hub; each step is extracted.
func runServe() error {
	// --- Config ---
	cfg, err := loadServeConfig()
	if err != nil {
		return err
	}
	if err := validateServeConfig(cfg); err != nil {
		return err
	}

	// --- Context with signal handling ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Graceful shutdown coordinator (internal/ares_shutdown). Real teardown
	// hooks (HTTP server, MCP, runtime) are registered below once those
	// components are initialized.
	shutdownMgr := ares_shutdown.NewManager(30 * time.Second)
	shutdownMgr.RegisterPhase(ares_shutdown.PhasePreShutdown, 5*time.Second)
	shutdownMgr.RegisterPhase(ares_shutdown.PhaseGraceful, 20*time.Second)
	shutdownMgr.RegisterPhase(ares_shutdown.PhaseForce, 5*time.Second)
	shutdownMgr.RegisterPhase(ares_shutdown.PhaseDone, 1*time.Second)

	g, ctx := errgroup.WithContext(ctx)
	// comp is assigned by Bootstrap below; the signal goroutine references it
	// for shutdown (WaitBackground + snapshot). The pointer is exchanged via
	// atomic.Store/Load so the goroutine never races with the Bootstrap
	// assignment on the main goroutine.
	var compPtr atomic.Pointer[ares_bootstrap.Components]
	g.Go(func() error {
		select {
		case <-sigCh:
			fmt.Println("\nShutting down...")
			// A second SIGINT/SIGTERM during the graceful shutdown forces an
			// immediate exit — a hung shutdown phase (or a stuck component)
			// must never trap the operator in an unstoppable process.
			// NOT adopted into the orchestrator: this watcher
			// must stay alive while the managed pools are being drained —
			// exactly the window when adopted loops are being torn down.
			// One-shot short task with its own recover boundary.
			go func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr, "force-exit watcher panicked: %v\n", r)
						os.Exit(1)
					}
				}()
				<-sigCh
				fmt.Fprintln(os.Stderr, "\nSecond signal received: forcing immediate exit")
				os.Exit(1)
			}()
			// Run the registered shutdown phases (HTTP → MCP → runtime) with a
			// bounded overall timeout. cancel() afterwards stops background
			// goroutines (event bridge, task submission) that wait on ctx.
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()
			if err := shutdownMgr.StartShutdown(shutdownCtx); err != nil {
				fmt.Fprintf(os.Stderr, "graceful shutdown error: %v\n", err)
			}
			// Give SystemRuntime Shutdown its own 15s budget so it
			// is not starved by phase callbacks that consumed the shared
			// 30s shutdownCtx. An expired context would skip MCP/Runtime/
			// FlightRecorder Stop, leaking goroutines and connections.
			sysRuntimeCtx, sysRuntimeCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer sysRuntimeCancel()
			shutdownSystemRuntime(&compPtr, sysRuntimeCtx)
			cancel()
		case <-ctx.Done():
		}
		comp := compPtr.Load()
		if comp == nil {
			return nil
		}
		// Record the pre-shutdown component snapshot for shutdown diagnostics
		// (which components were still running before background exit).
		if snapJSON, snapErr := comp.Snapshot().JSON(); snapErr == nil {
			log.Info("system_runtime snapshot (shutdown)", "snap_json", string(snapJSON))
		}
		// Wait for Bootstrap's background goroutines (distillation subscriber,
		// GA evolution ticker, LLM suggestion ticker) to exit after the
		// context is cancelled, so none outlives the graceful shutdown.
		comp.WaitBackground()
		return nil
	})

	// --- EventStore (Postgres when storage is configured, archive-enabled
	// memory otherwise) ---
	// Persistence contract (M4.1): when cfg.Storage points at Postgres, the
	// serve event stream is the durable events table via PostgresEventStore —
	// fitness evidence and the task.* log survive restarts, and the Task
	// Fabric folds them back on boot (createPeerAgents → RestoreFromStore).
	// PG construction failures are FATAL, mirroring Bootstrap's evidence-pool
	// posture: silently falling back to the in-memory store would make the
	// persistence feature lie about durability. Memory mode keeps the
	// archive-enabled compactable store (round_N.json archive +
	// compaction/trim) unchanged.
	serveStore, closeStore, err := newServeEventStore(cfg)
	if err != nil {
		return fmt.Errorf("create event store: %w", err)
	}

	// --- Bootstrap: infrastructure components via single wiring hub ---
	// Uses internal/ares_bootstrap for EventStore, Runtime, Memory.
	// MCP setup is handled separately below for registry bridging. The store
	// is passed via deps so Bootstrap wires Runtime/Memory against the real
	// serve store instead of creating a throwaway MemoryEventStore. On
	// success the store's shutdown is owned by the System Runtime (the
	// eventstore stop hook closes it in reverse-topological order).
	comp, err := ares_bootstrap.Bootstrap(ctx, cfg, &ares_bootstrap.BootstrapDeps{
		EventStore: serveStore,
	})
	if err != nil {
		// Bootstrap ran its own cleanups; the store came from serve, so its
		// resources (the PG pool) are released here — mirroring Bootstrap's
		// cleanup of its evidence pool on partial failure.
		_ = closeStore() // best-effort cleanup on the failure path
		return fmt.Errorf("bootstrap: %w", err)
	}
	// Publish the assembled components to the signal goroutine via the atomic
	// pointer so the shutdown snapshot/WaitBackground reads never race.
	compPtr.Store(comp)
	// Assembly-phase exit check — if a shutdown signal arrived during the
	// (potentially long) Bootstrap, abort the startup instead of proceeding
	// to wire components and start the runtime on a canceled context.
	if err := ctx.Err(); err != nil {
		log.Info("serve: shutdown was requested during assembly; aborting startup", "err", err)
		return normalizeShutdownErr(err)
	}
	store := comp.EventStore
	mgr := comp.Runtime

	// --- Runtime config store + hot-reload watcher ---
	// The store holds the last-good config and its reload history, served via
	// /runtime/config on the console HTTP server. When the serve command was
	// started with an explicit config file, an fsnotify watcher hot-reloads it
	// on change (failed reloads keep the previous config). With no config file
	// (minimal --llm-url mode) the watcher is skipped — the store still serves
	// the effective config snapshot.
	cfgStore := ares_config.NewConfigStore(cfg)
	if serveConfigPath != "" {
		cfgPath := serveConfigPath
		g.Go(func() error {
			// Watch blocks until ctx cancels; a reload error is logged inside
			// the store (recorded to history), so returning here is only for
			// watcher setup failures and ctx cancellation.
			return cfgStore.Watch(ctx, cfgPath)
		})
	}

	// EventStore is wired into Memory during Bootstrap,
	// not post-Bootstrap here. validateServeConfig has already enforced that
	// the full agent-serving entry point has its required Memory component.

	// Stage 1 observability: report the System Runtime component snapshot
	// (names, modes, lifecycle states) so operators can confirm which
	// components were assembled and reached Ready at startup.
	if snapJSON, snapErr := comp.Snapshot().JSON(); snapErr == nil {
		log.Info("system_runtime snapshot (startup)", "snap_json", string(snapJSON))
	} else {
		log.Warn("system_runtime snapshot unavailable", "err", snapErr)
	}

	// --- LLM adapter with fallback ---
	llmAdapter, err := createLLMAdapterWithFallback(cfg)
	if err != nil {
		return fmt.Errorf("create llm adapter: %w", err)
	}

	// --- Tool registry (public API) ---
	registry, err := newToolRegistry()
	if err != nil {
		return fmt.Errorf("create tool registry: %w", err)
	}

	// --- MCP servers: reuse the manager started by Bootstrap (single manager,
	// single set of connections; its Stop hook is registered below) and bridge
	// its tools into the internal + public registries. ---
	internalReg, err := setupMCP(ctx, comp.MCP, registry, ares_bootstrap.ToolDepsFromComponents(comp))
	if err != nil {
		return fmt.Errorf("MCP setup: %w", err)
	}

	// Register AKF (Knowledge Fabric) tools into the internal registry using
	// the shared KnowledgeRuntime from bootstrap. This is the critical wiring
	// that makes knowledge genome patches (ChangeBudget/ChangePlanner/
	// ChangeReducer) affect the actual runtime used by the agent's knowledge
	// tools — because both the evolution system's KnowledgePatchExecutor and
	// the agent's AKF tools share the same comp.KnowledgeRuntime instance.
	if comp.KnowledgeRuntime != nil {
		akfSvc := akf_mcp.NewAKFService(comp.KnowledgeRuntime, &compiler.DefaultCompiler{})
		for _, akfTool := range akfSvc.Tools() {
			t := akfTool // capture
			adapted := &akfToolAdapter{name: t.Name, desc: t.Description, fn: t.Execute}
			if err := internalReg.Register(adapted); err != nil {
				log.Warn("AKF: failed to register tool", "name", t.Name, "err", err)
			}
		}
		log.Info("AKF tools registered with shared KnowledgeRuntime", "count", len(akfSvc.Tools()))
	}

	// --- ToolBinder for agents ---
	// Primitive 7 wiring: probe host commands from the ARES_NATIVE_TOOLS
	// allowlist and register them into the internal registry (command -v +
	// --help; security boundary = allowlist only). Registered tools flow into
	// GetLLMTools naturally; SetActiveTools lets the runtime narrow the active
	// subset per task (progressive disclosure), and serve keeps the full set
	// active by default (zero-value behavior, no change to LLM tool injection).
	if err := registerNativeTools(ctx, internalReg); err != nil {
		return fmt.Errorf("register native tools: %w", err)
	}

	// Expose the environment-capability searcher as
	// the `search_capabilities` tool so agents can actively discover tools,
	// skills, and native commands. Registered before the binder is built so it
	// flows into the agent tool set naturally. comp.SkillsRegistry may be nil
	// (skills disabled) — the searcher skips that source.
	if err := registerCapabilitySearch(internalReg, comp.SkillsRegistry); err != nil {
		return fmt.Errorf("register capability search: %w", err)
	}

	// Expose the skill catalog as first-class agent tools
	// (skill_search / skill_load / ...) so the LLM can drive progressive
	// disclosure itself instead of only receiving the resident prompt block.
	if comp.SkillCatalog != nil {
		for _, t := range ares_skills.CatalogTools(comp.SkillCatalog) {
			if err := internalReg.Register(t); err != nil {
				log.Warn("serve: register skill tool skipped", "tool", t.Name(), "err", err)
			}
		}
		log.Info("serve: skill catalog tools registered (progressive disclosure active)")
	}

	toolBinder := newToolBinder(internalReg)
	log.Info("tools registered", "count", len(toolBinder.ListTools()))

	// --- Capability Planner bridge for agent tool fallback ---
	if bridge := newPlannerBridge(internalReg); bridge != nil {
		toolBinder.WithPlannerBridge(bridge)
		log.Info("planner bridge: attached")
	}

	// Step Y.3: arm the tool-call perception channel. The decorator wraps the
	// binder AFTER the planner bridge is attached, so planner-resolved calls are
	// measured too, and it is applied at the single site every execution body
	// receives its binder from — instrumenting the cognition loop instead
	// would miss calls resolved elsewhere. A nil
	// recorder (channel not armed — the default) returns the binder untouched.
	if comp.NewEvolution != nil && comp.NewEvolution.ChannelFeedback.ToolCallsArmed() {
		toolBinder = sub.ObserveToolCalls(toolBinder, comp.NewEvolution.ChannelFeedback)
		log.Info("serve: tool-call feedback channel armed (evolution reads tool outcomes)")
	}

	// --- ChatClient for native tool calling ---
	chatClient, err := createChatClient(cfg)
	if err != nil {
		return fmt.Errorf("create chat client: %w", err)
	}
	log.Info("chat client created", "provider", cfg.LLM.Provider, "model", cfg.LLM.Model)

	// --- Create + register agents with the runtime manager ---
	subAgents, peerKernel, err := createAndServeAgents(ctx, cfg, internalReg, llmAdapter, chatClient, toolBinder, comp, mgr)
	if err != nil {
		return err
	}

	// --- Peer registry: enable direct agent-to-agent messaging ---
	// setupPeerRegistry builds the registry; the kernel handle powers
	// collaboration-topic execution through the fabric DAG. The
	// registry is retained on the kernel handle so it stays reachable for
	// direct peer messaging / capability discovery instead of being discarded.
	reg, err := setupPeerRegistry(ctx, g, subAgents, comp, peerKernel)
	if err != nil {
		return err
	}
	if peerKernel != nil {
		peerKernel.peerRegistry = reg
		log.Info("serve: peer registry retained on kernel (agents)", "count", len(reg.IDs()))
	}

	// --- Runtime introspection control plane:
	// intelligence engine + read-only control server (extracted to
	// setupServeControlPlane to keep runServe's cyclomatic complexity within
	// gocyclo's 30 limit). The old MonitorPlugin/tabs/PluginBus bridge is gone.
	intelEngine, controlServer, err := setupServeControlPlane(ctx, g, cfg, cfgStore, store, peerKernel, comp.Dashboard, comp.FlightRecorder, evolutionLifecycleForServe(comp))
	if err != nil {
		return err
	}

	// --- Start runtime ---
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("start runtime: %w", err)
	}

	// Sub-agents are execution units only (ares-runtime: agents are not
	// orchestrated, they are scheduled). The Kernel owns dispatch: the
	// kernelScheduler drives each task through RunQuantum →
	// sub.Agent.ExecuteStep; agents never subscribe to the event stream and
	// self-dispatch (self-dispatch was removed).

	// --- Dashboard APIv2 server (observability read side) ---
	// The old standalone dashboard :8090 server was removed; the
	// observability providers now feed introspect.ControlServer below.

	// --- HTTP server + graceful-shutdown hooks (extracted to keep runServe
	// cyclomatic complexity within lint limits) ---
	if _, err := startServeHTTPAndHooks(ctx, g, cfg, cfgStore, controlServer, intelEngine, mgr, registry, toolBinder, shutdownMgr, comp, peerKernel); err != nil {
		return err
	}

	// Wait for all goroutines to complete (signal handler, bridge, tasks, HTTP).
	// A context cancellation (SIGINT/SIGTERM → graceful shutdown) surfaces as
	// context.Canceled from the errgroup; that is a NORMAL exit, not an error —
	// normalized to nil so `ares serve` exits 0 on Ctrl-C.
	return normalizeShutdownErr(g.Wait())
}

// normalizeShutdownErr treats context cancellation (graceful shutdown) as a
// clean exit: Ctrl-C is not a failure. Extracted so runServe stays within the
// cyclomatic-complexity limit.
func normalizeShutdownErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// newServeEventStore builds the serve event store plus a cleanup function.
//
// Postgres mode — cfg.Storage.Enabled && cfg.Storage.Host != "", the exact
// predicate Bootstrap uses for its evidence pool so one storage config drives
// every durable subsystem consistently — the event stream persists in the
// events table via PostgresEventStore, so fitness evidence, the task.* log
// and thus the restore path survive restarts. Construction fail-loudly
// rejects an unreachable database instead of silently degrading to memory: a
// silent fallback would make the persistence feature lie (the operator
// believes events survive restarts while they evaporate on exit).
//
// TODO(tech-debt) partially resolved: PG mode consciously keeps dropping the
// memory-mode round_N.json archive and compaction/trim. Archive: the compactable
// wrapper's round/lastArchivedVersion boundaries are in-memory and would re-archive the
// whole restored history over existing round files after a restart; and the
// archive exists to preserve rounds before TRIM deletes them, which never
// happens in PG mode (the table itself is the durable history — a round file
// would be a redundant copy, not a preservation). Compaction: PostgresEventStore
// implements no TrimAwareStore and no PG SummaryRepository exists, so the wrapper
// would summarize into a repo that dies on restart while never trimming — pure
// overhead on the append path. The retention follow-up IS wired:
// storage.events_retention_days registers an events-table retention cleaner
// with the bootstrap maintenance worker (the evidence store's ExpiryCleaners
// pattern), bounding the table's growth without an archive.
//
// Returns:
//   - store: the event store to inject via BootstrapDeps.EventStore.
//   - close: releases the store's resources (PG pool); never nil.
//   - err: wrapped construction failure.
func newServeEventStore(cfg *ares_config.Config) (ares_events.EventStore, func() error, error) {
	if cfg.Storage.Enabled && cfg.Storage.Host != "" {
		pgCfg := &postgres.Config{
			Host:     cfg.Storage.Host,
			Port:     cfg.Storage.Port,
			User:     cfg.Storage.Username,
			Password: cfg.Storage.Password,
			Database: cfg.Storage.Database,
			SSLMode:  cfg.Storage.SSLMode,
		}
		pool, err := postgres.NewPool(pgCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("event store: create postgres pool: %w", err)
		}
		store, err := ares_events.NewPostgresEventStore(pool)
		if err != nil {
			_ = pool.Close() // best-effort: the pool must not leak when store construction fails
			return nil, nil, fmt.Errorf("event store: create postgres store: %w", err)
		}
		log.Info("serve: event store = postgres (task/event stream persists across restarts; task fabric restores on boot)")
		return store, store.Close, nil
	}
	compactable, _, err := archive.NewCompactableStoreWithArchive(cfg.Memory.Archive)
	if err != nil {
		return nil, nil, err
	}
	return compactable, compactable.Close, nil
}

// akfToolAdapter adapts an AKF MCP tool (func(ctx, input string) -> string)
// to the core_tools.Tool interface so it can be registered in the internal
// tool registry and used by agents through the ToolBinder. This is the wiring
// that makes knowledge genome patches affect the agent's knowledge tools —
// because both share the same comp.KnowledgeRuntime instance.
type akfToolAdapter struct {
	name string
	desc string
	fn   func(ctx context.Context, input string) (string, error)
}

// Name returns the tool name.
func (a *akfToolAdapter) Name() string { return a.name }

// Description returns the tool description.
func (a *akfToolAdapter) Description() string { return a.desc }

// Category returns the tool category.
func (a *akfToolAdapter) Category() core_tools.ToolCategory { return core_tools.CategoryKnowledge }
func (a *akfToolAdapter) Capabilities() []core_tools.Capability {
	return []core_tools.Capability{core_tools.CapabilityKnowledge}
}
func (a *akfToolAdapter) Parameters() *core_tools.ParameterSchema { return nil }
func (a *akfToolAdapter) Execute(ctx context.Context, params map[string]interface{}) (core_tools.Result, error) {
	input, _ := params["input"].(string)
	if input == "" {
		// Serialize the whole params map as JSON input.
		b, _ := json.Marshal(params)
		input = string(b)
	}
	out, err := a.fn(ctx, input)
	if err != nil {
		return core_tools.NewErrorResult(err.Error()), nil
	}
	return core_tools.NewResult(true, map[string]interface{}{"output": out}), nil
}

// evolutionLifecycleForServe returns the wired evolution lifecycle, or nil
// when the evolution pipeline is not active. Both the control-plane snapshot
// endpoint and the actionHandler approval endpoint share one instance.
func evolutionLifecycleForServe(comp *ares_bootstrap.Components) *evolution.StrategyLifecycle {
	if comp == nil || comp.NewEvolution == nil {
		return nil
	}
	return comp.NewEvolution.Lifecycle
}

// setupServeControlPlane builds the runtime introspection control plane:
// the intelligence engine (health/anomalies/insights,
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
	log.Info("intelligence engine started", "level", intelEngine.SystemHealth().Level, "count", len(intelEngine.Anomalies()))

	// Feed the intelligence engine from the shared event store. Independent of
	// the introspect panel sink: this subscription only powers
	// health/anomalies/insights. Best-effort — a broken subscribe is logged,
	// the engine just stays empty (deny-by-default health).
	if store != nil {
		g.Go(func() error {
			ch, err := store.Subscribe(ctx, ares_events.EventFilter{})
			if err != nil {
				log.Warn("[intel] event subscribe failed", "err", err)
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
	// Observability (migrated from the deleted dashboard :8090 server):
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
	// Evolution lifecycle state snapshot at /api/evolution/lifecycle.
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
	mgr *runtime.Manager,
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
		log.Info("WARNING: server.host binds all interfaces while security.auth_enabled is false — the unauthenticated introspect read API (/api/v1/introspect/*) is reachable from the network; set security.auth_enabled or bind localhost", "host", cfg.Server.Host)
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
	// READ on the JSON read surfaces (introspect feed, tool inventories,
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
		// LLM cost dashboard — the SAME instance the client's
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
		// Chaos emergency-stop credential: POST /api/chaos/stop
		// requires a matching X-Chaos-Token header; empty disables the route.
		chaosStopToken: cfg.Kernel.Chaos.StopToken,
		// Runtime introspection panel (monitoring.md): UI + read API.
		intro: peerKernel.intro,
		// Evolution manual-approval gate (POST /api/evolution/approve).
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
		log.Info("serve: minimal config (llm-url only); runtime defaults for all subsystems")
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
		log.Info("LLM adapter created", "provider", cfg.LLM.Provider, "model", cfg.LLM.Model)
		return adapter, nil
	}
	log.Warn("primary LLM failed, trying fallbacks", "err", err)

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
			log.Info("LLM fallback adapter created", "provider", fbCfg.Provider, "model", fbCfg.Model)
			return adapter, nil
		}
		log.Warn("fallback LLM failed", "provider", fbCfg.Provider, "err", err)
	}
	// Last resort: ollama local
	log.Info("all remote LLMs failed, falling back to local ollama")
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
	log.Info("LLM fallback to ollama: model=llama3.2")
	return adapter, nil
}

// ErrNoLLMAdapter is the sentinel returned by createLLMAdapterWithFallback when
// every configured provider (primary, fallbacks, and the local ollama last
// resort) fails to produce an adapter. Callers that need to distinguish "no
// LLM available" from other serve failures should use errors.Is(err,
// ErrNoLLMAdapter) — e.g. to surface a degraded-mode warning instead of a hard
// crash. (Prefer typed errors over string matching.)
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

// createAndServeAgents builds and registers the flat peer-agent population with
// the runtime manager. This is the ONLY production serve path (the leader is
// removed): the configured peers spawn into the Agent Fabric as
// the dynamic population, the scheduler queries the fabric for candidates,
// and the spawn_agent / create_task syscalls are wired into the tool binder for
// autonomous decomposition. The peer kernel is returned so the serve HTTP layer
// can expose the task-submission endpoint (POST /api/tasks → submitPeerTask).
func createAndServeAgents(
	ctx context.Context,
	cfg *ares_config.Config,
	internalReg *core_tools.Registry,
	llmAdapter output.LLMAdapter,
	chatClient sub.ChatClient,
	toolBinder sub.ToolBinder,
	comp *ares_bootstrap.Components,
	mgr *runtime.Manager,
) ([]sub.Agent, *kernelHandle, error) {
	// The Bootstrap experience repo (nil when distillation is not wired) feeds
	// the spawn prior. The StrategySource closes the GA strategy loop: the
	// evolution system deploys the best-evolved strategy into
	// NewEvolution.StrategyStore, and the planner cognition reads it on every
	// growth quantum (the planner is the strategy actuator) — without this
	// bridge the deployed strategies were consumed by nothing.
	var strategySrc agents.StrategySource
	if comp.NewEvolution != nil {
		strategySrc = ares_bootstrap.NewStrategySource(comp.NewEvolution.StrategyStore)
		if strategySrc != nil {
			log.Info("serve: evolution strategy source wired into agents (GA deploy → runtime read)")
		}
	}

	// Inject the L1 ToolClass capability graph into the evolution
	// system BEFORE creating peer agents so the plannerCognition can
	// read enabled/budget/prior at L2 growth time. The L1 graph is NOT
	// compiled into taskfabric — it is a capability catalog, not an
	// execution plan (L1 ≠ L2).
	injectToolClassDAG(comp, toolBinder)

	subAgents, peerKernel, err := createPeerAgents(ctx, cfg, comp, llmAdapter, chatClient, toolBinder, comp.EventStore, strategySrc, comp.ExpRepo)
	if err != nil {
		return nil, nil, fmt.Errorf("create peer agents: %w", err)
	}
	// Register agents with the runtime manager.
	for _, sa := range subAgents {
		factory := func() base.Agent { return sa }
		mgr.RegisterAgent(sa, factory)
	}
	log.Info("serve: peer agents registered directly to Kernel", "count", len(subAgents))

	// Live-DAG injection (closes the evolution structure-patch loop): the
	// configured agent population IS the live workflow topology. Register it
	// on the runtime manager (under the shared live-DAG key) and swap it
	// into the evolution executors — without this, workflow/recovery patches
	// mutated the synthetic input→process→output bootstrap DAG forever and
	// "live promotion" was unobservable.
	if comp.NewEvolution != nil {
		liveDAG, dagErr := buildLiveAgentDAG(cfg)
		switch {
		case dagErr == nil:
			mgr.RegisterAgentDAG(runtime.AgentDAGLiveKey, liveDAG)
			if err := comp.NewEvolution.UpdateLiveDAG(liveDAG); err != nil {
				log.Warn("serve: live DAG injection failed (evolution keeps placeholder)", "err", err)
			} else {
				log.Info("serve: live agent DAG injected into evolution executors (nodes)", "count", len(liveDAG.Steps()))
			}

			// Wire the compile coordinator so DAG mutations
			// are projected into PlanSteps and compiled into the task
			// fabric — the single projection path closes the "two
			// graphs" gap. The coordinator subscribes to GraphEvents
			// so structural patches (Insert/Remove/AddEdge) trigger
			// recompilation without restart.
			if peerKernel != nil && peerKernel.fabric != nil {
				peerKernel.compileCoord = planprojection.NewCompileCoordinator(
					peerKernel.fabric, comp.EventStore,
				)
				if _, err := peerKernel.compileCoord.CompileDAG(ctx, liveDAG); err != nil {
					log.Warn("serve: initial DAG compile failed", "err", err)
				} else {
					log.Info("serve: live DAG compiled into task fabric")
				}
				peerKernel.compileCoord.SubscribeGraphEvents(ctx, liveDAG)

				// Wire the compile coordinator into the strategy
				// lifecycle so /api/evolution/lifecycle carries the
				// attribution triplet (generation, gates, compile_id).
				// The CompileCoordinator satisfies the
				// evolution.CompileInfoProvider interface directly (it
				// has CompileID/DAGVersion/CompileCount methods).
				if comp.NewEvolution != nil && comp.NewEvolution.Lifecycle != nil {
					comp.NewEvolution.Lifecycle.SetCompileInfoProvider(
						peerKernel.compileCoord,
					)
				}
			}
		case errors.Is(dagErr, errNoLiveAgentDAG):
			log.Info("serve: no peers configured; evolution keeps placeholder DAG")
		default:
			log.Warn("serve: live agent DAG build failed (evolution keeps placeholder)", "err", dagErr)
		}
	}

	// Evolution-aware quota loop: "Evolution decides; Kernel enforces".
	// The GA strategy store publishes a
	// quota.budget param; the quota manager pushes it into the Agent Fabric's
	// resource admission budget on a fixed cadence. Without this the
	// deployed budget was consumed by nothing — the fabric kept its startup
	// config budget forever. The loop is best-effort: a nil evolution store
	// yields a nil policy source, so Apply is a no-op that leaves the
	// configured cfg.Kernel.Resources budget untouched (backward compatible).
	if peerKernel != nil && peerKernel.agents != nil && comp.NewEvolution != nil {
		quotaSrc := ares_bootstrap.NewQuotaPolicySource(comp.NewEvolution.StrategyStore, cfg.Kernel.Resources)
		if quotaSrc != nil {
			quotaMgr := aresrecovery.NewEvolutionAwareQuotaManager(peerKernel.agents, quotaSrc)
			runBackground(ctx, comp, "evolution-quota", func(loopCtx context.Context) error {
				runKernelQuotaLoop(loopCtx, quotaMgr, parseKernelLoopConfig(cfg))
				return nil
			})
			log.Info("serve: evolution quota loop wired (GA budget → fabric P5 admission)")
		}

		// Evolution-aware spawn gate: "Evolution decides; Kernel enforces". The GA strategy store publishes
		// spawn.{enabled,max_concurrent,preferred_capabilities}; the spawner
		// enforces them so every RECOVERY replacement spawn honors the evolved
		// timing gate and capability preference (the population cap is bypassed
		// for recovery — a self-healing spawn must not be stranded by quota).
		// Without this, the deployed spawn policy was consumed by nothing and
		// recovery always used the plain fabric spawn. Best-effort: a nil store
		// yields a nil source, so WithSpawner is skipped (plain spawn).
		if peerKernel.recovery != nil {
			spawnSrc := ares_bootstrap.NewSpawnPolicySource(comp.NewEvolution.StrategyStore)
			if spawnSrc != nil {
				spawner := aresrecovery.NewEvolutionAwareSpawner(peerKernel.agents, spawnSrc)
				peerKernel.recovery.WithSpawner(spawner)
				log.Info("serve: evolution spawn gate wired (GA policy → recovery spawn enforcement)")
			}
		}

		// Evolution-aware population loop (runtime adaptation).
		// "Evolution decides; Kernel enforces": the GA
		// strategy store publishes population.{spawn,retire}; the adapter
		// applies the desired delta through the Agent Fabric's spawn/retire
		// primitives on a fixed cadence (idempotent — an empty policy is a
		// no-op). This is the missing top-level growth/shrink path: the spawn
		// gate (stage-2) only shapes RECOVERY replacements, whereas this loop
		// grows or shrinks the live population per the evolved topology.
		// Best-effort: a nil store yields a nil source, so the loop is skipped.
		popSrc := ares_bootstrap.NewPopulationPolicySource(comp.NewEvolution.StrategyStore)
		if popSrc != nil {
			popAdapter := aresrecovery.NewPopulationAdapter(peerKernel.agents, popSrc)
			runBackground(ctx, comp, "evolution-population", func(loopCtx context.Context) error {
				aresrecovery.RunKernelEvolutionLoop(loopCtx, popAdapter, 0, 0)
				return nil
			})
			log.Info("serve: evolution population loop wired (GA topology → fabric spawn/retire)")
		}
	}

	// Runtime introspection panel: a pull-only
	// collector refreshes the latest-wins store every 2s; actionHandler serves
	// the embedded UI at GET /introspect and JSON at /api/v1/introspect/*.
	// The chaos status source is a shared reporter the chaos loops
	// update — created here so the collector and wireChaos see the same frame.
	//
	// SECURITY: the introspect handler is unauthenticated and its eventstream
	// endpoint exposes raw event payloads (task inputs, checkpoints). Only
	// expose it on localhost/an internal network or behind an authenticating
	// reverse proxy — never bind it directly to a public address.
	chaosStatus := introspect.NewChaosReporter()
	if peerKernel.scheduler != nil && peerKernel.fabric != nil && peerKernel.agents != nil {
		store := &introspect.Store{}
		collabReporter := introspect.NewCollabReporter()
		collector := introspect.NewCollector(introspect.Sources{
			Kernel:    peerKernel.scheduler.Snapshot,
			Fabric:    peerKernel.fabric.LeaseSnapshot,
			Agents:    peerKernel.agents.AgentsView,
			Chaos:     chaosStatus.Snapshot,
			Tasks:     peerKernel.fabric.TaskSnapshot,
			Decisions: peerKernel.scheduler.DecisionsSnapshot,
			// Collab: no producer records collaboration edges today; the
			// reporter yields an empty graph. Wire a producer (e.g. the
			// spawn/collaboration IPC path) before enabling the panel tab.
			Collab: collabReporter.Snapshot,
		})
		peerKernel.intro = introspect.NewHandler(store).WithEventStore(comp.EventStore).
			// The panel snapshot also carries the System Runtime
			// component graph (kernel pillars + bootstrap infrastructure),
			// so a "false Ready" kernel is visible on the read surface. The
			// provider is read-only; the endpoint is read-gated like the
			// rest of the JSON feed.
			WithSystemRuntime(func() any { return comp.Snapshot() })
		sink := introspect.NewSink(store).WithCollab(collabReporter)
		comp.GoBackground(ctx, "introspect-sink", func(ctx context.Context) error {
			return sink.Run(ctx, comp.EventStore)
		})
		comp.GoBackground(ctx, "introspect-collector", func(ctx context.Context) error {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					store.Set(collector.Collect())
				}
			}
		})
		log.Info("serve: introspect panel wired (GET /introspect)")
	}

	// Wire the chaos subsystem. Default is shadow sandbox
	// (production zero-impact); live mode requires explicit config plus the
	// wired GA generation probe for the quiet window. The shared chaos status
	// reporter bridges the loops into the introspection panel.
	// comp is passed so the chaos loops run as managed background loops.
	wireChaos(ctx, comp, cfg, peerKernel, func() bool {
		if comp.NewEvolution == nil {
			return false
		}
		return comp.NewEvolution.GAGenerationActive()
	}, chaosStatus)

	// Adopt the six kernel pillars into the System Runtime so the
	// component graph, the readiness snapshot and the reverse-topological
	// shutdown cover the kernel too — not just the Bootstrap infrastructure.
	if err := peerKernel.adopt(ctx, comp.SystemRuntime); err != nil {
		return nil, nil, err
	}

	return subAgents, peerKernel, nil
}

// buildPeerRegistry registers the peer agents' message senders into a
// peer.Registry so agents can exchange messages directly without routing
// through a privileged orchestrator (primitive 2: peer-to-peer agent
// messaging). Agents that do not expose SendMessage (interface assertion) are
// skipped, not an error.
//
// TODO(tech-debt): no production agent exposes the SendMessage surface any
// more — it was removed with the sub.Agent message queue — so this registry
// is always empty and non-evolution ask_agent sends fail loud with "not
// registered" (equivalent to the previous always-failing nil-queue
// delivery). Kept for the discovery contract; removing it means
// restructuring the non-evolution ask_agent branch.
func buildPeerRegistry(subAgents []sub.Agent) *peer.Registry {
	reg := peer.NewRegistry()
	for _, sa := range subAgents {
		if sender, ok := sa.(interface {
			SendMessage(context.Context, *ahp.AHPMessage) error
		}); ok {
			_ = reg.Register(sa.ID(), sender.SendMessage)
		}
	}
	return reg
}

// setupPeerRegistry builds the peer-to-peer messaging registry. When the
// evolution system is wired, the peer channel is bridged through the
// evolution-aware IPC; otherwise the plain direct peer channel
// is used.
func setupPeerRegistry(
	ctx context.Context,
	g *errgroup.Group,
	subAgents []sub.Agent,
	comp *ares_bootstrap.Components,
	kernel *kernelHandle,
) (*peer.Registry, error) {
	var reg *peer.Registry
	switch {
	case comp.NewEvolution != nil:
		bridge, err := wireEvolutionIPC(subAgents, comp.NewEvolution.StrategyStore, comp.Observability.GlobalTracer, kernel)
		if err != nil {
			return nil, fmt.Errorf("wire evolution IPC: %w", err)
		}
		reg = bridge.reg
		// Dead-letter observability (RUNTIME.md #10 closure): the bus records
		// every undeliverable/failed request, but the store had no reader —
		// failures vanished at the error-return boundary. A background loop
		// surfaces the count (and a sample of recent reasons) periodically so
		// messaging failures are operator-visible. Deliberately observe-only:
		// auto-redelivery of handler-rejected messages would retry
		// non-transient failures — redelivery stays an operator decision.
		g.Go(func() error {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			last := 0
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					cur := bridge.DeadLetterCount()
					if cur > 0 && cur != last {
						dl := bridge.ipc.Bus().DeadLetters().Snapshot()
						reasons := make(map[string]int, 4)
						for _, e := range dl {
							reasons[e.Reason]++
						}
						slog.WarnContext(ctx, "peer mode: IPC dead letters retained (undeliverable/failed requests)",
							"count", cur, "reasons", reasons)
					}
					last = cur
				}
			}
		})
		// Arm the collaboration perception channel. Attaching here
		// (rather than inside wireEvolutionIPC) keeps the bridge builder free
		// of an evolution-observer parameter, and this is the only production
		// site where the bus and the recorder are both in scope. A nil recorder
		// (channel not armed — the default) leaves the bus unobserved.
		if rec := comp.NewEvolution.ChannelFeedback; rec.CollaborationArmed() {
			bridge.ipc.Bus().WithCollaborationObserver(rec)
			log.Info("serve: collaboration feedback channel armed (evolution reads collaboration receipts)")
		}
		// Wire ask_agent to ipc.Send. The syscall Kernel is built
		// in peer_mode before the bridge exists, so the collaboration primitive
		// is injected here once the bridge is ready. Reusing ipc.Send means the
		// ask_agent attempt lands in the SAME "collaboration" feedback source as
		// bridge-routed collaboration — no new observation point (reuse
		// existing components unless none exists).
		if kernel != nil && kernel.syscalls != nil {
			ipc := bridge.ipc
			kernel.syscalls.SetAskAgent(func(ctx context.Context, from, to, topic string, payload any) error {
				return ipc.Send(ctx, from, to, topic, payload)
			})
			log.Info("serve: ask_agent syscall wired to evolution-aware IPC (collaboration path)", "count", len(reg.IDs()))
		}
		log.Info("peer registry wired through evolution-aware IPC: agents registered", "count", len(reg.IDs()))
	default:
		reg = buildPeerRegistry(subAgents)
		// The ask_agent tool is advertised on the binder in every serve
		// path, so the default (non-evolution) branch must also wire the
		// collaboration primitive — otherwise the tool is advertised but
		// every call fails loud. Route through the plain peer registry
		// (no evolution observation here; the collaboration channel stays
		// disarmed until the evolution branch is taken).
		if kernel != nil && kernel.syscalls != nil {
			plainReg := reg
			kernel.syscalls.SetAskAgent(func(ctx context.Context, from, to, topic string, payload any) error {
				body := map[string]any{"topic": topic}
				if m, ok := payload.(map[string]any); ok {
					body["payload"] = m
				} else if payload != nil {
					body["payload"] = payload
				}
				msg := ahp.NewTaskMessage(from, to, "", "", body)
				return plainReg.Send(ctx, to, msg)
			})
			log.Info("serve: ask_agent syscall wired to plain peer registry (agents)", "count", len(reg.IDs()))
		}
		log.Info("peer registry wired: agents registered", "count", len(reg.IDs()))
	}
	// Retain the registry on the kernel handle at construction time (the
	// return value was previously discarded by callers). serve.go also assigns
	// it as a defensive second write; the retention contract must not depend
	// on a single call site.
	if kernel != nil {
		kernel.peerRegistry = reg
	}
	return reg, nil
}

// injectToolClassDAG builds the L1 ToolClass capability graph from the tool
// binder's schemas and injects it into the evolution system. Called
// BEFORE peer agents are created so the plannerCognition (constructed inside
// createPeerAgents when the DAG gate is open) can read enabled/budget/prior
// at L2 growth time. The L1 graph is NOT compiled into taskfabric — it is a
// capability catalog, not an execution plan (L1 ≠ L2).
func injectToolClassDAG(comp *ares_bootstrap.Components, toolBinder sub.ToolBinder) {
	if comp.NewEvolution == nil {
		return
	}
	l1DAG, err := buildToolClassDAG(toolBinder.GetToolSchemas())
	switch {
	case err == nil:
		comp.NewEvolution.SetToolClassDAG(l1DAG)
		log.Info("serve: L1 ToolClass DAG injected into evolution (nodes)", "count", len(l1DAG.Steps()))
	case errors.Is(err, errNoToolSchemas):
		log.Info("serve: no tool schemas; L1 ToolClass DAG skipped (constraints default to permissive)")
	default:
		log.Warn("serve: L1 ToolClass DAG build failed (constraints default to permissive)", "err", err)
	}
}

// chaosStopControl is the process-level kill switch for the live chaos loop
// (the emergency stop). The HTTP handler POST /api/chaos/stop
// calls RequestStop; the loop polls Stopped and exits permanently. Shadow
// mode is unaffected — it never touches production agents.
type chaosStopControl struct {
	mu      sync.Mutex
	stopped bool
}

// liveChaosCtl is the singleton control for this process.
var liveChaosCtl = &chaosStopControl{}

// RequestStop trips the kill switch.
func (c *chaosStopControl) RequestStop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
}

// Stopped reports whether the kill switch has been tripped.
func (c *chaosStopControl) Stopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// shadowSandboxLoop runs a periodic shadow Sandbox verification: it constructs
// an independent scratch fabric, replays a canonical failure scenario
// (agent kill → lease expire → recovery), and logs the result. Production
// agents are never touched — the sandbox uses its own scratch fabrics.
//
// The chaos subsystem defaults to shadow
// mode, which verifies recovery capability without impacting live agents.
//
// The status reporter records the latest verification outcome so the
// introspection panel can surface shadow-sandbox health.
func shadowSandboxLoop(ctx context.Context, interval time.Duration, status *introspect.ChaosReporter) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Info("serve: shadow sandbox loop started (production agents untouched)", "interval", interval.String())

	for {
		select {
		case <-ctx.Done():
			log.Info("serve: shadow sandbox loop stopping (context cancelled)")
			return
		case <-ticker.C:
			runShadowSandbox(ctx, status)
		}
	}
}

// runShadowSandbox constructs a scratch fabric, runs a canonical
// agent-kill→recovery scenario, and logs the outcome. All scratch fabrics
// are local to this call and discarded after — production is never touched.
// The outcome (recovered_ready / errored) is recorded to the panel status.
func runShadowSandbox(ctx context.Context, status *introspect.ChaosReporter) {
	// Build scratch fabrics — completely independent from production.
	scratchTasks := taskfabric.NewFabric()
	scratchAgents := agentfabric.NewFabric()

	// Build a scratch Recovery wired to the scratch fabrics.
	scratchRecovery := aresrecovery.New(scratchTasks, scratchAgents, aresrecovery.DefaultRestartPolicy())

	// Build the Sandbox on the scratch fabrics.
	sandbox := aresrecovery.NewSandbox(scratchTasks, scratchAgents, scratchRecovery)

	// Scripted scenario: spawn agent → create task → agent acquires task →
	// agent is killed → lease expires → recovery runs.
	events := []aresrecovery.SandboxEvent{
		{Type: aresrecovery.SandboxEventAgentSpawn, AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventTaskCreate, TaskID: "shadow-task-1"},
		{Type: aresrecovery.SandboxEventTaskAcquire, TaskID: "shadow-task-1", AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventAgentKill, AgentID: "shadow-agent-1"},
		{Type: aresrecovery.SandboxEventLeaseExpire, TaskID: "shadow-task-1"},
		{Type: aresrecovery.SandboxEventRecoverAll},
	}

	outcomes, err := sandbox.Replay(ctx, events)
	if err != nil {
		log.Info("serve: shadow sandbox replay failed: (recovery verification inconclusive)", "err", err)
		if status != nil {
			status.RecordShadow(introspect.ShadowResult{
				LastRun:   time.Now(),
				Events:    len(events),
				Recovered: false,
				Errored:   true,
			})
		}
		return
	}

	// Check the final outcome — the recovery chain must have fully recovered
	// the requeued task (RecoverFromAgentDeath re-acquires it for a
	// replacement agent, so the final state is LEASED, not READY). The
	// reliable signal is the recovered-task count carried on the
	// recover.all outcome's Detail. A missing/empty outcome list is treated
	// as inconclusive.
	if len(outcomes) == 0 {
		log.Info("serve: shadow sandbox replay produced no outcomes (recovery verification inconclusive)")
		if status != nil {
			status.RecordShadow(introspect.ShadowResult{
				LastRun:   time.Now(),
				Events:    len(events),
				Recovered: false,
				Errored:   true,
			})
		}
		return
	}
	last := outcomes[len(outcomes)-1]
	recovered, _ := last.Detail["recovered"].(int)
	recoveredOK := recovered > 0
	log.Info("serve: shadow sandbox completed", "events", len(outcomes), "final_task_state", last.TaskState, "recovered", recovered)
	if !recoveredOK {
		log.Info("serve: shadow sandbox WARNING — recovery chain did not recover the requeued task; chain may be degraded")
	}
	if status != nil {
		status.RecordShadow(introspect.ShadowResult{
			LastRun:   time.Now(),
			Events:    len(outcomes),
			Recovered: recoveredOK,
			Errored:   false,
		})
	}
}

// wireChaos wires the chaos subsystem based on the kernel config. By default
// (chaos disabled or mode=shadow), only the shadow sandbox loop is started.
// When mode=live AND allow_live=true, a real Chaos harness is also constructed
// — but only for dedicated testing environments. Production deployments should
// never enable live mode.
//
// The shadow sandbox loop is attached to the provided context and runs as a
// managed background loop (runBackground — panic-recovered, joined by the
// orchestrator/bootstrap on shutdown, never a bare `go`). It is best-effort:
// a panic in the sandbox is recovered and logged, never crashing the process.
//
// status bridges the loops into the introspection panel; it may be
// nil when the panel is not wired — the loops then only log.
func wireChaos(ctx context.Context, comp *ares_bootstrap.Components, cfg *ares_config.Config, peerKernel *kernelHandle, gaActive func() bool, status *introspect.ChaosReporter) {
	if status != nil {
		if cfg.Kernel.Chaos.Enabled {
			status.SetConfig(true, effectiveChaosMode(cfg))
		} else {
			status.SetConfig(false, "off")
		}
	}
	if !cfg.Kernel.Chaos.Enabled {
		log.Info("serve: chaos subsystem disabled (kernel.chaos.enabled=false)")
		return
	}

	mode := cfg.Kernel.Chaos.Mode
	if mode == "" {
		mode = "shadow"
	}

	startShadow := func() {
		interval := parseChaosInterval(cfg.Kernel.Chaos.Interval, 5*time.Minute)
		runBackground(ctx, comp, "chaos-shadow", func(loopCtx context.Context) error {
			shadowSandboxLoop(loopCtx, interval, status)
			return nil
		})
	}

	switch mode {
	case "shadow":
		startShadow()

	case "live":
		if !cfg.Kernel.Chaos.AllowLive {
			log.Info("serve: chaos mode=live but allow_live=false — falling back to shadow mode")
			startShadow()
			return
		}
		// Live chaos is dangerous: it kills real production agents.
		// Only construct the Chaos harness when explicitly confirmed AND a
		// non-empty target whitelist is configured: an empty
		// eligible_capabilities list must disable injection entirely rather
		// than default to "everything is a target".
		if len(cfg.Kernel.Chaos.EligibleCapabilities) == 0 {
			log.Info("serve: live chaos requested but eligible_capabilities is empty — refusing to arm (falling back to shadow)")
			startShadow()
			return
		}
		if peerKernel != nil && peerKernel.agents != nil && peerKernel.recovery != nil {
			if cfg.Kernel.Chaos.StopToken == "" {
				log.Info("serve: live chaos requested but stop_token is empty — refusing to arm without an emergency-stop credential")
				startShadow()
				return
			}
			chaos := aresrecovery.NewChaos(peerKernel.agents, peerKernel.recovery)
			interval := parseChaosInterval(cfg.Kernel.Chaos.Interval, 5*time.Minute)
			runBackground(ctx, comp, "chaos-live", func(loopCtx context.Context) error {
				liveChaosLoop(loopCtx, chaos, peerKernel.agents, interval, cfg.Kernel.Chaos, gaActive, status)
				return nil
			})
			log.Warn("serve: LIVE chaos mode enabled — agents WILL be killed", "interval", interval.String(), "rate_per_min", cfg.Kernel.Chaos.RatePerMin, "eligible_capabilities", cfg.Kernel.Chaos.EligibleCapabilities)
		} else {
			log.Info("serve: live chaos requested but kernel handle incomplete — falling back to shadow")
			startShadow()
		}

	default:
		log.Info("serve: unknown chaos mode — defaulting to shadow", "mode", mode)
		startShadow()
	}
}

// effectiveChaosMode resolves the mode that will actually run given the
// arming guards (allow_live, whitelist, stop token, kernel handle). It mirrors
// the branching inside wireChaos so the panel reports the true effective mode
// rather than the raw configured string.
func effectiveChaosMode(cfg *ares_config.Config) string {
	mode := cfg.Kernel.Chaos.Mode
	if mode == "" {
		mode = "shadow"
	}
	if mode != "live" || !cfg.Kernel.Chaos.AllowLive {
		return "shadow"
	}
	if len(cfg.Kernel.Chaos.EligibleCapabilities) == 0 || cfg.Kernel.Chaos.StopToken == "" {
		return "shadow"
	}
	return "live"
}

// liveChaosGuard holds the enforced safety state for a live chaos loop:
// the rate limiter, per-agent cooldowns, the round-robin cursor, and the
// fail-safe stop latch.
type liveChaosGuard struct {
	limiter     *ares_ratelimit.TokenBucketLimiter
	cooldownFor time.Duration
	nextIndex   int

	mu       sync.Mutex
	cooldown map[string]time.Time // agentID -> earliest next injection time
	stopped  bool                 // set when recovery verification fails; stops all future injections
}

func newLiveChaosGuard(ratePerMin int, cooldown time.Duration) *liveChaosGuard {
	if ratePerMin <= 0 {
		ratePerMin = 2
	}
	if cooldown <= 0 {
		cooldown = 10 * time.Minute
	}
	return &liveChaosGuard{
		// Token bucket: ratePerMin injections per minute → per-second rate,
		// burst 1 so injections can never stack.
		limiter: ares_ratelimit.NewTokenBucketLimiter(&ares_ratelimit.LimiterConfig{
			Rate:  float64(ratePerMin) / 60.0,
			Burst: 1,
		}),
		cooldownFor: cooldown,
		cooldown:    make(map[string]time.Time),
	}
}

// allowTarget reports whether agentID is outside its cooldown window. An
// expired cooldown entry is dropped on first touch so the map stays bounded to
// in-cooldown agents instead of accumulating every injected id forever.
func (g *liveChaosGuard) allowTarget(agentID string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	until, ok := g.cooldown[agentID]
	if !ok {
		return true
	}
	if now.After(until) {
		delete(g.cooldown, agentID)
		return true
	}
	return false
}

// markInjected records that agentID was just injected and advances the
// round-robin cursor past it.
func (g *liveChaosGuard) markInjected(agentID string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cooldown[agentID] = now.Add(g.cooldownFor)
}

// stop trips the fail-safe latch; after this no further injections run.
func (g *liveChaosGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stopped = true
}

func (g *liveChaosGuard) isStopped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// liveChaosLoop runs periodic live chaos injections. This is the dangerous
// path: real production agents are killed/suspended. Every injection cycle is
// gated by six enforced guardrails:
//
//  1. Emergency stop — POST /api/chaos/stop (X-Chaos-Token) exits the loop
//     permanently.
//  2. Fail-safe latch — if recovery verification ever fails, ALL further
//     injections stop until process restart.
//  3. GA quiet window — when cfg.PauseDuringGA is set, injections are deferred
//     while gaActive() reports a generation mid-flight.
//  4. Rate limit — token bucket capped at cfg.RatePerMin injections/minute.
//  5. Cooldown — an injected agent is not targeted again for cfg.Cooldown.
//  6. Target whitelist — only agents declaring a capability from
//     cfg.EligibleCapabilities qualify (arming itself refuses an empty list).
//
// status surfaces the loop's operational state to the panel
// (active / injections / fail-safe / GA pause); it may be nil.
func liveChaosLoop(ctx context.Context, chaos *aresrecovery.Chaos, fabric *agentfabric.Fabric, interval time.Duration, cfg ares_config.ChaosConfig, gaActive func() bool, status *introspect.ChaosReporter) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	ratePerMin := cfg.RatePerMin
	if ratePerMin <= 0 {
		ratePerMin = 2
	}
	cooldown := parseChaosInterval(cfg.Cooldown, 10*time.Minute)
	guard := newLiveChaosGuard(ratePerMin, cooldown)
	pausedForGA := false

	// Report the armed live state to the panel on loop start.
	if status != nil {
		status.SetLive(introspect.LiveChaosState{Active: true})
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Info("serve: live chaos loop started (rate limit and cooldown enforced)", "interval", interval.String(), "rate_per_min", ratePerMin, "cooldown", cooldown.String(), "eligible_capabilities", cfg.EligibleCapabilities, "pause_during_ga", cfg.PauseDuringGA)

	for {
		select {
		case <-ctx.Done():
			log.Info("serve: live chaos loop stopping (context cancelled)")
			if status != nil {
				status.SetLive(introspect.LiveChaosState{Active: false})
			}
			return
		case <-ticker.C:
			// Emergency stop: POST /api/chaos/stop trips
			// this permanently — the loop exits rather than idles.
			if liveChaosCtl.Stopped() {
				log.Info("serve: live chaos loop stopped by emergency stop endpoint")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{
						Active:           false,
						StoppedByControl: true,
					})
				}
				return
			}
			if guard.isStopped() {
				log.Info("serve: live chaos loop stopped by fail-safe latch (earlier recovery verification failed)")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{
						Active:          false,
						FailSafeTripped: true,
					})
				}
				return
			}
			// GA quiet window: defer injections while a
			// generation is mid-flight. State transitions are logged once so
			// operators can see the pause engaging and releasing.
			if cfg.PauseDuringGA && gaActive != nil && gaActive() {
				if !pausedForGA {
					pausedForGA = true
					log.Info("serve: live chaos paused — GA generation in flight (quiet window)")
					if status != nil {
						status.SetLive(introspect.LiveChaosState{Active: true, PausedForGA: true})
					}
				}
				continue
			}
			if pausedForGA {
				pausedForGA = false
				log.Info("serve: live chaos resumed — GA generation finished")
				if status != nil {
					status.SetLive(introspect.LiveChaosState{Active: true, PausedForGA: false})
				}
			}
			runLiveChaosInjection(ctx, chaos, fabric, guard, cfg.EligibleCapabilities, status)
		}
	}
}

// runLiveChaosInjection performs a single chaos injection cycle against the
// next round-robin target that is outside its cooldown window. It injects a
// kill, then verifies recovery; a failed verification trips the fail-safe
// latch so no further injections occur. The cycle is wrapped in panic
// recovery so a chaos failure never crashes the process.
//
// status records the injection count and fail-safe state; it may be
// nil.
func runLiveChaosInjection(ctx context.Context, chaos *aresrecovery.Chaos, fabric *agentfabric.Fabric, guard *liveChaosGuard, eligible []string, status *introspect.ChaosReporter) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("serve: live chaos injection panicked (recovered)", "panic", r)
		}
	}()

	agents := fabric.Agents()
	if len(agents) == 0 {
		log.Info("serve: live chaos — no agents available for injection")
		return
	}

	now := time.Now()

	// Round-robin target selection, skipping agents inside their cooldown
	// window AND agents whose declared capabilities are not whitelisted. If no agent qualifies, skip this cycle entirely.
	var target string
	for i := 0; i < len(agents); i++ {
		candidate := agents[guard.nextIndex%len(agents)]
		guard.nextIndex++
		if !guard.allowTarget(candidate, now) {
			continue
		}
		if !agentEligibleForChaos(fabric, candidate, eligible) {
			continue
		}
		target = candidate
		break
	}
	if target == "" {
		log.Info("serve: live chaos — no eligible target (cooldown or whitelist), skipping cycle")
		return
	}

	// Enforced rate limit: the token bucket admits at most RatePerMin
	// injections per minute regardless of ticker cadence.
	if allowed, err := guard.limiter.Allow(ctx); err != nil || !allowed {
		log.Info("serve: live chaos — rate limited, skipping injection on", "err", err, "target", target)
		return
	}

	if err := chaos.InjectFailure(ctx, target, aresrecovery.FailureKill); err != nil {
		log.Warn("serve: live chaos inject kill failed", "target", target, "err", err)
		return
	}
	guard.markInjected(target, now)

	// Report injection to the panel.
	if status != nil {
		status.AddInjection(now)
	}

	// Verify recovery. VerifyRecovery returns the count of recovered agents;
	// zero means the recovery chain did not restore anything — trip the
	// fail-safe latch so no further injections run.
	recovered := chaos.VerifyRecovery(ctx)
	if recovered == 0 {
		guard.stop()
		log.Info("serve: live chaos — recovery verification FAILED for (0 agents recovered); FURTHER INJECTIONS STOPPED by fail-safe latch", "target", target)
		if status != nil {
			status.SetLive(introspect.LiveChaosState{
				Active:          true,
				FailSafeTripped: true,
			})
		}
		return
	}
	log.Info("serve: live chaos — agent killed and recovered (agents recovered)", "target", target, "recovered", recovered)
}

// parseChaosInterval parses the chaos interval string, returning the default
// on empty or invalid input.
func parseChaosInterval(s string, defaultInterval time.Duration) time.Duration {
	if s == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	return d
}

// agentEligibleForChaos reports whether the named agent declares at least one
// capability present in the whitelist. The whitelist is matched
// against the agent's own Capabilities list; an unknown agent is never
// eligible.
func agentEligibleForChaos(fabric *agentfabric.Fabric, agentID string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return false
	}
	a, err := fabric.Get(agentID)
	if err != nil || a == nil {
		return false
	}
	for _, capName := range a.Capabilities {
		for _, w := range whitelist {
			if capName == w {
				return true
			}
		}
	}
	return false
}

// errNoLiveAgentDAG is returned when no peers are configured: the caller
// keeps the bootstrap placeholder rather than injecting an empty graph.
var errNoLiveAgentDAG = errors.New("no peer agents configured for a live DAG")

// errNoToolSchemas is returned when the tool binder exposes no tool schemas:
// the L1 capability graph would be empty, so the caller keeps the bootstrap
// placeholder instead of injecting an empty graph.
var errNoToolSchemas = errors.New("no tool schemas available for a ToolClass DAG")

// buildLiveAgentDAG materializes the configured agent population as a real
// MutableDAG: one node per peer (AgentType = primary capability), dependency
// edges from the legacy agents.sub entries' Dependencies when present.
//
// This is the live topology the evolution system's structure patches act on.
// Historically serve never called UpdateLiveDAG, so workflow/recovery
// patches mutated the synthetic input→process→output bootstrap DAG forever —
// the "live runtime" promotion affected nothing observable. The returned DAG
// is registered on the runtime manager AND injected into the evolution
// executors so graph/recovery patches land on the agent graph actually shown
// in the runtime snapshot.
//
// Returns (nil, errNoLiveAgentDAG) when no peers are configured — the caller
// matches on that sentinel and keeps the bootstrap placeholder rather than
// injecting an empty graph.
func buildLiveAgentDAG(cfg *ares_config.Config) (*engine.MutableDAG, error) {
	peers := normalizedPeers(cfg)
	if len(peers) == 0 {
		return nil, errNoLiveAgentDAG
	}

	// Legacy sub entries may declare Dependencies between agents; carry them
	// over so older configs keep their declared topology.
	legacyDeps := make(map[string][]string, len(cfg.Agents.Sub))
	for _, s := range cfg.Agents.Sub {
		if len(s.Dependencies) > 0 {
			legacyDeps[s.ID] = append([]string(nil), s.Dependencies...)
		}
	}

	steps := make([]*engine.Step, 0, len(peers))
	seen := make(map[string]bool, len(peers))
	for _, p := range peers {
		if p.ID == "" || seen[p.ID] {
			continue // defensive: NewMutableDAG rejects duplicate ids anyway
		}
		seen[p.ID] = true
		typ := ""
		if len(p.Capabilities) > 0 {
			typ = p.Capabilities[0]
		}
		step := &engine.Step{
			ID:        p.ID,
			Name:      p.ID,
			AgentType: typ,
			Input:     fmt.Sprintf("capability:%s", typ),
		}
		if deps, ok := legacyDeps[p.ID]; ok {
			step.DependsOn = deps
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, errNoLiveAgentDAG
	}

	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		return nil, fmt.Errorf("build live agent DAG: %w", err)
	}
	return dag, nil
}

// L1 metadata keys for ToolClass evolution constraints. The L1 graph's
// Metadata is string-only (engine.Step.Metadata), so budget/prior are stored
// as their string representations.
const (
	l1MetaEnabled = "enabled"
	l1MetaBudget  = "budget"
	l1MetaPrior   = "prior"
)

// buildToolClassDAG constructs the L1 capability graph: one node per
// ToolClass (toolName + "#" + argShape), with Metadata carrying the evolution
// constraints enabled/budget/prior. The argShape is the sorted set of
// parameter key names from the tool's schema — this normalizes by type
// signature, not by value, so "read_file(path=foo.txt)" and
// "read_file(path=bar.txt)" collapse into one ToolClass node.
//
// The L1 graph is the evolution system's stable action surface: genome
// patches mutate enabled/budget/prior on L1 nodes, the planner reads them
// before growing L2 tool nodes, and L2 execution
// statistics flow back as fitness. The L1 graph is NOT compiled into
// taskfabric — it is a capability catalog, not an execution plan.
//
// Returns (nil, errNoToolSchemas) when the binder exposes no tools.
func buildToolClassDAG(schemas []core_tools.ToolSchema) (*engine.MutableDAG, error) {
	if len(schemas) == 0 {
		return nil, errNoToolSchemas
	}

	steps := make([]*engine.Step, 0, len(schemas))
	seen := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		if s.Name == "" {
			continue
		}
		nodeID := core_tools.ToolClassID(s.Name, core_tools.ToolArgShape(s))
		if seen[nodeID] {
			continue // defensive: same tool+shape deduplicated
		}
		seen[nodeID] = true
		step := &engine.Step{
			ID:        nodeID,
			Name:      s.Name,
			AgentType: "tool/" + s.Name,
			Input:     s.Description,
			Metadata: map[string]string{
				l1MetaEnabled: "true",
				l1MetaBudget:  "0", // 0 = unlimited
				l1MetaPrior:   "",
			},
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return nil, errNoToolSchemas
	}

	dag, err := engine.NewMutableDAG(steps)
	if err != nil {
		return nil, fmt.Errorf("build toolclass DAG: %w", err)
	}
	return dag, nil
}

var arenaCmd = &cobra.Command{
	Use:   "arena",
	Short: "Chaos Engineering Arena commands",
	Long: `Run, validate, list, and inspect chaos engineering scenarios.
Also includes a built-in HTTP server and survival testing.`,
}

var arenaRunCmd = &cobra.Command{
	Use:   "run <scenario.yaml>",
	Short: "Run a scenario against a remote arena server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := arena.LoadScenarioFile(args[0])
		if err != nil {
			return fmt.Errorf("load scenario: %w", err)
		}
		if err := arena.ValidateScenario(s); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}

		fmt.Printf("Running scenario: %s\n", s.Name)
		if s.Description != "" {
			fmt.Printf("  Description: %s\n", s.Description)
		}
		fmt.Printf("  Actions: %d\n", len(s.Actions))
		fmt.Printf("  Target:   %s\n\n", arenaRunAddr)

		bodyData, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("marshal scenario: %w", err)
		}

		url := arenaRunAddr + "/arena/scenario/run"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bodyData)))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		setArenaAuthHeader(req)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(respBody))
		}

		var report arena.ScenarioReport
		if err := json.Unmarshal(respBody, &report); err != nil {
			return fmt.Errorf("parse scenario report: %w (body: %s)", err, string(respBody))
		}
		printReport(&report)
		return nil
	},
}

var arenaValidateCmd = &cobra.Command{
	Use:   "validate <scenario.yaml>",
	Short: "Validate a scenario file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scenarioPath := args[0]

		if arenaValidateRemote {
			return validateRemote(scenarioPath, arenaValidateAddr)
		}

		s, err := arena.LoadScenarioFile(scenarioPath)
		if err != nil {
			return fmt.Errorf("load scenario: %w", err)
		}
		if err := arena.ValidateScenario(s); err != nil {
			fmt.Printf("❌ INVALID: %s\n\n", scenarioPath)
			fmt.Printf("  Error: %v\n", err)
			fmt.Printf("  Name:   %s\n", s.Name)
			fmt.Printf("  Actions: %d\n", len(s.Actions))
			os.Exit(1)
		}

		fmt.Printf("✅ VALID: %s\n", scenarioPath)
		fmt.Printf("  Name:        %s\n", s.Name)
		fmt.Printf("  Description: %s\n", s.Description)
		fmt.Printf("  Tags:        %v\n", s.Tags)
		fmt.Printf("  Actions:     %d\n", len(s.Actions))
		if s.Config.StopOnError {
			fmt.Printf("  Config:      stop_on_error=true\n")
		}
		if s.Config.Warmup > 0 {
			fmt.Printf("  Config:      warmup=%v\n", s.Config.Warmup)
		}
		if s.Config.Cooldown > 0 {
			fmt.Printf("  Config:      cooldown=%v\n", s.Config.Cooldown)
		}
		if s.Config.Timeout > 0 {
			fmt.Printf("  Config:      timeout=%v\n", s.Config.Timeout)
		}
		return nil
	},
}

var arenaListCmd = &cobra.Command{
	Use:   "list [dir]",
	Short: "List available scenarios in a directory",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := "."
		if len(args) >= 1 {
			dir = args[0]
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("read directory %s: %w", dir, err)
		}

		var scenarios []string
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			ext := filepath.Ext(name)
			if ext == ".yaml" || ext == ".yml" || ext == ".json" {
				scenarios = append(scenarios, name)
			}
		}

		if len(scenarios) == 0 {
			fmt.Printf("No scenario files found in %s\n", dir)
			return nil
		}

		fmt.Printf("Scenarios in %s:\n", dir)
		for i, name := range scenarios {
			fullPath := filepath.Join(dir, name)
			s, err := arena.LoadScenarioFile(fullPath)
			if err != nil {
				fmt.Printf("  %d. %s (parse error: %v)\n", i+1, name, err)
				continue
			}
			desc := s.Description
			if desc == "" {
				desc = "(no description)"
			}
			tags := ""
			if len(s.Tags) > 0 {
				tags = fmt.Sprintf("[%s]", strings.Join(s.Tags, ", "))
			}
			fmt.Printf("  %d. %-30s  %s %s\n", i+1, name, desc, tags)
		}
		return nil
	},
}

// setArenaAuthHeader attaches the arena API key from the environment to an
// outgoing request. Client subcommands need this because the arena server
// denies unauthenticated requests by default; without it every CLI call would
// 401 against a properly configured server, pushing operators towards
// --allow-anonymous and undoing the hardening.
func setArenaAuthHeader(req *http.Request) {
	if key := os.Getenv("ARENA_API_KEY"); key != "" {
		req.Header.Set("X-API-Key", key)
	}
}

var arenaServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start arena HTTP server",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Real providers — chaos injections now operate on the
		// arena process's own agent pool and mutable DAG (see
		// buildArenaInjector / arenaRuntimeProvider / arenaDAGProvider).
		inj, arenaMgr := buildArenaInjector()
		defer func() { _ = arenaMgr.Stop() }()
		// Share the evolution components' evidence store so chaos failures land
		// in the same store the GA genomes consume for fitness evaluation.
		ev, err := getNewEvolution()
		if err != nil {
			return fmt.Errorf("get evolution components: %w", err)
		}
		var evStore evidence.Store
		if ev != nil && ev.EvidenceStore != nil {
			evStore = ev.EvidenceStore
		}
		svc := arena.NewService(inj, nil, evStore)

		// Wire the evolution bridge: chaos fault detection → coordinator.
		if ev != nil && ev.Coordinator != nil {
			bridge := arena.NewEvolutionBridge(ev.Coordinator)
			svc.SetEvolutionBridge(bridge)
		}

		handler := arena.NewHandler(svc)
		// Enable API key auth when configured via env or flag. Without a key,
		// the middleware denies every request unless anonymous access was
		// explicitly requested (local development only).
		apiKey := arenaServeAPIKey
		if apiKey == "" {
			apiKey = os.Getenv("ARENA_API_KEY")
		}
		if apiKey != "" {
			handler.SetAPIKey(apiKey)
		} else if arenaServeAllowAnon {
			handler.AllowAnonymous(true)
		} else {
			return errors.New("arena serve requires an API key: set --api-key or ARENA_API_KEY, " +
				"or pass --allow-anonymous to run without authentication (local development only)")
		}

		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		authWrapped := handler.APIKeyAuthMiddleware(mux)
		wrapped := arena.RecoverMiddleware(authWrapped)

		server := &http.Server{
			Addr:         arenaServeAddr,
			Handler:      wrapped,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		}

		fmt.Printf("Arena server listening on %s\n", arenaServeAddr)
		if apiKey != "" {
			fmt.Printf("Auth: API key enabled (header: X-API-Key)\n")
		} else {
			fmt.Printf("Auth: DISABLED via --allow-anonymous — destructive endpoints " +
				"are reachable without credentials. Do not expose this port.\n")
		}
		fmt.Printf("Endpoints:\n")
		fmt.Printf("  POST /arena/scenario/run       Run a scenario\n")
		fmt.Printf("  POST /arena/scenario/validate   Validate a scenario\n")
		fmt.Printf("  GET  /arena/stats               View statistics\n")
		fmt.Printf("  GET  /arena/history             View action history\n")
		fmt.Printf("  GET  /arena/stream              SSE event stream\n")
		fmt.Printf("  GET  /arena/score               Resilience score\n")
		fmt.Printf("  GET  /arena/metrics             Detailed metrics\n")
		fmt.Printf("  POST /arena/survival            Start survival test (background)\n")
		fmt.Printf("  POST /arena/survival/stop       Stop survival test\n")
		fmt.Printf("  GET  /arena/survival/status     Survival progress\n")
		fmt.Printf("  GET  /arena/flight/timeline     Flight recorder timeline\n")
		fmt.Printf("  GET  /arena/flight/diagnostics  Diagnostic records\n")

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	},
}

var arenaSurvivalCmd = &cobra.Command{
	Use:   "survival",
	Short: "Run survival mode against a remote server",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := map[string]any{
			"duration": arenaSurvivalDuration.String(),
			"interval": arenaSurvivalInterval.String(),
		}
		body, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}

		fmt.Println(strings.Repeat("=", 59))
		fmt.Println("  Arena Survival Mode")
		fmt.Println(strings.Repeat("=", 59))
		fmt.Printf("  Duration: %s  Interval: %s\n", arenaSurvivalDuration, arenaSurvivalInterval)
		fmt.Printf("  Server:   %s\n\n", arenaSurvivalAddr)

		baseURL := strings.TrimRight(arenaSurvivalAddr, "/")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			baseURL+"/arena/survival", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create start request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		setArenaAuthHeader(req)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("start survival: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			return fmt.Errorf("server returned status %d", resp.StatusCode)
		}

		fmt.Println("  Survival started. Press Ctrl+C to stop.")
		return pollSurvival(ctx, baseURL)
	},
}

var arenaInspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect arena run results from a remote server",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseURL := strings.TrimRight(arenaInspectAddr, "/")

		fmt.Println(strings.Repeat("=", 59))
		fmt.Println("  Arena Inspection Report")
		fmt.Println(strings.Repeat("=", 59))

		// Bound the inspection requests so a hanging server cannot block forever.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		score := getScore(ctx, baseURL)
		if score != nil {
			s, _ := score["score"].(float64)
			g, _ := score["grade"].(string)
			rr, _ := score["recovery_rate"].(float64)
			totalF, _ := score["total_faults"].(float64)
			recF, _ := score["recovered_faults"].(float64)
			failF, _ := score["failed_faults"].(float64)

			fmt.Printf("\n  Score:          %.1f (%s)\n", s, g)
			fmt.Printf("  Recovery Rate:  %.1f%%\n", rr)
			fmt.Printf("  Faults:         %.0f total, %.0f recovered, %.0f failed\n",
				totalF, recF, failF)

			if av, ok := score["availability_score"].(float64); ok {
				fmt.Printf("  Availability:   %.1f\n", av)
			}
			if cs, ok := score["consistency_score"].(float64); ok {
				fmt.Printf("  Consistency:    %.1f\n", cs)
			}
		} else {
			fmt.Println("  ⚠ Score data unavailable")
		}

		metrics := getMetrics(ctx, baseURL)
		if metrics != nil {
			fmt.Print("\n  Metrics:\n")
			if avg, ok := metrics["avg_recovery_time"].(string); ok && avg != "" && avg != "0" {
				fmt.Printf("    Avg Recovery Time: %s\n", avg)
			}
			if minR, ok := metrics["min_recovery_time"].(string); ok && minR != "" {
				fmt.Printf("    Min Recovery Time: %s\n", minR)
			}
			if maxR, ok := metrics["max_recovery_time"].(string); ok && maxR != "" {
				fmt.Printf("    Max Recovery Time: %s\n", maxR)
			}
			if fc, ok := metrics["failover_count"].(float64); ok && fc > 0 {
				fmt.Printf("    Failovers:         %.0f\n", fc)
			}
			if dr, ok := metrics["data_consistency_rate"].(float64); ok && dr > 0 {
				fmt.Printf("    Data Consistency:  %.1f%%\n", dr)
			}
		}

		if arenaInspectTimeline {
			printInspectTimeline(ctx, baseURL)
		}
		if arenaInspectDiagnostics {
			printInspectDiagnostics(ctx, baseURL)
		}

		fmt.Println()
		return nil
	},
}

// Flags
var (
	arenaRunAddr            string
	arenaValidateRemote     bool
	arenaValidateAddr       string
	arenaServeAddr          string
	arenaServeAPIKey        string
	arenaServeAllowAnon     bool
	arenaSurvivalAddr       string
	arenaSurvivalDuration   time.Duration
	arenaSurvivalInterval   time.Duration
	arenaInspectAddr        string
	arenaInspectTimeline    bool
	arenaInspectDiagnostics bool
)

// Arena init. Order-independent from the serve init above (each only wires
// its own disjoint cobra tree and flag sets; the pre-merge files ran them in
// filename order — arena.go before serve.go — which is not preserved here,
// and nothing depends on it).
func init() {
	rootCmd.AddCommand(arenaCmd)

	arenaCmd.AddCommand(arenaRunCmd)
	arenaRunCmd.Flags().StringVar(&arenaRunAddr, "addr", "http://localhost:8080", "Arena server address")

	arenaCmd.AddCommand(arenaValidateCmd)
	arenaValidateCmd.Flags().BoolVar(&arenaValidateRemote, "remote", false, "Validate against remote server")
	arenaValidateCmd.Flags().StringVar(&arenaValidateAddr, "addr", "http://localhost:8080", "Arena server address (used with --remote)")

	arenaCmd.AddCommand(arenaListCmd)

	arenaCmd.AddCommand(arenaServeCmd)
	arenaServeCmd.Flags().StringVar(&arenaServeAddr, "addr", ":8080", "Listen address")
	arenaServeCmd.Flags().StringVar(&arenaServeAPIKey, "api-key", "", "API key required for all arena endpoints (also via ARENA_API_KEY env)")
	arenaServeCmd.Flags().BoolVar(&arenaServeAllowAnon, "allow-anonymous", false,
		"Serve arena endpoints without authentication (local development only; destructive endpoints become unprotected)")

	arenaCmd.AddCommand(arenaSurvivalCmd)
	arenaSurvivalCmd.Flags().StringVar(&arenaSurvivalAddr, "addr", "http://localhost:8080", "Arena server address")
	arenaSurvivalCmd.Flags().DurationVar(&arenaSurvivalDuration, "duration", 5*time.Minute, "Survival test duration")
	arenaSurvivalCmd.Flags().DurationVar(&arenaSurvivalInterval, "interval", 10*time.Second, "Interval between fault injections")

	arenaCmd.AddCommand(arenaInspectCmd)
	arenaInspectCmd.Flags().StringVar(&arenaInspectAddr, "addr", "http://localhost:8080", "Arena server address")
	arenaInspectCmd.Flags().BoolVar(&arenaInspectTimeline, "timeline", true, "Show timeline events")
	arenaInspectCmd.Flags().BoolVar(&arenaInspectDiagnostics, "diagnostics", true, "Show diagnostics breakdown")
}

// ── Shared helpers ──────────────────────────────────────────

func validateRemote(scenarioPath, addr string) error {
	s, err := arena.LoadScenarioFile(scenarioPath)
	if err != nil {
		return fmt.Errorf("load scenario: %w", err)
	}

	bodyData, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal scenario: %w", err)
	}

	url := addr + "/arena/scenario/validate"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(bodyData)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	setArenaAuthHeader(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("remote validation failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	fmt.Println(string(respBody))
	return nil
}

func pollSurvival(ctx context.Context, baseURL string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nSurvival stopped.")
			printFinalScore(ctx, baseURL)
			return nil
		case <-ticker.C:
			printSurvivalStatus(ctx, baseURL)
		}
	}
}

func printSurvivalStatus(ctx context.Context, baseURL string) {
	s := getSurvivalStatus(ctx, baseURL)
	if s == nil {
		return
	}
	status, _ := s["status"].(string)
	if status == "" {
		return
	}
	progress, _ := s["progress"].(float64)
	statusMsg := status
	if progress > 0 {
		statusMsg = fmt.Sprintf("%s (%.0f%%)", status, progress)
	}
	fmt.Printf("\r  Status: %-20s", statusMsg)
}

func getSurvivalStatus(ctx context.Context, baseURL string) map[string]any {
	return getJSON(ctx, baseURL+"/arena/survival/status")
}

func getScore(ctx context.Context, baseURL string) map[string]any {
	return getJSON(ctx, baseURL+"/arena/score")
}

func getMetrics(ctx context.Context, baseURL string) map[string]any {
	return getJSON(ctx, baseURL+"/arena/metrics")
}

func printFinalScore(ctx context.Context, baseURL string) {
	score := getScore(ctx, baseURL)
	if score == nil {
		return
	}
	s, _ := score["score"].(float64)
	g, _ := score["grade"].(string)
	fmt.Printf("\n\nFinal Score: %.1f (%s)\n", s, g)
}

func printInspectTimeline(ctx context.Context, baseURL string) {
	tlData := getJSON(ctx, baseURL+"/arena/flight/timeline")
	if tlData == nil {
		return
	}

	if events, ok := tlData["events"].([]any); ok && len(events) > 0 {
		fmt.Print("\n  Timeline Events:\n")
		for i, evt := range events {
			if m, ok := evt.(map[string]any); ok {
				t := stringOr(m, "type", "?")
				agent := stringOr(m, "agent_id", "?")
				ts := stringOr(m, "timestamp", "")
				if len(ts) > 19 {
					ts = ts[:19]
				}
				fmt.Printf("    %d. [%s] agent=%s @ %s\n", i+1, t, agent, ts)
			}
		}
	}
}

func printInspectDiagnostics(ctx context.Context, baseURL string) {
	diagData := getJSON(ctx, baseURL+"/arena/flight/diagnostics")
	if diagData == nil {
		return
	}

	if records, ok := diagData["records"].([]any); ok && len(records) > 0 {
		fmt.Print("\n  Diagnostics:\n")
		for i, rec := range records {
			if m, ok := rec.(map[string]any); ok {
				cat := stringOr(m, "category", "?")
				agent := stringOr(m, "agent_id", "?")
				cause := stringOr(m, "root_cause", "")
				if len(cause) > 60 {
					cause = cause[:60] + "..."
				}
				fmt.Printf("    %d. [%s] agent=%s cause=%q\n", i+1, cat, agent, cause)
			}
		}
	}
}

// getJSON performs an HTTP GET with the given context and decodes the JSON
// response body into a map. Returns nil on any error. The context provides
// cancellation/timeout control and supersedes the legacy http.Get calls.
func getJSON(ctx context.Context, url string) map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result
}

func printReport(report *arena.ScenarioReport) {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Printf("  Scenario Report: %s\n", report.ScenarioName)
	fmt.Println("=" + strings.Repeat("=", 59))

	if report.Description != "" {
		fmt.Printf("  Description: %s\n", report.Description)
	}

	fmt.Printf("  Started:    %s\n", report.StartedAt.Format(time.RFC3339))
	fmt.Printf("  Finished:   %s\n", report.FinishedAt.Format(time.RFC3339))
	fmt.Printf("  Duration:   %s\n", report.Duration.Truncate(time.Millisecond))
	fmt.Println()
	fmt.Printf("  Results:    %d passed, %d failed\n",
		report.Passed, report.Failed)
	fmt.Printf("  Score:      %.1f (%s)\n", report.Score.Score, report.Score.Grade)
	fmt.Printf("  Verified:   %t\n", report.Verified)
	fmt.Println()

	if len(report.Results) > 0 {
		fmt.Println("  Action Details:")
		fmt.Println("  " + strings.Repeat("-", 59))
		for i, r := range report.Results {
			status := "✅ PASS"
			if !r.Success {
				status = "❌ FAIL"
			}
			actionType := string(r.Action.Type)
			label := ""
			if r.Action.Metadata != nil {
				if l, ok := r.Action.Metadata["label"].(string); ok {
					label = l
				}
			}
			if label != "" {
				fmt.Printf("    %d. [%s] %s (%s) - %s\n",
					i+1, status, actionType, label, r.Duration.Truncate(time.Millisecond))
			} else {
				fmt.Printf("    %d. [%s] %s - %s\n",
					i+1, status, actionType, r.Duration.Truncate(time.Millisecond))
			}
			if r.Error != "" {
				fmt.Printf("       Error: %s\n", r.Error)
			}
		}
	}

	fmt.Println()
	fmt.Printf("  Recovery Rate: %.1f%%\n", report.Score.RecoveryRate)
	if report.Score.AvgRecoveryTime > 0 {
		fmt.Printf("  Avg Recovery: %s\n", report.Score.AvgRecoveryTime.Truncate(time.Millisecond))
	}
	fmt.Println()
}

func stringOr(m map[string]any, key, fallback string) string {
	if v, ok := m[key]; ok {
		if s, ok2 := v.(string); ok2 {
			return s
		}
	}
	return fallback
}

// arenaRuntimeProvider adapts the arena process's own runtime.Manager to
// arena.RuntimeProvider. Manager implements the chaos methods natively
// (manager_chaos.go); the explicit delegation keeps the adapter decoupled
// from interface drift on either side.
type arenaRuntimeProvider struct{ mgr *runtime.Manager }

func (p *arenaRuntimeProvider) StopAgent(ctx context.Context, agentID string) error {
	return p.mgr.StopAgent(ctx, agentID)
}

func (p *arenaRuntimeProvider) ListAgents() []runtime.AgentInfo {
	return p.mgr.ListAgents()
}

func (p *arenaRuntimeProvider) PauseAgent(ctx context.Context, agentID string) error {
	return p.mgr.PauseAgent(ctx, agentID)
}

func (p *arenaRuntimeProvider) ResumeAgent(ctx context.Context, agentID string) error {
	return p.mgr.ResumeAgent(ctx, agentID)
}

func (p *arenaRuntimeProvider) SlowAgent(ctx context.Context, agentID string, delay time.Duration) error {
	return p.mgr.SlowAgent(ctx, agentID, delay)
}

func (p *arenaRuntimeProvider) PartitionNetwork(ctx context.Context, agentID string) error {
	return p.mgr.PartitionNetwork(ctx, agentID)
}

func (p *arenaRuntimeProvider) ToolTimeout(ctx context.Context, agentID string, timeout time.Duration) error {
	return p.mgr.ToolTimeout(ctx, agentID, timeout)
}

func (p *arenaRuntimeProvider) CorruptMemory(ctx context.Context, agentID string) error {
	return p.mgr.CorruptMemory(ctx, agentID)
}

func (p *arenaRuntimeProvider) DisconnectMCP(ctx context.Context, agentID string) error {
	return p.mgr.DisconnectMCP(ctx, agentID)
}

func (p *arenaRuntimeProvider) InjectLLMFailure(ctx context.Context, agentID string, errType string) error {
	return p.mgr.InjectLLMFailure(ctx, agentID, errType)
}

// arenaDAGProvider adapts a workflow engine MutableDAG to arena.DAGProvider.
// The DAG snapshot supplies node/edge listings; mutations delegate to the
// live mutable DAG so evolution patches and chaos removals share one graph.
type arenaDAGProvider struct{ dag *engine.MutableDAG }

// ListNodes implements arena.DAGProvider.
func (p *arenaDAGProvider) ListNodes(_ context.Context) []string {
	snap := p.dag.Snapshot()
	ids := make([]string, 0, len(snap.Nodes))
	for id := range snap.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ListEdges implements arena.DAGProvider.
func (p *arenaDAGProvider) ListEdges(_ context.Context) [][2]string {
	snap := p.dag.Snapshot()
	var edges [][2]string
	for from, tos := range snap.Edges {
		for _, to := range tos {
			edges = append(edges, [2]string{from, to})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i][0] != edges[j][0] {
			return edges[i][0] < edges[j][0]
		}
		return edges[i][1] < edges[j][1]
	})
	return edges
}

// RemoveNode implements arena.DAGProvider.
func (p *arenaDAGProvider) RemoveNode(ctx context.Context, id string) error {
	return p.dag.RemoveNode(ctx, id)
}

// RemoveEdge implements arena.DAGProvider.
func (p *arenaDAGProvider) RemoveEdge(ctx context.Context, from, to string) error {
	return p.dag.RemoveEdge(ctx, from, to)
}

// buildArenaInjector assembles the arena fault injector against the arena
// process's own runtime and DAG (demo positioning): the Manager
// starts with a small pool of registered demo agents so every chaos injection
// has a real target immediately, and the mutable DAG gives the node/edge
// removals a live graph to operate on.
//
// Args:
//
//	none.
//
// Returns:
//   - *arena.Injector: the wired injector. A provider is nil only when its
//     backing component failed to construct, in which case the corresponding
//     injections fail loudly (ErrRuntimeNil / ErrDAGNil) instead of silently
//     reporting success against a pool that never started.
//   - *runtime.Manager: the demo agent pool (stopped by the caller).
func buildArenaInjector() (*arena.Injector, *runtime.Manager) {
	mgr := runtime.New(nil, nil, nil)
	for _, id := range []string{"arena-worker-1", "arena-worker-2", "arena-worker-3"} {
		mgr.RegisterAgent(newArenaDemoAgent(id, "coder"), nil)
	}
	// A pool that failed to start has no live agents: hand the injector a nil
	// RuntimeProvider so kill/pause/slow return ErrRuntimeNil rather than
	// appearing to act on agents that are not running.
	var rt arena.RuntimeProvider
	if err := mgr.Start(context.Background()); err != nil {
		log.Info("arena serve: demo agent pool start failed; agent injections disabled", "err", err)
	} else {
		rt = &arenaRuntimeProvider{mgr: mgr}
	}
	dag, err := engine.NewMutableDAG(nil)
	if err != nil {
		log.Info("arena serve: mutable DAG unavailable; DAG injections disabled", "err", err)
		return arena.NewInjector(rt, nil), mgr
	}
	return arena.NewInjector(rt, &arenaDAGProvider{dag: dag}), mgr
}

// arenaDemoAgent is a minimal base.Agent standing in for a real executor in
// the arena drill process: its only job is to exist so kill/pause/slow/
// partition injections have a lifecycle to act on.
type arenaDemoAgent struct {
	id     string
	typ    models.AgentType
	status atomic.Value // models.AgentStatus
}

func newArenaDemoAgent(id, typ string) *arenaDemoAgent {
	a := &arenaDemoAgent{id: id, typ: models.AgentType(typ)}
	a.status.Store(models.AgentStatusOffline)
	return a
}

func (a *arenaDemoAgent) ID() string                 { return a.id }
func (a *arenaDemoAgent) Type() models.AgentType     { return a.typ }
func (a *arenaDemoAgent) Status() models.AgentStatus { return a.status.Load().(models.AgentStatus) }
func (a *arenaDemoAgent) Start(context.Context) error {
	a.status.Store(models.AgentStatusReady)
	return nil
}
func (a *arenaDemoAgent) Stop(context.Context) error {
	a.status.Store(models.AgentStatusOffline)
	return nil
}
func (a *arenaDemoAgent) Process(context.Context, any) (any, error) {
	return map[string]any{"demo": "arena agent processed input"}, nil
}
func (a *arenaDemoAgent) ProcessStream(ctx context.Context, input any) (<-chan base.AgentEvent, error) {
	ch := make(chan base.AgentEvent, 1)
	// One-shot short task (feed one result, close), not a
	// long-lived loop — adoption would outlive the per-request stream it
	// serves. Carries its own recover boundary so a panic cannot kill the
	// arena process; the channel closes either way.
	go func() {
		defer func() {
			close(ch)
			if r := recover(); r != nil {
				log.Error("arena: demo agent stream panicked (recovered)", "panic", r)
			}
		}()
		out, _ := a.Process(ctx, input)
		ch <- base.AgentEvent{Source: a.id, Data: out}
	}()
	return ch, nil
}
