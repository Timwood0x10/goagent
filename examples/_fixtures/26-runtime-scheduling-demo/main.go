// Runtime scheduling demo — submit a real task, let the runtime schedule it,
// and watch a capability agent analyse the module (H1: RegisterAgent + Submit).
// This is the "Agent as Thread" showcase: the runtime owns dispatch, agents
// are disposable execution threads.
//
// ARES is a Peer Agent operating system (aresos-plan.md §1.1): all agents are
// equal peers — no Leader/Worker hierarchy. The legacy Leader/Sub team path
// (NewTeam/team.Run) has been removed. This example demonstrates the current
// peer flow.
//
// Purpose:
//
//	Show the full loop for a code-module analysis task: register a
//	capability agent, submit the task, and let the runtime dispatch it to
//	the matching agent, which reads the module files (via a sandboxed
//	read_file / list_files tool) and produces the summary.
//
// Learning objectives:
//   - How sdk.LoadConfigFile + ToOptions wire the LLM/runtime from ares.yaml.
//   - How rt.ToolRegistry().Register adds a sandboxed read_file tool the
//     agent can actually call (the runtime dispatches the tool call).
//   - How rt.RegisterAgent registers peer capabilities and rt.Submit
//     dispatches a task to the matching agent.
//   - How the Result exposes Output and Duration so you can observe the
//     execution end to end.
//
// Core APIs used (with package paths):
//   - sdk.LoadConfigFile             — github.com/Timwood0x10/ares/sdk
//   - (*cfg.ConfigFile).ToOptions()  — github.com/Timwood0x10/ares/sdk
//   - sdk.NewRuntime                 — github.com/Timwood0x10/ares/sdk
//   - (*Runtime).ToolRegistry()      — github.com/Timwood0x10/ares/sdk
//   - api/tools.ToolFunc             — github.com/Timwood0x10/ares/api/tools
//   - rt.NewAgent / sdk.WithInstruction — github.com/Timwood0x10/ares/sdk
//   - rt.RegisterAgent / rt.Submit   — github.com/Timwood0x10/ares/sdk
//   - sdk.Task / sdk.Result          — github.com/Timwood0x10/ares/sdk
//
// Run (from the repo root):
//
//	go run examples/26-runtime-scheduling-demo/main.go
//
// Config: the demo reads ./ares.yaml (the root config with your real LLM
// endpoints). A version-safe template lives at
// examples/25-dual-endpoint-fallback/ares.yaml.
//
// Expected output:
//
//	📋 Task: Summarise the taskfabric module: its files, responsibilities and
//	        how the scheduler picks a capable agent for a task.
//	📝 Result: <the module summary>
//	   took: <duration>
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and wire everything ──
	// LoadConfigFile reads the YAML config; ToOptions converts it to Runtime
	// options that auto-wire LLM, memory, distillation, AKG, and evolution.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		return
	}
	opts, err := cfg.ToOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ config: %v\n", err)
		return
	}
	rt := sdk.NewRuntime(opts...)
	defer rt.Close()

	// ── Step 2: Build the sandboxed file tools ──
	// read_file / list_files let the agents actually inspect the module under
	// analysis. The paths are sandboxed to the repo root so the agents can
	// never read outside the project (defense in depth, not trust).
	//
	// NOTE: tools must ALSO be bound to each agent via sdk.WithTools. The
	// runtime ToolRegistry alone does NOT expose them to the LLM — the agent's
	// LLM tool definitions come from its own `WithTools` list
	// (sdk.resolveTools: a.toCoreTools(a.tools)); the registry is only the
	// executor the runtime uses when the agent calls a tool.
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ getwd: %v\n", err)
		return
	}
	fileTools := buildFileTools(repoRoot)

	// Bind tools BOTH ways: sdk.WithTools exposes the tool definitions to the
	// LLM (resolveTools: toCoreTools(a.tools)), and ToolRegistry.Register wires
	// the executor the runtime uses when the agent actually calls a tool.
	// Missing either side fails: without WithTools the LLM never calls the
	// tool (0 tools); without Register the call errors with "tool not found".
	for _, t := range fileTools {
		if err := rt.ToolRegistry().Register(t); err != nil {
			fmt.Fprintf(os.Stderr, "❌ register %s: %v\n", t.Name(), err)
			return
		}
	}

	// ── Step 3: Register the capability agent (H1) ──
	// RegisterAgent creates the agent and registers it as the handler for its
	// capability; WithInstruction/WithTools configure it. The agent plans,
	// inspects the module with the file tools, and produces the summary.
	task := "Summarise the internal/fabric/task module: enumerate its files, " +
		"explain each one's responsibility, and describe how the scheduler " +
		"picks a capable agent for a task."

	// ── 阶段记录 (Phase log) ──
	// 每一步都打印，完整还原"任务提交 → runtime 调度 → agent 执行（含工具
	// 调用）→ 完成"的全过程。
	logPhase("1/3 提交任务 (submit)", task)
	fmt.Printf("📋 Task: %s\n\n", task)

	rt.RegisterAgent("code_reader",
		sdk.WithInstruction(`You are a code analyst. Use list_files to enumerate the
module's files and read_file to inspect each one. Summarise the responsibilities
you find: what each file does and how the pieces fit together. Be factual and
reference file paths.`),
		sdk.WithTools(fileTools...),
	)
	start := time.Now()
	result, err := rt.Submit(ctx, sdk.Task{
		Capability: "code_reader",
		Input:      task,
	})
	elapsed := time.Since(start)
	if err != nil {
		if strings.Contains(err.Error(), "API key") {
			fmt.Fprintf(os.Stderr, "❌ %v\n   → Set your LLM key in ./ares.yaml (see examples/25-dual-endpoint-fallback)\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "❌ submit: %v\n", err)
		return
	}

	// ── Step 5: Print the trace — dispatch, execution, result ──
	// runtime 调度（Phase 2）：Runtime 按 capability 匹配注册的 agent。
	logPhase("2/3 runtime 调度 (dispatch → code_reader)", "")
	logPhase("3/3 agent 执行 (execute)", "")
	fmt.Printf("✅ Result:\n%s\n\n", result.Output)
	fmt.Printf("   tool_calls: %d | took: %v (runtime elapsed: %v)\n",
		result.ToolCalls, result.Duration.Round(time.Millisecond), elapsed.Round(time.Millisecond))
}

// logPhase prints a phase banner so the demo's stdout is a complete, greppable
// record of the whole task lifecycle (提交→规划→拆分→执行→汇总).
func logPhase(title, detail string) {
	if detail != "" {
		fmt.Printf("── %s: %s ──\n", title, detail)
		return
	}
	fmt.Printf("── %s ──\n", title)
}

// buildFileTools builds the sandboxed list_files / read_file tools. The tools
// are returned so callers bind them to agents via sdk.WithTools — binding is
// what makes the LLM aware of them (see Step 2 note in main).
//
// Args:
//   - repoRoot: the absolute repo root; tool paths are confined to it.
//
// Returns:
//   - []tools.Tool: list_files + read_file, ready for sdk.WithTools.
func buildFileTools(repoRoot string) []tools.Tool {
	// safeJoin confines a user-supplied relative path to the repo root.
	safeJoin := func(rel string) (string, error) {
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) {
			return "", fmt.Errorf("absolute paths are not allowed")
		}
		full := filepath.Join(repoRoot, clean)
		if !strings.HasPrefix(full, repoRoot) {
			return "", fmt.Errorf("path escapes the repo root")
		}
		return full, nil
	}

	listTool := tools.ToolFunc{
		ToolName: "list_files",
		ToolDesc: "List the files and directories under a repo-relative path (e.g. \"internal/fabric/task\"). Returns file names with sizes.",
		ToolParams: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "repo-relative directory path"},
			},
			"required": []string{"path"},
		},
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			rel, _ := params["path"].(string)
			full, err := safeJoin(rel)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(full)
			if err != nil {
				return nil, err
			}
			var b strings.Builder
			for _, e := range entries {
				info, ierr := e.Info()
				size := int64(0)
				if ierr == nil {
					size = info.Size()
				}
				if e.IsDir() {
					fmt.Fprintf(&b, "%s/\n", e.Name())
				} else {
					fmt.Fprintf(&b, "%s (%d B)\n", e.Name(), size)
				}
			}
			return b.String(), nil
		},
	}

	readTool := tools.ToolFunc{
		ToolName: "read_file",
		ToolDesc: "Read a repo-relative file and return its contents (truncated to 1500 chars so long files never blow up the prompt budget).",
		ToolParams: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "repo-relative file path"},
			},
			"required": []string{"path"},
		},
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			rel, _ := params["path"].(string)
			full, err := safeJoin(rel)
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			const max = 1500
			if len(data) > max {
				return fmt.Sprintf("%s\n… [truncated %d more chars — read the next section via a targeted grep if needed]\n", string(data[:max]), len(data)-max), nil
			}
			return string(data), nil
		},
	}

	return []tools.Tool{listTool, readTool}
}
