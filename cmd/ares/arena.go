package main

//nolint: errcheck // ResponseWriter writes: not actionable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_runtime"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

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
		// P1-9 closure: real providers — chaos injections now operate on the
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

// arenaRuntimeProvider adapts the arena process's own ares_runtime.Manager to
// arena.RuntimeProvider. Manager implements the chaos methods natively
// (manager_chaos.go); the explicit delegation keeps the adapter decoupled
// from interface drift on either side.
type arenaRuntimeProvider struct{ mgr *ares_runtime.Manager }

func (p *arenaRuntimeProvider) StopAgent(ctx context.Context, agentID string) error {
	return p.mgr.StopAgent(ctx, agentID)
}

func (p *arenaRuntimeProvider) ListAgents() []ares_runtime.AgentInfo {
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
// process's own runtime and DAG (P1-9 closure, demo positioning): the Manager
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
//   - *ares_runtime.Manager: the demo agent pool (stopped by the caller).
func buildArenaInjector() (*arena.Injector, *ares_runtime.Manager) {
	mgr := ares_runtime.New(nil, nil, nil)
	for _, id := range []string{"arena-worker-1", "arena-worker-2", "arena-worker-3"} {
		mgr.RegisterAgent(newArenaDemoAgent(id, "coder"), nil)
	}
	// A pool that failed to start has no live agents: hand the injector a nil
	// RuntimeProvider so kill/pause/slow return ErrRuntimeNil rather than
	// appearing to act on agents that are not running.
	var rt arena.RuntimeProvider
	if err := mgr.Start(context.Background()); err != nil {
		log.Printf("arena serve: demo agent pool start failed (%v); agent injections disabled", err)
	} else {
		rt = &arenaRuntimeProvider{mgr: mgr}
	}
	dag, err := engine.NewMutableDAG(nil)
	if err != nil {
		log.Printf("arena serve: mutable DAG unavailable (%v); DAG injections disabled", err)
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
	// K3 exception: one-shot short task (feed one result, close), not a
	// long-lived loop — adoption would outlive the per-request stream it
	// serves. Carries its own recover boundary so a panic cannot kill the
	// arena process; the channel closes either way.
	go func() {
		defer func() {
			close(ch)
			if r := recover(); r != nil {
				log.Printf("arena: demo agent stream panicked (recovered): %v", r)
			}
		}()
		out, _ := a.Process(ctx, input)
		ch <- base.AgentEvent{Source: a.id, Data: out}
	}()
	return ch, nil
}
