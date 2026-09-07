// nolint: errcheck // best-effort: Close/Start/Stop/AddMessage errors are not actionable
// Command knowledge-import — structure-aware markdown knowledge base
// with RAG, multi-agent team import, and dialog-based chat.
//
// Purpose:
//
//	Provide a production-grade CLI that ingests markdown into a
//	PostgreSQL+pgvector knowledge base, supports RAG queries, chat,
//	multi-agent team import, and GA evolution of the import agent.
//
// Learning objectives:
//   - Parse CLI flags and dispatch to the correct mode handler.
//   - Build SDK Runtime options from a YAML config.
//   - Register tools (read_directory, read_note_file, import_knowledge,
//     query_knowledge) on the runtime.
//   - Run an interactive chat loop and a multi-agent team import.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/sdk.New
//   - github.com/Timwood0x10/ares/sdk.Runtime.NewAgent
//   - github.com/Timwood0x10/ares/sdk.Runtime.Evolve
//   - github.com/Timwood0x10/ares/sdk.Agent.Run
//   - github.com/Timwood0x10/ares/api/tools.ToolFunc
//
// Usage:
//
//	go run examples/_fixtures/11-knowledge-import/main.go --dir ./notes
//	go run examples/_fixtures/11-knowledge-import/main.go --save ./notes/arch.md
//	go run examples/_fixtures/11-knowledge-import/main.go --ask "how does it work?"
//	go run examples/_fixtures/11-knowledge-import/main.go --chat
//	go run examples/_fixtures/11-knowledge-import/main.go --team --dir ./notes
//	go run examples/_fixtures/11-knowledge-import/main.go --list
//
// --dir is the default mode when no other flag is provided.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/api/tools"
	ares_memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/sdk"
)

// cliOptions holds parsed command-line flags.
type cliOptions struct {
	configPath string
	tenantID   string
	dir        string
	file       string
	question   string
	chat       bool
	team       bool
	list       bool
	evolve     string // task description for GA evolution
	demo       bool   // full pipeline demo
	logFile    string // path to write debug log output
	// Chaos engineering
	chaosFailRate  float64
	chaosLatency   time.Duration
	chaosKillAfter int
	chaosHTTP      string
}

func main() {
	if err := run(); err != nil {
		slog.Error("knowledge-import failed", "error", err)
		os.Exit(1)
	}
}

// run is the top-level entry point: parse flags, configure logging,
// load config, create the knowledge base, then dispatch.
func run() error {
	opts := parseFlags()

	// ── Step 1: Configure logging ──
	// If --log-file is set, tee stderr output to both the file and stderr;
	// otherwise log to stderr only.
	if opts.logFile != "" {
		f, err := os.Create(opts.logFile)
		if err != nil {
			return fmt.Errorf("create log file: %w", err)
		}
		defer f.Close()
		slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f),
			&slog.HandlerOptions{Level: slog.LevelInfo})))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
			&slog.HandlerOptions{Level: slog.LevelInfo})))
	}

	// ── Step 2: Load config and create knowledge base ──
	// LoadConfig reads the YAML config file; NewKnowledgeBase connects
	// to PostgreSQL and sets up the embedding pipeline.
	cfg, err := LoadConfig(opts.configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	kb, err := NewKnowledgeBase(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := kb.Close(); cerr != nil {
			slog.Warn("close knowledge base", "error", cerr)
		}
	}()

	// ── Step 3: Dispatch to the selected mode ──
	return dispatch(ctx, kb, opts)
}

// parseFlags defines and parses all CLI flags, returning populated cliOptions.
func parseFlags() cliOptions {
	var opts cliOptions
	flag.StringVar(&opts.configPath, "config",
		"examples/_fixtures/11-knowledge-import/config.yaml", "Path to the YAML config file")
	flag.StringVar(&opts.tenantID, "tenant", "default", "Tenant namespace")
	flag.StringVar(&opts.dir, "dir", "", "Directory of markdown files to ingest recursively")
	flag.StringVar(&opts.file, "file", "", "Single markdown file to ingest")
	flag.StringVar(&opts.question, "ask", "", "Ask a question against the knowledge base")
	flag.BoolVar(&opts.chat, "chat", false, "Start interactive chat with RAG + tools")
	flag.BoolVar(&opts.team, "team", false, "Multi-agent team import from directory")
	flag.BoolVar(&opts.list, "list", false, "List stored documents")
	flag.BoolVar(&opts.demo, "demo", false, "Run full pipeline: evolve + kill + AKG + ask")
	flag.StringVar(&opts.evolve, "evolve", "", "Run GA evolution on the import agent with the given task")
	flag.StringVar(&opts.logFile, "log-file", "", "Path to write debug log output (e.g. examples/_fixtures/11-knowledge-import/run.log)")
	flag.Float64Var(&opts.chaosFailRate, "chaos-fail", 0, "Chaos: tool failure rate [0,1]")
	flag.DurationVar(&opts.chaosLatency, "chaos-latency", 0, "Chaos: tool latency (e.g. 500ms)")
	flag.IntVar(&opts.chaosKillAfter, "chaos-kill", 0, "Chaos: kill agent after N tool calls")
	flag.StringVar(&opts.chaosHTTP, "chaos-http", "", "Chaos: HTTP endpoint address (e.g. :8081)")
	flag.Parse()
	return opts
}

// dispatch routes to the correct mode handler based on parsed flags.
// Build chaos config from CLI flags.
func dispatch(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	chaosCfg := DefaultChaosConfig()
	if opts.chaosFailRate > 0 || opts.chaosLatency > 0 || opts.chaosKillAfter > 0 || opts.chaosHTTP != "" {
		chaosCfg = ChaosConfig{
			Enabled:        true,
			InjectFailRate: opts.chaosFailRate,
			InjectLatency:  opts.chaosLatency,
			KillAgentAfter: opts.chaosKillAfter,
		}
		slog.Warn("🧨 chaos enabled", "fail_rate", chaosCfg.InjectFailRate,
			"latency", chaosCfg.InjectLatency, "kill_after", chaosCfg.KillAgentAfter)
	}

	// Start chaos HTTP endpoint if configured.
	if opts.chaosHTTP != "" {
		go func() {
			if err := ArenaHTTPServer(ctx, opts.chaosHTTP); err != nil {
				slog.Warn("chaos http server stopped", "error", err)
			}
		}()
	}

	// Build tool wrapper for fault injection.
	toolWrapper := NewToolWrapper(chaosCfg)

	// ── Step 4: Switch on mode ──
	// Priority: chat > team > file > dir > evolve > ask > list > demo > usage.
	switch {
	case opts.chat:
		return runChat(ctx, kb, opts, toolWrapper)
	case opts.team:
		return runTeam(ctx, kb, opts, toolWrapper)
	case opts.file != "":
		return runIngestFile(ctx, kb, opts)
	case opts.dir != "":
		return runIngestDir(ctx, kb, opts)
	case opts.evolve != "":
		return runEvolve(ctx, kb, opts)
	case strings.TrimSpace(opts.question) != "":
		return runAsk(ctx, kb, opts)
	case opts.list:
		return runList(ctx, kb, opts)
	case opts.demo:
		return runDemo(ctx, kb, opts)
	default:
		printUsage()
		return nil
	}
}

// runIngestDir ingests all markdown files under a directory.
func runIngestDir(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	stats, err := kb.IngestDir(ctx, opts.tenantID, opts.dir)
	if err != nil {
		return err
	}
	fmt.Printf("Ingested %d files, %d chunks stored, %d skipped.\n",
		stats.Files, stats.Chunks, stats.Skipped)
	return nil
}

// runIngestFile ingests a single markdown file.
func runIngestFile(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	stored, skipped, err := kb.IngestFile(ctx, opts.tenantID, opts.file)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %s: %d chunks stored, %d skipped.\n", opts.file, stored, skipped)
	return nil
}

// runAsk queries the knowledge base and prints the answer with sources.
func runAsk(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	answer, err := kb.Ask(ctx, opts.tenantID, opts.question)
	if err != nil {
		return err
	}
	printAnswer(answer)
	return nil
}

// runList lists all stored documents for the given tenant.
func runList(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	docs, err := kb.ListDocuments(ctx, opts.tenantID)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		fmt.Println("(no documents stored)")
		return nil
	}
	fmt.Printf("Documents for tenant %q:\n", opts.tenantID)
	for _, d := range docs {
		fmt.Printf("  %-60s %d chunks\n", d.Source, d.Chunks)
	}
	return nil
}

// printAnswer formats an Answer to stdout with sources.
func printAnswer(answer *Answer) {
	fmt.Printf("\nQ: %s\n\n%s\n", answer.Question, answer.Text)
	if len(answer.Sources) == 0 {
		return
	}
	fmt.Printf("\nSources (generated=%v):\n", answer.Generated)
	for _, s := range answer.Sources {
		fmt.Printf("  [%d] score %.3f  %s\n", s.Rank, s.Score, s.Path)
	}
}

// printUsage prints CLI usage instructions.
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  --dir <path>    ingest a directory of markdown files recursively (default)")
	fmt.Println("  --file <path>   ingest a single markdown file")
	fmt.Println("  --ask <text>    ask a question against the knowledge base")
	fmt.Println("  --chat          start interactive chat with RAG + tools")
	fmt.Println("  --team --dir    multi-agent team import")
	fmt.Println("  --list          list stored documents")
	fmt.Println("  --tenant <id>   tenant namespace (default \"default\")")
	fmt.Println("  --config <path> config file path (default examples/_fixtures/11-knowledge-import/config.yaml)")
}

// ── Chat mode ─────────────────────────────────────────────────────────────

// sdkOptions creates SDK Runtime options from the example's config.yaml.
// Uses primary LLM config first, falls back to backup if primary is empty.
func sdkOptions(cfg *Config) []sdk.Option {
	opts := []sdk.Option{sdk.WithTrace(true)}

	// ── Step 5: Determine which LLM config to use ──
	// If primary provider/model are empty but backup is configured, use backup.
	llmCfg := &cfg.LLM
	useBackup := (llmCfg.Provider == "" || llmCfg.Model == "") && cfg.LLM.Backup != nil &&
		cfg.LLM.Backup.Provider != "" && cfg.LLM.Backup.Model != ""

	var target *LLMBackupConfig
	if useBackup {
		target = cfg.LLM.Backup
	} else {
		target = &LLMBackupConfig{
			Provider: llmCfg.Provider, Model: llmCfg.Model,
			BaseURL: llmCfg.BaseURL, APIKey: llmCfg.APIKey,
			Timeout: llmCfg.Timeout, MaxTokens: llmCfg.MaxTokens,
		}
	}

	// ── Step 6: Register the primary LLM provider ──
	// Each With* function configures the runtime for that provider's API.
	switch target.Provider {
	case "ollama":
		opts = append(opts, sdk.WithOllama(target.Model))
	case "openai":
		opts = append(opts, sdk.WithOpenAI(target.Model))
	case "anthropic":
		opts = append(opts, sdk.WithAnthropic(target.Model))
	case "openrouter":
		opts = append(opts, sdk.WithOpenRouter(target.Model))
	}
	if target.BaseURL != "" {
		opts = append(opts, sdk.WithBaseURL(target.BaseURL))
	}
	if target.APIKey != "" {
		opts = append(opts, sdk.WithAPIKey(target.APIKey))
	}

	// ── Step 7: Register backup as fallback LLM ──
	// If a different backup is configured, add it as a fallback so the
	// agent retries on the secondary provider if the primary fails.
	if !useBackup && cfg.LLM.Backup != nil && cfg.LLM.Backup.Provider != "" && cfg.LLM.Backup.Model != "" {
		b := cfg.LLM.Backup
		fallbackCfg := &core.LLMConfig{
			Provider:  providerName(b.Provider),
			Model:     b.Model,
			BaseURL:   b.BaseURL,
			APIKey:    b.APIKey,
			Timeout:   b.Timeout,
			MaxTokens: b.MaxTokens,
		}
		opts = append(opts, sdk.WithFallbackLLM(fallbackCfg))
	}

	// ── Step 8: Enable GA evolution and AKG knowledge fabric ──
	// WithEvolution enables the GA strategy evolution subsystem;
	// WithKnowledge enables the AKG knowledge graph.
	opts = append(opts, sdk.WithEvolution(), sdk.WithKnowledge())

	return opts
}

// providerName converts the yaml provider string to core.LLMProvider format.
func providerName(s string) core.LLMProvider {
	switch s {
	case "openai":
		return core.LLMProviderOpenAI
	case "ollama":
		return core.LLMProviderOllama
	case "anthropic":
		return core.LLMProviderAnthropic
	case "openrouter":
		return core.LLMProviderOpenRouter
	default:
		return core.LLMProvider(s)
	}
}

// runChat starts an interactive REPL: "import/store" triggers an agent
// with tools; everything else goes through the RAG Ask pipeline.
func runChat(ctx context.Context, kb *KnowledgeBase, opts cliOptions, tw *ToolWrapper) error {
	// ── Step 9: Create the SDK runtime for chat mode ──
	// sdk.New creates a Runtime from the provided options; registerTools
	// then wires the knowledge-base tools onto that runtime.
	rt, err := sdk.New(sdkOptions(kb.cfg)...)
	if err != nil {
		return fmt.Errorf("SDK: %v", err)
	}
	defer rt.Close()

	registerTools(rt, kb, opts.tenantID, tw)

	// ── Step 10: Set up memory manager (optional) ──
	// If memory is enabled in config, create a MemoryManager and start
	// a session so chat turns are persisted for future retrieval.
	var memMgr ares_memory.MemoryManager
	var sessionID string
	if cfg := kb.cfg; cfg.Memory.Enabled {
		memMgr, _ = ares_memory.NewMemoryManager(&ares_memory.MemoryConfig{
			Enabled: true, Storage: "memory", MaxHistory: cfg.Memory.MaxHistory,
			MaxSessions: cfg.Memory.MaxSessions, SessionTTL: 24 * time.Hour,
			TaskTTL: 7 * 24 * time.Hour, VectorDim: 128,
		})
		if memMgr != nil {
			memMgr.Start(ctx)
			sessionID, _ = memMgr.CreateSession(ctx, opts.tenantID)
		}
	}

	// ── Step 11: Interactive REPL loop ──
	// Read lines from stdin; "exit"/"quit" breaks the loop.
	slog.Info("Chat started. Ask questions or say: import <path> to import knowledge")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nYou: ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" || input == "exit" || input == "quit" {
			break
		}

		// Import request: use agent with tools.
		if strings.Contains(input, "import") || strings.Contains(input, "store") {
			agt := rt.NewAgent("assistant",
				sdk.WithInstruction("You are a knowledge base import assistant. To import files: first call read_directory to discover .md files, then for each file call read_note_file to get the content, then call import_knowledge with the file path and content."),
				sdk.WithTools(toolList()...),
			)
			result, err := agt.Run(ctx, input)
			if err != nil {
				slog.Warn("import agent error", "error", err)
				fmt.Printf("\nError: %v\n", err)
				continue
			}
			fmt.Printf("\nAssistant: %s\n", result.Output)
			continue
		}

		// Question: use RAG pipeline.
		answer, err := kb.Ask(ctx, opts.tenantID, input)
		if err != nil {
			slog.Warn("ask error", "error", err)
			fmt.Printf("\nError: %v\n", err)
			continue
		}
		printAnswer(answer)

		// Persist the exchange to memory for future context.
		if memMgr != nil && sessionID != "" {
			memMgr.AddMessage(ctx, sessionID, "user", input)
			memMgr.AddMessage(ctx, sessionID, "assistant", answer.Text)
		}
	}
	if memMgr != nil {
		memMgr.Stop(ctx)
	}
	return nil
}

// ── Team mode ─────────────────────────────────────────────────────────────

// runTeam creates a leader agent and 8 sub-agent importers, then runs
// the team to import markdown files in parallel.
func runTeam(ctx context.Context, kb *KnowledgeBase, opts cliOptions, tw *ToolWrapper) error {
	// ── Step 12: Resolve the directory to import ──
	// Default to the bundled notes/ directory if --dir is not provided.
	dir := opts.dir
	if dir == "" {
		dir = "examples/_fixtures/11-knowledge-import/notes"
	}
	if !strings.HasPrefix(dir, "/") && !strings.HasPrefix(dir, ".") && !strings.Contains(dir, ":") {
		// Not a path, treat as relative.
		dir = "./" + dir
	}

	// ── Step 13: Create the SDK runtime and register tools ──
	rt, err := sdk.New(sdkOptions(kb.cfg)...)
	if err != nil {
		return fmt.Errorf("SDK: %v", err)
	}
	defer rt.Close()

	registerTools(rt, kb, opts.tenantID, tw)

	// ── Step 14: Register the importer capability (H1) ──
	// RegisterAgent creates the importer agent (with the import tools) and
	// registers it as the handler for its capability; Submit dispatches the
	// task to it. The legacy NewTeam/team.Run leader path has been removed —
	// the peer flow has no privileged orchestrator. A supervisor tracks agent
	// health for chaos-engineered runs (resurrection demo).
	supervisor := NewAgentSupervisor(ChaosConfig{Enabled: true})

	task := fmt.Sprintf("Read all markdown files from %s and import them into tenant %s", dir, opts.tenantID)
	result, err := rt.Submit(ctx, sdk.Task{
		Capability: "importer",
		Input:      task,
	})
	if err != nil {
		return err
	}

	// Report resurrection status.
	if failed := supervisor.FailedAgents(); len(failed) > 0 {
		slog.Warn("sub-agents failed during import", "count", len(failed), "agents", failed)
	} else {
		slog.Info("all sub-agents completed successfully")
	}
	slog.Info("Import done", "duration", result.Duration)
	return nil
}

// ── GA Evolution ─────────────────────────────────────────────────────────

// runEvolve runs GA evolution on the import agent to optimize its parameters.
func runEvolve(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	cfg, err := LoadConfig(opts.configPath)
	if err != nil {
		return err
	}

	sdkOpts := sdkOptions(cfg)
	rt, err := sdk.New(sdkOpts...)
	if err != nil {
		return fmt.Errorf("SDK: %w", err)
	}
	defer rt.Close()

	registerTools(rt, kb, opts.tenantID, NewToolWrapper(DefaultChaosConfig()))

	agent := rt.NewAgent("evolver",
		sdk.WithInstruction("You are a knowledge import specialist. Read files and import them."),
		sdk.WithTools(toolList()...),
	)

	// ── Step 17: Run GA evolution on the agent ──
	// rt.Evolve runs the GA loop: it creates strategy variants, runs each
	// against the task, scores them, and evolves over generations.
	task := opts.evolve
	if task == "" {
		task = "Read markdown files from notes and import them into the knowledge base"
	}

	slog.Info("🧬 GA evolution started", "task", task)
	start := time.Now()
	result, err := rt.Evolve(ctx, agent, task)
	if err != nil {
		return fmt.Errorf("evolve: %w", err)
	}
	elapsed := time.Since(start)

	slog.Info("🧬 GA evolution complete",
		"duration", elapsed.Round(time.Millisecond),
		"result", result,
	)
	fmt.Printf("\n🧬 Evolved Strategy: %s\n", result)
	return nil
}

// ── Demo pipeline ─────────────────────────────────────────────────────────

// runDemo runs the full pipeline: evolve → kill → AKG → ask.
func runDemo(ctx context.Context, kb *KnowledgeBase, opts cliOptions) error {
	slog.Info("=== DEMO: Full Pipeline ===")

	// 1. GA Evolution.
	slog.Info("--- Phase 1: GA Evolution ---")
	if err := runEvolve(ctx, kb, opts); err != nil {
		slog.Warn("evolution failed (continuing)", "error", err)
	}

	// 2. Chaos kill + resurrection (Team mode with kill).
	slog.Info("--- Phase 2: Chaos Kill + Resurrection ---")
	if err := runTeam(ctx, kb, cliOptions{
		tenantID:       opts.tenantID,
		dir:            opts.dir,
		chaosFailRate:  0.0,
		chaosKillAfter: 3,
		configPath:     opts.configPath,
	}, NewToolWrapper(ChaosConfig{
		Enabled:        true,
		KillAgentAfter: 3,
	})); err != nil {
		slog.Warn("team with chaos failed (continuing)", "error", err)
	}

	// 3. AKG Build (run as subprocess).
	slog.Info("--- Phase 3: AKG Build ---")
	akgCmd := exec.CommandContext(ctx, "go", "run", "examples/_fixtures/11-knowledge-import/akg/")
	akgOut, err := akgCmd.CombinedOutput()
	if err != nil {
		slog.Warn("AKG build failed (continuing)", "error", err)
	} else {
		fmt.Print(string(akgOut))
	}

	// 4. Ask query.
	slog.Info("--- Phase 4: RAG Query ---")
	askOpts := cliOptions{
		tenantID:   opts.tenantID,
		question:   "What are the key differences between zk-SNARK and zk-STARK?",
		configPath: opts.configPath,
	}
	if err := runAsk(ctx, kb, askOpts); err != nil {
		slog.Warn("ask failed (continuing)", "error", err)
	}

	slog.Info("=== DEMO: Complete ===")
	return nil
}

// ── Tools ─────────────────────────────────────────────────────────────────

// The four knowledge-base tools registered on the runtime.
// They are package-level vars so registerTools can assign closures that
// capture the knowledge base, tenant, and tool wrapper.
var (
	dirTool  tools.Tool
	fileTool tools.Tool
	impTool  tools.Tool
	qryTool  tools.Tool
)

// toolList returns all four registered tools as a slice.
func toolList() []tools.Tool {
	return []tools.Tool{dirTool, fileTool, impTool, qryTool}
}

// registerTools builds and registers the four knowledge-base tools:
// read_directory, read_note_file, import_knowledge, and query_knowledge.
func registerTools(rt *sdk.Runtime, kb *KnowledgeBase, tenantID string, tw *ToolWrapper) {
	dirTool = tools.ToolFunc{
		ToolName: "read_directory",
		ToolDesc: "Recursively find all .md files under a directory.",
		ToolParams: map[string]any{
			"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Directory path"},
			}, "required": []string{"path"},
		},
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			path, _ := params["path"].(string)
			if path == "" {
				return nil, fmt.Errorf("path required")
			}
			files, err := findMarkdownFiles(path)
			if err != nil {
				return nil, fmt.Errorf("walk: %w", err)
			}
			if len(files) == 0 {
				return "(no .md files)", nil
			}
			return strings.Join(files, "\n"), nil
		},
	}

	fileTool = tools.ToolFunc{
		ToolName: "read_note_file",
		ToolDesc: "Read a file's content.",
		ToolParams: map[string]any{
			"type": "object", "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Full file path"},
			}, "required": []string{"path"},
		},
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			path, _ := params["path"].(string)
			if path == "" {
				return nil, fmt.Errorf("path required")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read: %w", err)
			}
			return string(data), nil
		},
	}

	impTool = tools.ToolFunc{
		ToolName: "import_knowledge",
		ToolDesc: "Import markdown content into the knowledge base. Parses structure (headings, code blocks, tables), chunks by section, vectorizes, and stores in PostgreSQL.",
		ToolParams: map[string]any{
			"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path or title"},
				"content": map[string]any{"type": "string", "description": "Markdown content"},
			}, "required": []string{"path", "content"},
		},
		Fn: func(ctx context.Context, params map[string]any) (any, error) {
			path, _ := params["path"].(string)
			content, _ := params["content"].(string)
			if path == "" || content == "" {
				return nil, fmt.Errorf("path and content required")
			}
			doc := ParseContent(path, content)
			chunks := ChunkDocument(doc, kb.cfg.Knowledge)
			docID := uuid.New().String()

			// Build chunk models.
			var chunkModels []*models.KnowledgeChunk
			for _, c := range chunks {
				m := &models.KnowledgeChunk{
					TenantID: tenantID, DocumentID: docID,
					SourceType: "markdown", Source: doc.Path,
					ChunkIndex: c.Index, Content: c.Content,
					ContentHash:      hashWithIndex(c.Content, c.Index),
					EmbeddingVersion: 1,
					EmbeddingStatus:  models.EmbeddingStatusPending,
					Metadata:         chunkMetadata(c, doc),
				}
				chunkModels = append(chunkModels, m)
			}

			// Embed all chunks.
			var lastErr error
			ok := 0
			for _, m := range chunkModels {
				vec, err := kb.embedder.EmbedWithPrefix(ctx, m.Content, kb.cfg.Knowledge.PassagePrefix)
				if err != nil {
					lastErr = err
					continue
				}
				if len(vec) != kb.cfg.Embedding.Dimensions {
					continue
				}
				m.Embedding = postgres.NormalizeVector(vec)
				m.EmbeddingStatus = models.EmbeddingStatusCompleted
				ok++
			}
			if ok == 0 {
				return nil, fmt.Errorf("no chunks embedded: %v", lastErr)
			}

			// Batch insert.
			if err := retryWithBackoff(ctx, 3, func() error {
				return kb.repo.CreateBatch(ctx, chunkModels)
			}); err != nil {
				return nil, fmt.Errorf("batch insert: %w", err)
			}
			return fmt.Sprintf("✅ Imported %d/%d sections from %s", ok, len(chunks), path), nil
		},
	}

	qryTool = tools.ToolFunc{
		ToolName: "query_knowledge",
		ToolDesc: "Search the knowledge base.",
		ToolParams: map[string]any{
			"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query"},
			}, "required": []string{"query"},
		},
		Fn: func(ctx context.Context, params map[string]any) (any, error) {
			query, _ := params["query"].(string)
			if query == "" {
				return nil, fmt.Errorf("query required")
			}
			answer, err := kb.Ask(ctx, tenantID, query)
			if err != nil {
				return nil, fmt.Errorf("search: %w", err)
			}
			if len(answer.Sources) == 0 {
				return "(no results)", nil
			}
			var b strings.Builder
			b.WriteString(answer.Text)
			b.WriteString("\n\nSources:\n")
			for _, s := range answer.Sources {
				fmt.Fprintf(&b, "  [%d] score %.3f  %s\n", s.Rank, s.Score, s.Path)
			}
			return b.String(), nil
		},
	}

	for _, t := range toolList() {
		rt.ToolRegistry().Register(t)
	}
}
