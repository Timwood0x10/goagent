// ARES unified CLI — single entry point for all ARES commands.
//
// Usage:
//
//	ARES serve                         Start full agent runtime (LLM + MCP + introspection)
//	ARES agent list                    List all registered agents
//	ARES arena run <scenario>          Run chaos scenario
//	ARES arena validate <scenario>     Validate scenario
//	ARES arena list [dir]              List scenarios
//	ARES arena serve                   Start arena HTTP server
//	ARES arena survival                Run survival test
//	ARES arena inspect                 Inspect arena results
//	ARES evolution run                 Run one evolution cycle
//	ARES evolution status              Show evolution system status
//	ARES flight inspect <taskID>       Inspect flight data
//	ARES flight replay <taskID>        Replay flight data
//	ARES knowledge build <goal>        Build a knowledge graph (via HTTP API)
//	ARES recall query <text>           Recall archived rounds by text
//	ARES recall round <N>              Recall one archived round
//	ARES evolution run [flags]         Run the GA evolution loop
//	ARES status                        Show runtime status
//	ARES init / run / bench            Scaffold, start dev runtime, run benchmarks
//	ARES mcp-null serve                Start minimal MCP null server (stdio)
//	ARES db migrate                    Run full DB migration
//	ARES db create-table               Create distilled_memories table
//	ARES db check-rls                  Check RLS policies
//	ARES version                       Show version
//	ARES doctor                        Diagnose environment
//	ARES status                        Show runtime status at a glance
//	ARES dashboard                     Open the runtime introspection panel
//	ARES init                          Scaffold new project
//	ARES run                           Run agent from config file
//	ARES bench                         Run benchmark
//
// Merged CLI source: main.go, dev.go, auth.go, knowledge_cli.go, recall.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/runtime/archive"
	"github.com/Timwood0x10/ares/sdk"
)

var rootCmd = &cobra.Command{
	Use:   "ares",
	Short: "ARES — Agent Runtime & Evolution System",
	Long: `ARES is the unified CLI for the Agent Runtime & Evolution System.

It provides commands for running agents, managing databases,
inspecting flight data, running chaos engineering scenarios,
and debugging MCP servers.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// Dev commands — ares init | run | bench | doctor | version

var version = "dev" // set via ldflags at build time

func init() {
	// version
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show ARES version",
		Run: func(_ *cobra.Command, _ []string) {
			v := version
			if v == "dev" {
				// ldflags injection (main.version) absent (e.g. `go run` or a
				// build without Makefile): fall back to the module pseudo-version
				// from build info when one exists.
				if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
					v = info.Main.Version
				}
			}
			fmt.Printf("ARES %s (%s/%s, %s)\n", v, runtime.GOOS, runtime.GOARCH, runtime.Version())
		},
	})

	// doctor
	rootCmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Diagnose ARES environment",
		RunE:  runDoctor,
	})

	// init
	var initDir string
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a new ARES project",
		RunE:  runInit,
	}
	initCmd.Flags().StringVarP(&initDir, "dir", "d", ".", "Project directory")
	rootCmd.AddCommand(initCmd)

	// run
	var configPath string
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run agent from config file",
		RunE:  runRun,
	}
	runCmd.Flags().StringVarP(&configPath, "config", "c", "ares.yaml", "Config file path")
	rootCmd.AddCommand(runCmd)

	// bench
	benchCmd := &cobra.Command{
		Use:   "bench",
		Short: "Run benchmark",
		RunE:  runBench,
	}
	benchCmd.Flags().StringP("format", "f", "markdown", "Output format: markdown | json")
	rootCmd.AddCommand(benchCmd)
}

// ── doctor ─────────────────────────────────────────────────────

func runDoctor(_ *cobra.Command, _ []string) error {
	ok := true

	fmt.Println("🔍 ARES Doctor")
	fmt.Println()

	// Go version
	fmt.Printf("  Go:       %s", runtime.Version())
	if v := runtime.Version(); strings.HasPrefix(v, "go1.26") || strings.HasPrefix(v, "go1.25") {
		fmt.Println(" ✅")
	} else {
		fmt.Println(" ⚠  Go 1.25+ recommended")
	}

	// LLM key check
	providers := []struct {
		name string
		env  string
	}{
		{"OpenAI", "OPENAI_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"OpenRouter", "OPENROUTER_API_KEY"},
	}
	for _, p := range providers {
		if v := os.Getenv(p.env); v != "" {
			fmt.Printf("  %-10s ✅ (%s...)\n", p.name, v[:min(8, len(v))]+"...")
		} else {
			fmt.Printf("  %-10s ❌ set %s\n", p.name, p.env)
			ok = false
		}
	}

	// Ollama check
	if err := exec.Command("ollama", "--version").Run(); err == nil {
		fmt.Println("  Ollama    ✅")
	} else {
		fmt.Println("  Ollama    ❌ not found (optional, install for local LLM)")
	}

	// Git check
	if err := exec.Command("git", "--version").Run(); err == nil {
		fmt.Println("  Git       ✅")
	} else {
		fmt.Println("  Git       ❌ not found")
	}

	fmt.Println()
	if ok {
		fmt.Println("✅ Environment looks good")
	} else {
		fmt.Println("⚠  Some checks failed — see above")
	}
	return nil
}

// ── init ───────────────────────────────────────────────────────

func runInit(cmd *cobra.Command, args []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		dir = "."
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Detect ARES module path for the replace directive.
	aresMod := "github.com/Timwood0x10/ares"
	aresRoot := findAresRoot()
	replaceLine := ""
	if aresRoot != "" {
		replaceLine = fmt.Sprintf("replace %s => %s\n", aresMod, aresRoot)
	}

	// go.mod template.
	goMod := fmt.Sprintf(`module myapp

go 1.26

require %s v0.0.0

%s`, aresMod, replaceLine)

	// main.go template.
	mainGo := `package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	rt := sdk.NewRuntime(
		sdk.WithOllama("llama3.2"),
		sdk.WithDefaultMemory(),
	)
	defer rt.Close()

	agent := rt.NewAgent("assistant",
		sdk.WithInstruction("You are a helpful assistant."),
	)

	input := "Hello!"
	if len(os.Args) > 1 {
		input = os.Args[1]
	}

	result, err := agent.Run(ctx, input)
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	fmt.Println(result.Output)
	fmt.Printf("(tokens: %d, tools: %d, took: %v)\n",
		result.TokenUsage.Total, result.ToolCalls, result.Duration)
}
`

	// ares.yaml config template.
	aresYaml := `# ARES project configuration
llm:
  provider: ollama    # openai | anthropic | openrouter
  model: llama3.2

memory:
  enabled: true

tools:
  builtin: true

reflection:
  enabled: false

evolution:
  enabled: false
`

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainGo), 0644); err != nil {
		return fmt.Errorf("write main.go: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ares.yaml"), []byte(aresYaml), 0644); err != nil {
		return fmt.Errorf("write ares.yaml: %w", err)
	}

	fmt.Printf("✅ Created ARES project in %s\n", dir)
	fmt.Println("   Files: go.mod, main.go, ares.yaml")
	fmt.Println("   Run:   cd", dir, "&& go run .")
	return nil
}

// findAresRoot walks up from the current directory looking for go.mod
// containing "github.com/Timwood0x10/ares".
func findAresRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for {
		gm := filepath.Join(dir, "go.mod")
		data, err := os.ReadFile(gm)
		if err == nil && strings.Contains(string(data), "module github.com/Timwood0x10/ares") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// ── run ────────────────────────────────────────────────────────

func runRun(cmd *cobra.Command, _ []string) error {
	configPath, _ := cmd.Flags().GetString("config")

	// Auto-detect config if -c not provided.
	if configPath == "" || configPath == "ares.yaml" {
		if _, err := os.Stat("ares.yaml"); err == nil {
			configPath = "ares.yaml"
		} else if _, err := os.Stat("config/ares.yaml"); err == nil {
			configPath = "config/ares.yaml"
		}
	}
	if configPath == "" {
		return errors.New("no ares.yaml found; use -c to specify, or create one with 'ares init'")
	}

	// Load and parse config.
	cfg, err := sdk.LoadConfigFile(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	opts, err := cfg.ToOptions()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	opts = append(opts, sdk.WithTrace(true))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rt := sdk.NewRuntime(opts...)
	defer rt.Close()

	agent := rt.NewAgent("cli-agent",
		sdk.WithInstruction("You are a helpful assistant."),
	)

	// Read input from args (skip run subcommand and config flags).
	input := strings.Join(parseRunArgs(), " ")
	if input == "" {
		fmt.Print("Enter prompt: ")
		_, _ = fmt.Scanln(&input)
		input = strings.TrimSpace(input)
	}
	if input == "" {
		input = "Say hello"
	}

	result, err := agent.Run(ctx, input)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	fmt.Println(result.Output)
	fmt.Printf("(tokens: %d, tools: %d, took: %v)\n",
		result.TokenUsage.Total, result.ToolCalls, result.Duration)
	return nil
}

// parseRunArgs returns os.Args with the "run" subcommand and --config flag
// removed, so remaining words can be used as the input prompt.
func parseRunArgs() []string {
	var out []string
	skipNext := false
	for i, a := range os.Args {
		if i == 0 {
			continue // skip program name
		}
		if a == "run" {
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		if a == "-c" || a == "--config" {
			skipNext = true
			continue
		}
		// Also strip `-c=path` / `--config=path` forms so they never leak into
		// the prompt arguments.
		if strings.HasPrefix(a, "--config=") || strings.HasPrefix(a, "-c=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ── bench ──────────────────────────────────────────────────────

type benchResult struct {
	Task      string        `json:"task"`
	Success   bool          `json:"success"`
	Output    string        `json:"output"`
	ToolCalls int           `json:"tool_calls"`
	Tokens    int           `json:"tokens"`
	Latency   time.Duration `json:"latency_ms"`
	MemoryHit bool          `json:"memory_hit"`
}

type benchReport struct {
	Date        string        `json:"date"`
	Model       string        `json:"model"`
	Provider    string        `json:"provider"`
	Results     []benchResult `json:"results"`
	AvgLatency  time.Duration `json:"avg_latency_ms"`
	TotalTokens int           `json:"total_tokens"`
	TotalTools  int           `json:"total_tool_calls"`
	PassRate    float64       `json:"pass_rate"`
}

func runBench(cmd *cobra.Command, _ []string) error {
	format, _ := cmd.Flags().GetString("format")
	if format == "" {
		format = "markdown"
	}

	ctx := context.Background()

	rt := sdk.NewRuntime(sdk.WithTrace(false))
	defer rt.Close()

	agent := rt.NewAgent("bench-agent",
		sdk.WithInstruction("Respond concisely in under 20 words."),
	)

	tasks := []string{
		"Say hello in English",
		"What is 2+2?",
		"Name three primary colors",
		"Convert 100 Celsius to Fahrenheit",
		"List the planets in order from the sun",
	}

	var results = make([]benchResult, 0, len(tasks))
	var totalDuration time.Duration
	var totalTokens, totalTools int
	passed := 0

	for i, task := range tasks {
		start := time.Now()
		result, err := agent.Run(ctx, task)
		d := time.Since(start)

		br := benchResult{
			Task:    task,
			Success: err == nil,
			Latency: d,
		}
		if err == nil {
			br.Output = truncateStr(result.Output, 60)
			br.ToolCalls = result.ToolCalls
			br.Tokens = result.TokenUsage.Total
			br.MemoryHit = result.MemoryUsed
			totalDuration += d
			totalTokens += result.TokenUsage.Total
			totalTools += result.ToolCalls
			passed++
		} else {
			br.Output = err.Error()
		}
		results = append(results, br)

		if format == "markdown" {
			status := "✅"
			if !br.Success {
				status = "❌"
			}
			fmt.Printf("| %d | %-40s | %s %s | %d tok | %v |\n",
				i+1, br.Task, status, truncateStr(br.Output, 30), br.Tokens, br.Latency.Round(time.Millisecond))
		}
	}

	report := benchReport{
		Date:        time.Now().Format(time.RFC3339),
		Model:       rt.GetModel(),
		Provider:    rt.GetProvider(),
		Results:     results,
		AvgLatency:  totalDuration / time.Duration(len(tasks)),
		TotalTokens: totalTokens,
		TotalTools:  totalTools,
		PassRate:    float64(passed) / float64(len(tasks)) * 100,
	}

	switch format {
	case "json":
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
	default:
		fmt.Println()
		fmt.Printf("**Summary** | **Value**\n")
		fmt.Printf("---|---\n")
		fmt.Printf("Model | %s\n", report.Model)
		fmt.Printf("Provider | %s\n", report.Provider)
		fmt.Printf("Tasks | %d/%d passed\n", passed, len(tasks))
		fmt.Printf("Pass Rate | %.0f%%\n", report.PassRate)
		fmt.Printf("Avg Latency | %v\n", report.AvgLatency.Round(time.Millisecond))
		fmt.Printf("Total Tokens | %d\n", report.TotalTokens)
		fmt.Printf("Total Tool Calls | %d\n", report.TotalTools)
	}
	return nil
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// Auth commands — ares auth token

// defaultTokenTTL is the fallback lifetime for `ares auth token` when neither
// --ttl nor security.jwt_expiry is configured.
const defaultTokenTTL = "24h"

func init() {
	authCmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage ARES authentication (JWT tokens)",
	}

	var (
		tokenRole       string
		tokenSubject    string
		tokenTTL        string
		tokenConfigPath string
	)
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Issue a signed JWT for protected HTTP endpoints",
		Long: `Issue a signed JWT (HS256) for protected HTTP endpoints (agent kill/resume/
retry, MCP tool calls, chaos actions). The token is signed with the configured
JWT secret — the same one the serve process validates. Configure the secret in
security.jwt_secret in ares.yaml or via the ARES_JWT_SECRET environment
variable, and enable enforcement with security.auth_enabled: true (or
ARES_AUTH_ENABLED=1).

Roles: admin (full control), operator (write, no destructive chaos), agent
(read-only).

The token lifetime resolves in this order: --ttl flag, then the
security.jwt_expiry config, then the built-in default of ` + defaultTokenTTL + `.

Example:
  ARES_JWT_SECRET=changeme ares auth token --role operator --sub "deploy-user"`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := ares_config.Load(tokenConfigPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			secret := tokenEnvSecret(cfg)
			if secret == "" {
				return errors.New("no JWT secret configured: set security.jwt_secret or ARES_JWT_SECRET")
			}
			// Lifetime precedence: explicit --ttl flag > security.jwt_expiry
			// config > defaultTokenTTL. An empty flag leaves the config (and
			// the default) in charge.
			ttlStr := tokenTTL
			if ttlStr == "" {
				ttlStr = cfg.Security.JWTExpiry
			}
			if ttlStr == "" {
				ttlStr = defaultTokenTTL
			}
			ttl, err := time.ParseDuration(ttlStr)
			if err != nil {
				return fmt.Errorf("parse ttl %q: %w", ttlStr, err)
			}
			if _, err := ares_security.ParseRole(tokenRole); err != nil {
				return err
			}
			tok, err := ares_security.SignJWT([]byte(secret), tokenSubject, tokenRole, ttl, time.Now())
			if err != nil {
				return err
			}
			fmt.Println(tok)
			return nil
		},
	}
	tokenCmd.Flags().StringVar(&tokenConfigPath, "config", "", "Path to ares.yaml (uses ARES_JWT_SECRET otherwise)")
	tokenCmd.Flags().StringVar(&tokenRole, "role", "operator", "Role: admin, operator, or agent")
	tokenCmd.Flags().StringVar(&tokenSubject, "sub", "cli-user", "Token subject")
	tokenCmd.Flags().StringVar(&tokenTTL, "ttl", "", "Token lifetime (e.g. 24h, 1h30m); defaults to security.jwt_expiry or "+defaultTokenTTL)
	authCmd.AddCommand(tokenCmd)

	rootCmd.AddCommand(authCmd)
}

// tokenEnvSecret exposes the JWT secret resolution used by both the CLI and
// serve wiring: the environment variable wins, then the config file. It is a
// small helper kept beside the command so tests can exercise the precedence
// without spawning a subprocess.
func tokenEnvSecret(cfg *ares_config.Config) string {
	if v := os.Getenv("ARES_JWT_SECRET"); v != "" {
		return v
	}
	return cfg.Security.JWTSecret
}

var knowledgeCmd = &cobra.Command{
	Use:   "knowledge",
	Short: "Knowledge graph management (via HTTP API)",
	Long: `Knowledge graph commands are available via the running ARES HTTP API.

Usage:
  ares serve                  Start the HTTP server first
  curl localhost:PORT/api/v1/knowledge/build -d '{"goal":"..."}'
  curl localhost:PORT/api/v1/knowledge/context -d '{"goal":"...", "formats":["prompt"]}'`,
}

var knowledgeBuildCmd = &cobra.Command{
	Use:   "build <goal>",
	Short: "Build a knowledge graph (requires running ares serve)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf(`knowledge build is available via the HTTP API.
Start the server with 'ares serve' first, then send a POST request:

  curl -X POST http://localhost:PORT/api/v1/knowledge/build \
    -H "Content-Type: application/json" \
    -d '{"goal":%q, "max_tokens":5000, "for_graph":3000}'`, args[0])
	},
}

func init() {
	rootCmd.AddCommand(knowledgeCmd)
	knowledgeCmd.AddCommand(knowledgeBuildCmd)
}

// The `recall` command tree queries the round archive. Round archives
// persist conversation rounds as independent JSON files so they survive
// event-stream compaction — recall gives operators a way to search past
// rounds by keyword or inspect a specific round by number.

var recallCmd = &cobra.Command{
	Use:   "recall",
	Short: "Query round archives (survive compaction)",
	Long: `Query the round archive — conversation rounds persisted as independent
round_N.json files under the configured archive directory.

Archiving is enabled by default. Disable with memory.archive.enabled: false
in the config YAML.

Subcommands:
  recall query <text>   Search archives by keyword and print matching rounds.
  recall round <N>      Print a specific round's archive record as JSON.`,
}

var recallQueryCmd = &cobra.Command{
	Use:   "query <text>",
	Short: "Search archives by keyword",
	Long: `Search archived rounds for the given keyword (case-insensitive substring
match across summary, decisions, file paths, and identifier refs). Prints a
human-readable conclusion for each matching round, newest first.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRecallQuery(args[0])
	},
}

var recallRoundCmd = &cobra.Command{
	Use:   "round <N>",
	Short: "Print a specific round's archive record",
	Long: `Print the round_N.json archive record as pretty-printed JSON. The round
number must be a positive integer.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRecallRound(args[0])
	},
}

var recallConfigPath string

func init() {
	rootCmd.AddCommand(recallCmd)
	recallCmd.AddCommand(recallQueryCmd)
	recallCmd.AddCommand(recallRoundCmd)
	for _, c := range []*cobra.Command{recallQueryCmd, recallRoundCmd} {
		c.Flags().StringVarP(&recallConfigPath, "config", "c", "", "Path to config YAML")
	}
}

// loadRecallConfig resolves the config path (falling back to ares.yaml,
// mirroring loadServeConfig), loads it, and applies environment overrides.
// Returns the loaded config or a wrapped error.
func loadRecallConfig() (*ares_config.Config, error) {
	configPath := recallConfigPath
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
	}

	cfg, err := ares_config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if err := ares_config.LoadFromEnv(cfg); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	return cfg, nil
}

// runRecallQuery searches the archive directory for rounds matching the query
// and prints a human-readable conclusion. When the archive directory does not
// exist (no rounds archived yet), it prints a friendly message and returns nil.
func runRecallQuery(query string) error {
	cfg, err := loadRecallConfig()
	if err != nil {
		return err
	}
	if !cfg.Memory.Archive.IsEnabled() {
		return errors.New("archive is disabled in config (set memory.archive.enabled: true or omit it)")
	}

	reader, err := archive.NewFileArchiveReader(cfg.Memory.Archive.Dir)
	if err != nil {
		return fmt.Errorf("create archive reader: %w", err)
	}

	// Handle a missing archive directory gracefully so a fresh deployment
	// gets a friendly message instead of an error.
	if _, statErr := os.Stat(cfg.Memory.Archive.Dir); errors.Is(statErr, os.ErrNotExist) {
		fmt.Printf("no archive directory found at %s\n", cfg.Memory.Archive.Dir)
		return nil
	}

	out, err := reader.Recall(context.Background(), query)
	if err != nil {
		return fmt.Errorf("recall query %q: %w", query, err)
	}
	fmt.Println(out)
	return nil
}

// runRecallRound prints a single round's archive record as pretty-printed JSON.
// The round argument must be a positive integer. A missing round file yields a
// friendly "not found" message rather than a raw error.
func runRecallRound(arg string) error {
	n, err := strconv.Atoi(arg)
	if err != nil {
		return fmt.Errorf("invalid round number %q: must be a positive integer", arg)
	}
	if n <= 0 {
		return fmt.Errorf("invalid round number %d: must be positive", n)
	}

	cfg, err := loadRecallConfig()
	if err != nil {
		return err
	}
	if !cfg.Memory.Archive.IsEnabled() {
		return errors.New("archive is disabled in config (set memory.archive.enabled: true or omit it)")
	}

	reader, err := archive.NewFileArchiveReader(cfg.Memory.Archive.Dir)
	if err != nil {
		return fmt.Errorf("create archive reader: %w", err)
	}

	rec, err := reader.Read(context.Background(), n)
	if err != nil {
		if errors.Is(err, archive.ErrRoundNotFound) {
			fmt.Printf("round %d not found in archive at %s\n", n, cfg.Memory.Archive.Dir)
			return nil
		}
		return fmt.Errorf("read round %d: %w", n, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal round %d: %w", n, err)
	}
	fmt.Println(string(data))
	return nil
}
