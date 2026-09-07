package sdk

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/agentsyscall"
)

// TestSDKSyscallsRegistered is the acceptance (BindTools was
// registered only in peer mode, so an SDK user's agent never saw
// spawn_agent/create_task): after the shared scheduler is started (first
// Submit path), the runtime's tool registry carries both kernel syscalls and
// the Agent Fabric exists to back spawns.
func TestSDKSyscallsRegistered(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	// wireSyscalls runs inside ensureScheduler (schedOnce), exactly the point
	// where Submit enters the merged dispatch path.
	rt.ensureScheduler()

	if rt.agentsFabric == nil {
		t.Fatal("D1: agentsFabric must be created for spawn_agent")
	}
	if len(rt.syscallTools) == 0 {
		t.Fatal("D1: syscallTools must be populated")
	}
	for _, name := range []string{agentsyscall.SpawnAgentTool, agentsyscall.CreateTaskTool} {
		if _, ok := rt.toolReg.Get(name); !ok {
			t.Fatalf("D1: SDK tool registry must expose %q, got missing", name)
		}
	}
}

// TestSDKAgentSeesSyscallTools verifies the LLM-facing side of the syscall
// wiring: an SDK
// agent's tool list (resolveTools, discovery OFF — the default) includes
// spawn_agent/create_task even when the agent registered no tools of its own,
// so it can autonomously decompose a task.
func TestSDKAgentSeesSyscallTools(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.ensureScheduler()

	agent := rt.NewAgent("coder") // no WithTools — syscalls must still appear
	llmTools, _, _ := agent.resolveTools(context.Background(), "decompose")

	names := make(map[string]bool, len(llmTools))
	for _, tl := range llmTools {
		names[tl.Function.Name] = true
	}
	for _, name := range []string{agentsyscall.SpawnAgentTool, agentsyscall.CreateTaskTool} {
		if !names[name] {
			t.Fatalf("D1: agent tool list must include %q, got %v", name, names)
		}
	}
}
