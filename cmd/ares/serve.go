package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_shutdown"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	akf_mcp "github.com/Timwood0x10/ares/internal/knowledge/mcp"
	"github.com/Timwood0x10/ares/internal/runtime/archive"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
	core_tools "github.com/Timwood0x10/ares/internal/tools/resources/core"
)

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
			// must never trap the operator in an unstoppable process (N1).
			// NOT adopted into the orchestrator (K3 exception): this watcher
			// must stay alive while the managed pools are being drained —
			// exactly the window when adopted loops are being torn down.
			// One-shot short task with its own recover boundary per the K3
			// exception rule.
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
			// P1-3: give SystemRuntime Shutdown its own 15s budget so it
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
			log.Printf("system_runtime snapshot (shutdown): %s", string(snapJSON))
		}
		// Wait for Bootstrap's background goroutines (distillation subscriber,
		// GA evolution ticker, LLM suggestion ticker) to exit after the
		// context is cancelled, so none outlives the graceful shutdown.
		comp.WaitBackground()
		return nil
	})

	// --- EventStore (archive-enabled, shared pipeline) ---
	// Build the archive-enabled store once and inject it into Bootstrap so
	// `ares serve` uses the same construction path as `ares start`
	// (archive.NewCompactableStoreWithArchive is the single source).
	// Archive defaults to on; disable via memory.archive.enabled: false.
	// The raw *MemoryEventStore is unused here — serve consumes the store via
	// the EventStore interface only — so it is discarded.
	compactableStore, _, err := archive.NewCompactableStoreWithArchive(cfg.Memory.Archive)
	if err != nil {
		return fmt.Errorf("create event store: %w", err)
	}

	// --- Bootstrap: infrastructure components via single wiring hub ---
	// Uses internal/ares_bootstrap for EventStore, Runtime, Memory.
	// MCP setup is handled separately below for registry bridging. The store
	// is passed via deps so Bootstrap wires Runtime/Memory against the real
	// archive-enabled store instead of creating a throwaway MemoryEventStore.
	comp, err := ares_bootstrap.Bootstrap(ctx, cfg, &ares_bootstrap.BootstrapDeps{
		EventStore: compactableStore,
	})
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	// Publish the assembled components to the signal goroutine via the atomic
	// pointer so the shutdown snapshot/WaitBackground reads never race.
	compPtr.Store(comp)
	// N1: assembly-phase exit check — if a shutdown signal arrived during the
	// (potentially long) Bootstrap, abort the startup instead of proceeding
	// to wire components and start the runtime on a canceled context.
	if err := ctx.Err(); err != nil {
		log.Printf("serve: shutdown was requested during assembly (%v); aborting startup", err)
		return normalizeShutdownErr(err)
	}
	store := comp.EventStore
	mgr := comp.Runtime

	// --- Runtime config store + hot-reload watcher (P1) ---
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

	// Stage 3 fix (B01): EventStore is wired into Memory during Bootstrap,
	// not post-Bootstrap here. validateServeConfig has already enforced that
	// the full agent-serving entry point has its required Memory component.

	// Stage 1 observability: report the System Runtime component snapshot
	// (names, modes, lifecycle states) so operators can confirm which
	// components were assembled and reached Ready at startup.
	if snapJSON, snapErr := comp.Snapshot().JSON(); snapErr == nil {
		log.Printf("system_runtime snapshot (startup): %s", string(snapJSON))
	} else {
		log.Printf("system_runtime snapshot unavailable: %v", snapErr)
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
				log.Printf("AKF: failed to register tool %q: %v", t.Name, err)
			}
		}
		log.Printf("AKF tools registered with shared KnowledgeRuntime: %d", len(akfSvc.Tools()))
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

	// REVIEW #11 (second half): expose the environment-capability searcher as
	// the `search_capabilities` tool so agents can actively discover tools,
	// skills, and native commands. Registered before the binder is built so it
	// flows into the agent tool set naturally. comp.SkillsRegistry may be nil
	// (skills disabled) — the searcher skips that source.
	if err := registerCapabilitySearch(internalReg, comp.SkillsRegistry); err != nil {
		return fmt.Errorf("register capability search: %w", err)
	}

	// W8 closure: expose the skill catalog as first-class agent tools
	// (skill_search / skill_load / ...) so the LLM can drive progressive
	// disclosure itself instead of only receiving the resident prompt block.
	if comp.SkillCatalog != nil {
		for _, t := range ares_skills.CatalogTools(comp.SkillCatalog) {
			if err := internalReg.Register(t); err != nil {
				log.Printf("serve: register skill tool %q skipped: %v", t.Name(), err)
			}
		}
		log.Printf("serve: skill catalog tools registered (progressive disclosure active)")
	}

	toolBinder := newToolBinder(internalReg)
	log.Printf("tools registered: %d", len(toolBinder.ListTools()))

	// --- Capability Planner bridge for agent tool fallback ---
	if bridge := newPlannerBridge(internalReg); bridge != nil {
		toolBinder.WithPlannerBridge(bridge)
		log.Println("planner bridge: attached")
	}

	// Step Y.3: arm the tool-call perception channel. The decorator wraps the
	// binder AFTER the planner bridge is attached, so planner-resolved calls are
	// measured too, and it is applied at the single site every execution body
	// (sub executor and agentfabric ChatCognition) receives its binder from —
	// instrumenting either loop instead would leave the other blind. A nil
	// recorder (channel not armed — the default) returns the binder untouched.
	if comp.NewEvolution != nil && comp.NewEvolution.ChannelFeedback.ToolCallsArmed() {
		toolBinder = sub.ObserveToolCalls(toolBinder, comp.NewEvolution.ChannelFeedback)
		log.Printf("serve: tool-call feedback channel armed (evolution reads tool outcomes)")
	}

	// --- ChatClient for native tool calling ---
	chatClient, err := createChatClient(cfg)
	if err != nil {
		return fmt.Errorf("create chat client: %w", err)
	}
	log.Printf("chat client created: provider=%s model=%s", cfg.LLM.Provider, cfg.LLM.Model)

	// --- Create + register agents with the runtime manager ---
	subAgents, peerKernel, err := createAndServeAgents(ctx, cfg, internalReg, llmAdapter, chatClient, toolBinder, comp, mgr)
	if err != nil {
		return err
	}

	// --- Peer registry: enable direct agent-to-agent messaging ---
	// setupPeerRegistry builds the registry; the kernel handle powers
	// collaboration-topic execution through the fabric DAG (fusion C2). The
	// registry is retained on the kernel handle (N4) so it stays reachable for
	// direct peer messaging / capability discovery instead of being discarded.
	reg, err := setupPeerRegistry(subAgents, comp, peerKernel)
	if err != nil {
		return err
	}
	if peerKernel != nil {
		peerKernel.peerRegistry = reg
		log.Printf("serve: peer registry retained on kernel (%d agents)", len(reg.IDs()))
	}

	// --- Runtime introspection control plane (monitoring.md Phase 4):
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
	// self-dispatch (self-dispatch was removed in v0.3.0).

	// --- Dashboard APIv2 server (M3/M4 observability read side) ---
	// The old standalone dashboard :8090 server was removed in Phase 4; the
	// M3/M4 observability providers now feed introspect.ControlServer below.

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
