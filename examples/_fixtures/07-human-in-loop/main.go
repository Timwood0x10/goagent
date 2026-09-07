// Human-in-loop — demonstrates human approval before executing tool calls.
//
// Purpose:
//
//	Show how to intercept every tool call with a human-approval callback so
//	that read operations proceed automatically while destructive operations
//	(delete, payment) are rejected. The agent receives the approval decision
//	and adapts its subsequent behaviour accordingly.
//
// Learning objectives (what this example teaches you):
//   - How to use sdk.WithHumanInput to attach a per-tool-call approval gate.
//   - How the approval function signature (toolName, args → bool, error) maps
//     to approve / reject / abort semantics.
//   - How a system prompt combined with human-in-loop approval creates a safe
//     agent that never performs destructive actions without consent.
//   - How to read Result metrics to verify the agent respected the gate.
//
// Core APIs used (package path → symbol):
//   - github.com/Timwood0x10/ares/sdk.NewRuntime              // create Runtime
//   - github.com/Timwood0x10/ares/sdk.WithOllama              // pick Ollama provider + model
//   - github.com/Timwood0x10/ares/sdk.WithTrace               // enable per-step trace logging
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).ToolRegistry // access tool registry
//   - github.com/Timwood0x10/ares/api/tools.(*Registry).Register
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).NewAgent
//   - github.com/Timwood0x10/ares/sdk.WithInstruction         // set system prompt
//   - github.com/Timwood0x10/ares/sdk.WithHumanInput          // attach approval callback
//   - github.com/Timwood0x10/ares/sdk.HumanInputFunc          // func type for the callback
//   - github.com/Timwood0x10/ares/sdk.(*Agent).Run            // run a single task
//   - github.com/Timwood0x10/ares/sdk.Result                  // Output, ToolCalls, TokenUsage…
//   - github.com/Timwood0x10/ares/api/tools.ToolFunc          // struct-based tool implementation
//
// Run:
//
//	go run examples/07-human-in-loop/main.go
//
// Expected output:
//
//	"---" + "📋 Task: …"   → the natural-language task given to the agent
//	"  👤 Approve reading …? [y/n] (auto: y)" → human-gate decision for read_file
//	"  👤 Approve DELETING …? [y/n] (auto: n)" → human-gate decision for delete_file
//	"  👤 Approve payment …? [y/n] (auto: n)"  → human-gate decision for send_payment
//	"🤖 <agent output>"
//	"   tools: N | tokens: N | took: …"
//	"✅ Human-in-loop demo completed"
//
// Things you can try to modify:
//   - Change the approver function to prompt for real keyboard input (bufio.Reader)
//     instead of auto-approving / auto-rejecting.
//   - Add a "retry after rejection" path by returning an error from the approver
//     to see how the agent aborts.
//   - Swap the tool set (e.g. add an exec_command tool) to test approval on new
//     destructive operations.
//   - Change sdk.WithOllama("llama3.2") to a different model to compare how well
//     each model respects the human-gate decision.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Create a Runtime with Ollama and trace logging ──
	// NewRuntime initialises the top-level container. WithOllama selects the
	// Ollama provider with model "llama3.2" (no API key needed). WithTrace(true)
	// turns on per-step trace logging so you can follow the agent's reasoning.
	rt := sdk.NewRuntime(
		sdk.WithOllama("llama3.2"),
		sdk.WithTrace(true),
	)
	defer rt.Close()

	// ── Step 2: Register all tools on the Runtime's tool registry ──
	// allTools contains list_dir, read_file, delete_file, and send_payment.
	// Registering them makes the tools discoverable by the agent so it can
	// choose which to call based on the task.
	for _, t := range allTools {
		if err := rt.ToolRegistry().Register(t); err != nil {
			fmt.Fprintf(os.Stderr, "❌ register %s: %v\n", t.Name(), err)
			return
		}
	}

	// ── Step 3: Define the human approver callback ──
	// The approver is a sdk.HumanInputFunc called before every tool invocation.
	// Returning true approves the call; false skips it; an error aborts the run.
	// Here we auto-approve read operations, auto-reject destructive ones
	// (delete, payment), and auto-approve any unknown tool as a safe default.
	approver := func(_ context.Context, name string, args map[string]any) (bool, error) {
		switch name {
		case "read_file":
			filename, _ := args["filename"].(string)
			fmt.Printf("  👤 Approve reading %q? [y/n] (auto: y): ", filename)
			return true, nil // auto-approve reads

		case "delete_file":
			filename, _ := args["filename"].(string)
			fmt.Printf("  👤 Approve DELETING %q? [y/n] (auto: n): ", filename)
			return false, nil // auto-reject deletes

		case "send_payment":
			amount, _ := args["amount"].(string)
			to, _ := args["to"].(string)
			fmt.Printf("  👤 Approve payment of %s to %s? [y/n] (auto: n): ", amount, to)
			return false, nil // auto-reject payments

		default:
			return true, nil // auto-approve unknown tools
		}
	}

	// ── Step 4: Create the Agent with instruction and human input ──
	// WithInstruction sets the system prompt that tells the agent to read
	// before deleting and to never delete without explicit permission.
	// WithHumanInput attaches the approver callback so every tool call is
	// gated by human approval.
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction(`You are a helpful assistant with access to files and payments.
Always read files before deleting them. Never delete without explicit user permission.`),
		sdk.WithHumanInput(approver),
	)

	// ── Step 5: Run each task and print results ──
	// For every task we call agent.Run, which streams the task through the LLM,
	// invokes the approver before each tool call, and returns a *sdk.Result.
	// API-key / refusal errors are fatal; other errors are printed and skipped.
	for _, task := range tasks {
		fmt.Printf("\n---\n📋 Task: %s\n", task)
		result, err := agent.Run(ctx, task)
		if err != nil {
			if strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), "refused") {
				fmt.Fprintf(os.Stderr, "❌ %v\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			continue
		}
		fmt.Printf("🤖 %s\n", result.Output)
		fmt.Printf("   tools: %d | tokens: %d | took: %v\n",
			result.ToolCalls, result.TokenUsage.Total, result.Duration)
	}

	fmt.Println("\n✅ Human-in-loop demo completed")
}

// tasks is the list of natural-language tasks the agent will process.
var tasks = []string{
	"What files are in the current directory? Use list_dir to check, then read any .go file you find.",
}

// allTools is the full set of tools registered on the Runtime for this demo.
var allTools = []tools.Tool{
	listDirTool,
	readFileTool,
	deleteFileTool,
	sendPaymentTool,
}

// listDirTool lists the files in a directory (defaults to "." when no path is
// given).
var listDirTool = tools.ToolFunc{
	ToolName: "list_dir",
	ToolDesc: "List files in a directory",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		dir, _ := params["path"].(string)
		if dir == "" {
			dir = "."
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("list dir: %w", err)
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, "\n"), nil
	},
}

// readFileTool reads a text file, resolving the path safely relative to the
// working directory.
var readFileTool = tools.ToolFunc{
	ToolName: "read_file",
	ToolDesc: "Read a text file",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		path, _ := params["filename"].(string)
		safePath, err := safeFilePath(path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(safePath)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return string(data), nil
	},
}

// deleteFileTool permanently deletes a file after resolving its path safely.
var deleteFileTool = tools.ToolFunc{
	ToolName: "delete_file",
	ToolDesc: "Delete a file permanently",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		path, _ := params["filename"].(string)
		safePath, err := safeFilePath(path)
		if err != nil {
			return nil, err
		}
		if err := os.Remove(safePath); err != nil {
			return nil, fmt.Errorf("delete: %w", err)
		}
		return fmt.Sprintf("deleted %s", path), nil
	},
}

// safeFilePath resolves path relative to the working directory and rejects
// paths that escape it (path traversal protection).
func safeFilePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("filename is required")
	}
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("absolute paths are not allowed: %s", path)
	}
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	rel, err := filepath.Rel(wd, absPath)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %s is outside the working directory", path)
	}
	return absPath, nil
}

// sendPaymentTool simulates sending a payment to a user.
var sendPaymentTool = tools.ToolFunc{
	ToolName: "send_payment",
	ToolDesc: "Send a payment to a user",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		to, _ := params["to"].(string)
		amount, _ := params["amount"].(string)
		return fmt.Sprintf("sent %s to %s", amount, to), nil
	},
}
