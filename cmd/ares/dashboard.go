// ares dashboard — open the runtime introspection panel.
//
// The panel (internal/introspect) is served by a running `ares serve` at
// /introspect. This command resolves that address (--addr, else the config's
// server address, else http://localhost:8080), verifies the panel is
// reachable, and opens it in the default browser. With --url it only prints
// the address (useful for headless / remote hosts), and with --wait it polls
// until the panel comes up (handy right after launching serve).
//
// Merged CLI source: dashboard.go, status.go, flight.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
)

var (
	dashboardAddr       string // --addr: panel address (overrides config)
	dashboardConfigPath string // --config: config file to resolve the address from
	dashboardURLOnly    bool   // --url: print the URL, do not open a browser
	dashboardWait       bool   // --wait: poll until the panel is reachable
)

func init() {
	dashboardCmd := &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"panel", "introspect", "ui"},
		Short:   "Open the runtime introspection panel in a browser",
		Long: "Open the ARES runtime introspection panel served by `ares serve`.\n\n" +
			"The panel shows the live kernel scheduler, task-fabric leases/quanta,\n" +
			"agent lifecycle, and an activity feed (who died, who took work,\n" +
			"recoveries). It is read-only.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDashboard(cmd.Context())
		},
	}
	dashboardCmd.Flags().StringVar(&dashboardAddr, "addr", "", "Panel base address (default: config server or http://localhost:8080)")
	dashboardCmd.Flags().StringVarP(&dashboardConfigPath, "config", "c", "", "Config file to resolve the address from (default: auto-detect)")
	dashboardCmd.Flags().BoolVar(&dashboardURLOnly, "url", false, "Print the panel URL instead of opening a browser")
	dashboardCmd.Flags().BoolVar(&dashboardWait, "wait", false, "Poll until the panel is reachable before opening")
	rootCmd.AddCommand(dashboardCmd)
}

func runDashboard(ctx context.Context) error {
	// Resolve the base address: --addr wins, else the config's server address,
	// else the default fallback (reuses the status command's resolver so the
	// two agree on where serve listens).
	base := dashboardAddr
	if base == "" {
		cfg, _ := inspectStatusConfig(dashboardConfigPath)
		base = statusConfigServerAddr(cfg)
	}
	base = strings.TrimRight(base, "/")
	panelURL := base + "/introspect"

	if dashboardURLOnly {
		fmt.Println(panelURL)
		return nil
	}

	// Verify (optionally wait for) the panel before opening a browser, so the
	// user gets a clear "not running" message instead of a blank tab.
	if err := waitForPanel(ctx, base, dashboardWait); err != nil {
		return fmt.Errorf("dashboard: %w (start it with 'ares serve')", err)
	}

	if err := openBrowser(panelURL); err != nil {
		// Best-effort: on headless hosts there is no browser. Fall back to
		// printing the URL rather than failing the command.
		fmt.Printf("open the panel manually: %s\n(%v)\n", panelURL, err)
		return nil
	}
	fmt.Printf("opening runtime introspection panel: %s\n", panelURL)
	return nil
}

// waitForPanel probes base/api/v1/introspect/snapshot. When wait is false it
// probes once; when true it polls (2s interval) up to ~30s. A 200 or 503
// (collector warming up) both count as "panel is up".
func waitForPanel(ctx context.Context, base string, wait bool) error {
	probe := func() error {
		reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/api/v1/introspect/snapshot", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusServiceUnavailable {
			return nil
		}
		return fmt.Errorf("panel returned HTTP %d", resp.StatusCode)
	}

	if !wait {
		if err := probe(); err != nil {
			return fmt.Errorf("panel not reachable at %s: %w", base, err)
		}
		return nil
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := probe(); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("panel not reachable at %s after 30s", base)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// openBrowser opens url in the platform default browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default: // linux, *bsd
		cmd = "xdg-open"
		args = []string{url}
	}
	return exec.Command(cmd, args...).Start() // #nosec G204 — args are the fixed opener + our own URL
}

// ares status — one command to see the whole runtime at a glance.
//
// It reports three layers:
//
//  1. Runtime: whether `ares serve` is up, its system health and the live
//     agent fleet (probed via the dashboard HTTP API).
//  2. Config: where the effective configuration comes from and what it
//     resolves to (LLM endpoint, kernel policy, memory, agent team, storage).
//     A missing config file is valid — it means the minimal-assembly path
//     (NewMinimalConfig) with runtime defaults.
//  3. Capabilities: the Capability Fabric assets — indexed skills (project /
//     user / registered sources) and the accumulated experience store.
//
// Exit code: 0 when everything is healthy, 1 when a warning was reported
// (e.g. memory disabled, which `ares serve` would reject, or a runtime
// probe failure). Use --json for machine-readable output.

// statusProbeTimeout bounds each live-runtime HTTP probe so `ares status`
// never hangs on a dead or unreachable dashboard.
const statusProbeTimeout = 2 * time.Second

// statusHealthPath / statusAgentsPath are the dashboard endpoints probed for
// the live-runtime section. Both are read-only and need no API key.
const (
	statusHealthPath = "/api/health"
	statusAgentsPath = "/api/agents"
)

// statusConfigCandidates are the auto-detected config locations, in order of
// preference, mirroring `ares serve`'s discovery (config.go Load + serve.go
// loadServeConfig).
var statusConfigCandidates = []string{
	"ares.yaml",
	"config/ares.yaml",
}

var (
	statusAddr       string // --addr: dashboard address to probe (overrides config)
	statusConfigPath string // --config: config file to inspect (overrides detection)
	statusJSON       bool   // --json: emit machine-readable report
)

func init() {
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show ARES runtime status at a glance",
		Long: `Shows the ARES runtime status: whether ares serve is up (system
health + live agent fleet), what configuration is in effect (LLM endpoint,
kernel policy, memory, agent team, storage) and the Capability Fabric assets
(indexed skills + accumulated experience).

Exit code is 0 when healthy, 1 when warnings were reported.`,
		RunE: runStatus,
	}
	statusCmd.Flags().StringVar(&statusAddr, "addr", "", "Dashboard address to probe (default: config server or http://localhost:8080)")
	statusCmd.Flags().StringVarP(&statusConfigPath, "config", "c", "", "Config file to inspect (default: auto-detect ares.yaml / config/ares.yaml)")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output report as JSON")
	rootCmd.AddCommand(statusCmd)
}

// statusReport is the complete machine-readable report.
type statusReport struct {
	Version      string             `json:"version"`
	GoVersion    string             `json:"go_version"`
	Runtime      statusRuntime      `json:"runtime"`
	Config       statusConfig       `json:"config"`
	Capabilities statusCapabilities `json:"capabilities"`
	Warnings     []string           `json:"warnings"`
}

// statusRuntime reports the live `ares serve` state.
type statusRuntime struct {
	Running bool          `json:"running"`
	Addr    string        `json:"addr,omitempty"`
	Health  *statusHealth `json:"health,omitempty"`
	Agents  []statusAgent `json:"agents,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// statusHealth mirrors the dashboard /api/health payload.
type statusHealth struct {
	Level     string `json:"level"`
	Anomalies int    `json:"anomalies"`
}

// statusAgent mirrors the dashboard /api/agents entries.
type statusAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
	TaskID string `json:"task_id,omitempty"`
}

// statusConfig summarizes the effective configuration.
type statusConfig struct {
	Source  string            `json:"source"`
	Minimal bool              `json:"minimal"`
	Server  statusServer      `json:"server"`
	LLM     statusLLM         `json:"llm"`
	Kernel  statusKernel      `json:"kernel"`
	Memory  statusMemory      `json:"memory"`
	Agents  statusAgentConfig `json:"agents"`
	Storage statusStorage     `json:"storage"`
}

type statusServer struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type statusLLM struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BaseURL   string `json:"base_url"`
	APIKeySet bool   `json:"api_key_set"`
}

type statusKernel struct {
	Policy string `json:"policy"`
}

type statusMemory struct {
	Enabled bool `json:"enabled"`
}

type statusAgentConfig struct {
	Sub []string `json:"sub"`
}

type statusStorage struct {
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"`
}

// statusCapabilities summarizes the Capability Fabric assets.
type statusCapabilities struct {
	Skills     statusSkills `json:"skills"`
	Experience statusExp    `json:"experience"`
}

type statusSkills struct {
	Project    int `json:"project"`
	User       int `json:"user"`
	Registered int `json:"registered"`
	Git        int `json:"git"`
	HTTP       int `json:"http"`
}

type statusExp struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
}

func runStatus(_ *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	report := statusReport{
		Version:   buildVersion(),
		GoVersion: runtime.Version(),
	}

	// Config layer: auto-detect unless --config given; a missing file means
	// the minimal-assembly path (runtime defaults), which is valid.
	var cfgWarnings []string
	report.Config, cfgWarnings = inspectStatusConfig(statusConfigPath)
	report.Warnings = append(report.Warnings, cfgWarnings...)
	report.Warnings = append(report.Warnings, statusConfigWarnings(report.Config)...)

	// Runtime layer: probe the dashboard (address = --addr, else the config's
	// server address, else the default fallback).
	addr := statusAddr
	if addr == "" {
		addr = statusConfigServerAddr(report.Config)
	}
	report.Runtime = probeStatusRuntime(ctx, addr)
	if !report.Runtime.Running {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"runtime not reachable at %s — start with 'ares serve' to see live state", addr))
	}

	// Capability layer: scan the declared skill sources and experience store.
	report.Capabilities = inspectStatusCapabilities(ctx)

	if statusJSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("status: encode report: %w", err)
		}
		fmt.Println(string(data))
	} else {
		printStatusText(report)
	}

	if len(report.Warnings) > 0 {
		// Warnings are already rendered above; the non-zero exit code alone
		// signals health to scripts, without a redundant "error:" line on
		// stderr. cancel() runs first so the timeout goroutine is released.
		cancel()
		finishStatusExit(1)
	}
	return nil
}

// finishStatusExit terminates the CLI with the given exit code. It lives in
// its own function (no defers) so the exit-after-defer static check stays
// quiet and the exit semantics are explicit.
func finishStatusExit(code int) {
	os.Exit(code)
}

// buildVersion resolves the effective build version (ldflags override dev).
func buildVersion() string {
	if v := version; v != "" && v != "dev" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// inspectStatusConfig resolves the effective configuration: an explicit
// --config, an auto-detected config file, or the minimal-assembly defaults.
// The returned warnings describe config load/parse failures (the report then
// falls back to minimal defaults so `ares status` still answers).
func inspectStatusConfig(explicit string) (statusConfig, []string) {
	if explicit != "" {
		cfg, err := ares_config.Load(explicit)
		if err != nil {
			return statusConfig{Source: explicit, Minimal: true},
				[]string{fmt.Sprintf("config %s failed to load: %v — showing minimal defaults", explicit, err)}
		}
		return configToStatus(explicit, cfg, false), nil
	}
	for _, candidate := range statusConfigCandidates {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		cfg, err := ares_config.Load(candidate)
		if err != nil {
			return statusConfig{Source: candidate, Minimal: true},
				[]string{fmt.Sprintf("config %s failed to parse: %v — showing minimal defaults", candidate, err)}
		}
		return configToStatus(candidate, cfg, false), nil
	}
	// No config file: the minimal assembly path assembles everything from
	// defaults, exactly like `ares serve` with only --llm-url.
	cfg := ares_config.NewMinimalConfig("", "", "")
	return configToStatus("(minimal — no config file; runtime defaults)", cfg, true), nil
}

// statusConfigWarnings derives health warnings from the resolved config.
func statusConfigWarnings(cfg statusConfig) []string {
	var warnings []string
	if !cfg.Memory.Enabled {
		warnings = append(warnings,
			"memory.enabled=false — 'ares serve' runs without memory; agents will lack persistent cognitive state")
	}
	if p := cfg.Kernel.Policy; p != "" && p != "taskfabric" {
		warnings = append(warnings, fmt.Sprintf(
			"kernel.policy=%q is unknown — the only supported policy is \"taskfabric\"", p))
	}
	return warnings
}

// configToStatus projects a resolved ares_config.Config into the report shape.
func configToStatus(source string, cfg *ares_config.Config, minimal bool) statusConfig {
	if cfg == nil {
		cfg = ares_config.NewMinimalConfig("", "", "")
		minimal = true
	}
	// Empty policy is reported as the effective default "taskfabric"
	// (wireKernelPolicy flips to Task Fabric unless "legacy" is explicit).
	policy := cfg.Kernel.Policy
	if strings.TrimSpace(policy) == "" {
		policy = "taskfabric"
	}
	out := statusConfig{
		Source:  source,
		Minimal: minimal,
		Server:  statusServer{Host: cfg.Server.Host, Port: cfg.Server.Port},
		LLM: statusLLM{
			Provider:  cfg.LLM.Provider,
			Model:     cfg.LLM.Model,
			BaseURL:   cfg.LLM.BaseURL,
			APIKeySet: cfg.LLM.APIKey != "" || os.Getenv("LLM_API_KEY") != "",
		},
		Kernel: statusKernel{Policy: policy},
		Memory: statusMemory{Enabled: cfg.Memory.IsEnabled()},
		Agents: statusAgentConfig{
			Sub: make([]string, 0, len(cfg.Agents.Sub)),
		},
		Storage: statusStorage{
			Enabled: cfg.Storage.Enabled,
			Type:    cfg.Storage.Type,
		},
	}
	for _, s := range cfg.Agents.Sub {
		if s.ID != "" {
			out.Agents.Sub = append(out.Agents.Sub, s.ID)
		} else if s.Type != "" {
			out.Agents.Sub = append(out.Agents.Sub, s.Type)
		}
	}
	return out
}

// statusConfigServerAddr derives the dashboard probe address from the resolved
// config's server section (localhost fallback for a wildcard/empty host).
func statusConfigServerAddr(cfg statusConfig) string {
	host := cfg.Server.Host
	if host == "" || host == "0.0.0.0" {
		host = "localhost"
	}
	port := cfg.Server.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

// probeStatusRuntime queries the dashboard API for health + agent fleet.
func probeStatusRuntime(ctx context.Context, addr string) statusRuntime {
	addr = strings.TrimRight(addr, "/")
	out := statusRuntime{Addr: addr}
	client := &http.Client{Timeout: statusProbeTimeout}

	healthURL := addr + statusHealthPath
	resp, err := client.Get(healthURL)
	if err != nil {
		out.Error = fmt.Sprintf("health probe failed: %v", err)
		return out
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		out.Error = fmt.Sprintf("health probe returned status %d", resp.StatusCode)
		return out
	}
	// The dashboard /api/health payload is {"level": "...", "agents": N}
	// where "agents" is the anomaly count — decode via a map to tolerate
	// future field additions.
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		out.Error = fmt.Sprintf("health probe: decode: %v", err)
		return out
	}
	health := statusHealth{}
	if lv, ok := raw["level"].(string); ok {
		health.Level = lv
	}
	if an, ok := raw["agents"].(float64); ok {
		health.Anomalies = int(an)
	}
	out.Running = true
	out.Health = &health

	// Agents are best-effort: a dashboard without the agent route must not
	// downgrade the whole runtime section.
	agentsResp, agentsErr := client.Get(addr + statusAgentsPath)
	if agentsErr == nil {
		defer func() { _ = agentsResp.Body.Close() }()
		if agentsResp.StatusCode == http.StatusOK {
			var agents []statusAgent
			if decErr := json.NewDecoder(agentsResp.Body).Decode(&agents); decErr == nil {
				out.Agents = agents
			}
		}
	}
	return out
}

// inspectStatusCapabilities scans the declared skill sources and the
// experience store. Failures degrade to zero counts — `ares status` reports,
// it never fails the process over a missing directory.
func inspectStatusCapabilities(ctx context.Context) statusCapabilities {
	var out statusCapabilities

	home, _ := os.UserHomeDir()
	projectDir := filepath.Join(".", ".ares", "skills")
	userDir := ""
	if home != "" {
		userDir = filepath.Join(home, ".ares", "skills")
	}
	extraDirs, gitSources, httpSources, _ := ares_skills.LoadSkillSources("")

	sm := ares_skills.NewSourceManager(projectDir, userDir, extraDirs)
	sm.SetGitSources(gitSources)

	for _, src := range sm.Sources() {
		dirs, err := sm.SkillDirs(src)
		if err != nil {
			continue
		}
		switch src.Kind {
		case ares_skills.SourceProject:
			out.Skills.Project = len(dirs)
		case ares_skills.SourceUser:
			out.Skills.User = len(dirs)
		default:
			out.Skills.Registered += len(dirs)
		}
	}
	out.Skills.Git = len(gitSources)
	out.Skills.HTTP = len(httpSources)

	// Experience store: default location ~/.ares/experience.json (the same
	// path `ares serve` wires via CatalogConfig.ExperiencePath).
	if home != "" {
		expPath := filepath.Join(home, ".ares", "experience.json")
		out.Experience.Path = expPath
		if records, err := ares_skills.NewJSONExperienceStore(expPath).Load(ctx); err == nil {
			out.Experience.Records = len(records)
		}
	}
	return out
}

// printStatusText renders the report for humans.
func printStatusText(r statusReport) {
	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}

	fmt.Printf("ARES status — %s (%s, %s)\n", r.Version, runtime.GOOS, runtime.GOARCH)
	fmt.Println()

	fmt.Println("Runtime")
	rt := r.Runtime
	status := "not running"
	if rt.Running {
		status = "running"
	}
	fmt.Printf("  server      %s  [%s %s]\n", rt.Addr, status, mark(rt.Running))
	if rt.Health != nil {
		fmt.Printf("  health      level=%s anomalies=%d\n", rt.Health.Level, rt.Health.Anomalies)
	}
	if len(rt.Agents) > 0 {
		fmt.Printf("  agents      %d total:\n", len(rt.Agents))
		for _, a := range rt.Agents {
			role := a.Role
			if role == "" {
				role = a.Status
			}
			if role == "" {
				role = "unknown"
			}
			fmt.Printf("    - %-12s %s\n", a.ID, role)
		}
	} else if rt.Error != "" {
		fmt.Printf("  probe       %s\n", rt.Error)
	}
	fmt.Println()

	fmt.Println("Config")
	c := r.Config
	fmt.Printf("  source      %s\n", c.Source)
	if c.Minimal {
		fmt.Println("              (minimal assembly — runtime defaults for all subsystems)")
	}
	fmt.Printf("  llm         %s / %s\n", c.LLM.Provider, c.LLM.Model)
	if c.LLM.BaseURL != "" {
		fmt.Printf("              base: %s\n", c.LLM.BaseURL)
	}
	keyState := "unset"
	if c.LLM.APIKeySet {
		keyState = "set"
	}
	fmt.Printf("              api key: %s\n", keyState)
	fmt.Printf("  kernel      %s\n", kernelPolicyLabel(c.Kernel.Policy))
	fmt.Printf("  memory      %s\n", boolLabel(c.Memory.Enabled))
	fmt.Printf("  agents      %d peer(s)", len(c.Agents.Sub))
	if len(c.Agents.Sub) > 0 {
		fmt.Printf(": %s", strings.Join(c.Agents.Sub, ", "))
	}
	fmt.Println()
	fmt.Printf("  storage     %s", boolLabel(c.Storage.Enabled))
	if c.Storage.Enabled {
		fmt.Printf(" (%s)", c.Storage.Type)
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("Capabilities")
	cap := r.Capabilities
	fmt.Printf("  skills      %d project, %d user, %d registered, %d git, %d http\n",
		cap.Skills.Project, cap.Skills.User, cap.Skills.Registered, cap.Skills.Git, cap.Skills.HTTP)
	if cap.Experience.Path != "" {
		fmt.Printf("  experience  %d records (%s)\n", cap.Experience.Records, cap.Experience.Path)
	}
	fmt.Println()

	if len(r.Warnings) > 0 {
		fmt.Println("Warnings")
		for _, w := range r.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
		fmt.Println()
		fmt.Println("✗ status has warnings — see above (exit code 1)")
		return
	}
	fmt.Println("✓ all systems nominal")
}

// kernelPolicyLabel renders the kernel policy with its execution mode.
func kernelPolicyLabel(policy string) string {
	switch policy {
	case "taskfabric":
		return "taskfabric (Task Fabric scheduler active)"
	case "":
		return "taskfabric (default, Task Fabric scheduler active)"
	case "legacy":
		return "legacy (explicit, Task Fabric in shadow)"
	default:
		return policy
	}
}

// boolLabel renders a boolean as on/off.
func boolLabel(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

var flightCmd = &cobra.Command{
	Use:   "flight",
	Short: "Agent Flight Recorder commands",
	Long: `Inspect and replay agent flight data from recorded events.
Supports text, mermaid, dot, and JSON output formats.`,
}

var (
	flightInspectFormat string
	flightInspectInput  string
)

var flightInspectCmd = &cobra.Command{
	Use:   "inspect <taskID>",
	Short: "Show flight data for a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		evts, err := loadFlightEvents(flightInspectInput)
		if err != nil {
			return fmt.Errorf("load events: %w", err)
		}
		if len(evts) == 0 {
			return errors.New("no events found")
		}

		var taskEvts []*ares_events.Event
		for _, e := range evts {
			if e.StreamID == taskID {
				taskEvts = append(taskEvts, e)
			}
		}
		if len(taskEvts) == 0 {
			return fmt.Errorf("no events found for task %s", taskID)
		}

		switch flightInspectFormat {
		case "text":
			return inspectText(taskID, taskEvts)
		case "mermaid":
			return inspectMermaid(taskEvts)
		case "dot":
			return inspectDOT(taskEvts)
		case "json":
			return inspectJSON(taskEvts)
		default:
			return fmt.Errorf("unknown format: %s (supported: text, mermaid, dot, json)", flightInspectFormat)
		}
	},
}

var (
	flightReplayStep  int
	flightReplayInput string
)

var flightReplayCmd = &cobra.Command{
	Use:   "replay <taskID>",
	Short: "Step-by-step replay of a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		evts, err := loadFlightEvents(flightReplayInput)
		if err != nil {
			return fmt.Errorf("load events: %w", err)
		}

		store := ares_events.NewMemoryEventStore()
		defer func() { _ = store.Close() }()

		ctx := context.Background()

		streamEvents := make(map[string][]*ares_events.Event)
		for _, e := range evts {
			streamEvents[e.StreamID] = append(streamEvents[e.StreamID], e)
		}
		for streamID, sevts := range streamEvents {
			if err := store.Append(ctx, streamID, sevts, 0); err != nil {
				return fmt.Errorf("append events for stream %s: %w", streamID, err)
			}
		}

		session, err := flight.NewReplaySession(ctx, store, taskID)
		if err != nil {
			return fmt.Errorf("create replay session: %w", err)
		}

		summary := session.Summary()
		fmt.Printf("Task: %s\n", summary.TaskID)
		fmt.Printf("Total steps: %d\n", summary.TotalSteps)
		fmt.Printf("Duration: %s\n", summary.Duration)
		fmt.Printf("Agents: %s\n", strings.Join(summary.Agents, ", "))
		fmt.Println("---")

		if flightReplayStep >= 0 {
			rs, err := session.StepTo(flightReplayStep)
			if err != nil {
				return fmt.Errorf("step to %d: %w", flightReplayStep, err)
			}
			printReplayStep(rs)
		} else {
			for {
				rs, err := session.Step()
				if err != nil {
					break
				}
				printReplayStep(rs)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(flightCmd)

	flightCmd.AddCommand(flightInspectCmd)
	flightInspectCmd.Flags().StringVarP(&flightInspectFormat, "format", "f", "text", "Output format: text, mermaid, dot, json")
	flightInspectCmd.Flags().StringVarP(&flightInspectInput, "input", "i", "", "Path to JSON events file (default: stdin)")

	flightCmd.AddCommand(flightReplayCmd)
	flightReplayCmd.Flags().IntVarP(&flightReplayStep, "step", "s", -1, "Jump to a specific step (0-indexed)")
	flightReplayCmd.Flags().StringVarP(&flightReplayInput, "input", "i", "", "Path to JSON events file (default: stdin)")
}

// ── Shared helpers ──────────────────────────────────────────

func loadFlightEvents(path string) ([]*ares_events.Event, error) {
	var reader io.Reader
	if path == "" {
		reader = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		reader = f
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("input is empty")
	}

	var evts []*ares_events.Event
	if err := json.Unmarshal(data, &evts); err != nil {
		return nil, fmt.Errorf("parse JSON events: %w", err)
	}
	return evts, nil
}

func inspectText(taskID string, evts []*ares_events.Event) error {
	tl := flight.NewTimeline()
	dl := flight.NewDecisionLog()
	de := flight.NewDiagnosticsEngine()

	for _, e := range evts {
		te := flight.TimelineEvent{
			ID:       e.ID,
			AgentID:  e.StreamID,
			Type:     mapFlightEventType(e.Type),
			Name:     eventName(e),
			StartAt:  e.Timestamp,
			Metadata: e.Payload,
		}
		tl.Add(te)

		if e.Type == "decision" {
			d := flight.Decision{
				ID:        e.ID,
				AgentID:   e.StreamID,
				Type:      flight.DecisionType(stringOr(e.Payload, "type", "unknown")),
				Selected:  stringOr(e.Payload, "selected", ""),
				Reason:    stringOr(e.Payload, "reason", ""),
				Timestamp: e.Timestamp,
				Metadata:  e.Payload,
			}
			dl.Add(d)
		}

		if e.Type == "error" {
			errMsg := stringOr(e.Payload, "error", "unknown error")
			cat := flight.ClassifyError(errMsg)
			suggestions := flight.SuggestFix(cat)
			suggestion := ""
			if len(suggestions) > 0 {
				suggestion = suggestions[0]
			}
			dr := flight.DiagnosticRecord{
				ID:         e.ID,
				AgentID:    e.StreamID,
				TaskID:     taskID,
				Category:   cat,
				RootCause:  errMsg,
				Suggestion: suggestion,
				Timestamp:  e.Timestamp,
			}
			de.Record(dr)
		}
	}

	summary := tl.Summary()
	fmt.Printf("=== Flight Inspector: %s ===\n\n", taskID)
	fmt.Printf("Timeline Summary:\n")
	fmt.Printf("  Events:        %d\n", summary.EventCount)
	fmt.Printf("  Total Duration: %s\n", formatDuration(summary.TotalDuration))
	fmt.Printf("  Tool Duration:  %s (%.1f%%)\n", formatDuration(summary.ToolDuration), summary.ToolPercent)
	fmt.Printf("  LLM Duration:   %s (%.1f%%)\n", formatDuration(summary.LLMDuration), summary.LLMPercent)
	fmt.Printf("  Wait Duration:  %s (%.1f%%)\n", formatDuration(summary.WaitDuration), summary.WaitPercent)
	fmt.Println()

	decisions := dl.All()
	if len(decisions) > 0 {
		fmt.Printf("Decisions (%d):\n", len(decisions))
		for _, d := range decisions {
			fmt.Printf("  [%s] agent=%s selected=%q reason=%q confidence=%.2f\n",
				d.Type, d.AgentID, d.Selected, d.Reason, d.Confidence)
		}
		fmt.Println()
	}

	records := de.All()
	if len(records) > 0 {
		fmt.Printf("Diagnostics (%d):\n", len(records))
		for _, r := range records {
			fmt.Printf("  [%s] agent=%s cause=%q fix=%q\n",
				r.Category, r.AgentID, r.RootCause, r.Suggestion)
		}
		fmt.Println()
	}

	fmt.Printf("Events:\n")
	for i, e := range evts {
		fmt.Printf("  %d. [%s] %s (%s) @ %s\n",
			i, e.Type, eventName(e), e.StreamID, e.Timestamp.Format(time.RFC3339))
	}

	return nil
}

func inspectMermaid(evts []*ares_events.Event) error {
	g := buildGraph(evts)
	fmt.Println(g.ExportMermaid())
	return nil
}

func inspectDOT(evts []*ares_events.Event) error {
	g := buildGraph(evts)
	fmt.Println(g.ExportDOT())
	return nil
}

func inspectJSON(evts []*ares_events.Event) error {
	data, err := json.MarshalIndent(evts, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func buildGraph(evts []*ares_events.Event) *flight.Graph {
	g := flight.NewGraph()
	hasRoot := false

	for _, e := range evts {
		nodeID := e.ID
		if nodeID == "" {
			nodeID = fmt.Sprintf("evt-%d", e.Version)
		}

		nodeType := flight.NodeAgent
		switch e.Type {
		case "tool.call", "tool.result":
			nodeType = flight.NodeTool
		case "llm.call", "llm.result":
			nodeType = flight.NodeLLM
		}

		status := flight.StatusCompleted
		if e.Type == "error" {
			status = flight.StatusFailed
		}

		parentID := stringOr(e.Payload, "parent_id", "")

		if parentID == "" && !hasRoot {
			hasRoot = true
		} else if parentID == "" {
			// Root node's ID may be synthesized ("evt-<version>"); use the same
			// synthesis for the parent reference so it never dangles.
			parentID = evts[0].ID
			if parentID == "" {
				parentID = fmt.Sprintf("evt-%d", evts[0].Version)
			}
		}

		node := &flight.GraphNode{
			ID:       nodeID,
			ParentID: parentID,
			Type:     nodeType,
			Name:     eventName(e),
			Status:   status,
			StartAt:  e.Timestamp,
			Metadata: e.Payload,
		}
		g.AddNode(node)
	}
	return g
}

func mapFlightEventType(t ares_events.EventType) flight.EventType {
	switch t {
	case "agent.started":
		return flight.EventAgentStart
	case "agent.stopped":
		return flight.EventAgentEnd
	case "tool.call":
		return flight.EventToolCall
	case "tool.result":
		return flight.EventToolResult
	case "llm.call":
		return flight.EventLLMCall
	case "llm.result":
		return flight.EventLLMResult
	case "error":
		return flight.EventError
	case "memory.distilled", "memory.op":
		return flight.EventMemoryOp
	case "decision":
		return flight.EventDecision
	default:
		return flight.EventType(t)
	}
}

func eventName(e *ares_events.Event) string {
	if name, ok := e.Payload["name"].(string); ok && name != "" {
		return name
	}
	if tool, ok := e.Payload["tool"].(string); ok && tool != "" {
		return tool
	}
	if model, ok := e.Payload["model"].(string); ok && model != "" {
		return model
	}
	return string(e.Type)
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fus", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d.Milliseconds()))
	}
	return d.Truncate(time.Millisecond).String()
}

func printReplayStep(step *flight.ReplayStep) {
	fmt.Printf("Step %d: [%s] agent=%s @ %s\n",
		step.StepNum, step.EventType, step.AgentID,
		step.Timestamp.Format(time.RFC3339))
	if len(step.Payload) > 0 {
		data, err := json.MarshalIndent(step.Payload, "  ", "  ")
		if err == nil {
			fmt.Printf("  payload: %s\n", string(data))
		}
	}
}
