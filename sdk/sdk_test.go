package sdk

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/api/tools"
	ares_events "github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/detector"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	aresexp "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
)

func TestNew(t *testing.T) {
	rt, err := New(WithOllama("llama3.2"), WithTrace(false))
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if rt == nil {
		t.Fatal("New() returned nil Runtime")
	}
	rt.Close()
}

func TestNewWithAllFeatures(t *testing.T) {
	rt, err := New(
		WithOllama("llama3.2"),
		WithDefaultMemory(),
		WithEvolution(),
		WithAPIKey("test-key"),
		WithBaseURL("http://localhost:11434"),
		WithLLMConfig(&core.LLMConfig{Provider: "ollama", Model: "llama3.2"}),
		WithTrace(false),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer rt.Close()
}

func TestNewWithProviders(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{"openai", WithOpenAI(defaultOpenAIModel)},
		{"anthropic", WithAnthropic("claude-3-haiku")},
		{"openrouter", WithOpenRouter("openai/gpt-4o")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, err := New(tt.opt, WithTrace(false))
			if err != nil {
				t.Fatalf("New(%s) error: %v", tt.name, err)
			}
			rt.Close()
		})
	}
}

func TestNewError(t *testing.T) {
	_, err := New(func(c *config) error {
		c.llmCfg.Provider = "openai"
		c.llmCfg.Model = ""
		c.llmCfg.APIKey = ""
		return nil
	}, WithTrace(false))
	if err == nil {
		t.Fatal("expected error with empty model")
	}
}

func TestMustNewPanic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	NewRuntime(WithOllama(""))
}

func TestToolRegistry(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	reg := rt.ToolRegistry()
	if reg == nil {
		t.Fatal("ToolRegistry returned nil")
	}
}

func TestNewAgent(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test",
		WithInstruction("be helpful"),
		WithTools(calcTool),
	)
	if agent.name != "test" {
		t.Fatalf("name mismatch")
	}
}

func TestAgentRunNoLLM(t *testing.T) {
	rt := NewRuntime(WithOllama("nonexistent"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test", WithInstruction("hi"))
	_, err := agent.Run(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestToolConversion(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("t", WithTools(calcTool))
	coreTools := agent.toCoreTools(agent.tools)
	if len(coreTools) != 1 || coreTools[0].Function.Name != "calculator" {
		t.Fatal("tool conversion failed")
	}
}

func TestParseArgs(t *testing.T) {
	m := parseArgs(`{"x":"1"}`)
	if m == nil || m["x"] != "1" {
		t.Fatal("parseArgs failed")
	}
	if got := parseArgs(""); got != nil {
		t.Fatal("expected nil for empty")
	}
	if got := parseArgs("bad"); got != nil {
		t.Fatal("expected nil for invalid")
	}
}

func TestBuildMessages(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test", WithInstruction("help"))
	msgs := agent.buildMessages(context.Background(), "hello", "sess")
	if len(msgs) < 2 {
		t.Fatal("expected system+user messages")
	}
}

func TestWithKnowledgeEnabled(t *testing.T) {
	rt, err := New(
		WithOllama("llama3.2"),
		WithDefaultMemory(),
		WithKnowledge(),
		WithTrace(false),
	)
	if err != nil {
		t.Fatalf("New() with knowledge error: %v", err)
	}
	defer rt.Close()

	if !rt.knowledgeEnabled {
		t.Fatal("expected knowledgeEnabled to be true")
	}
	if rt.knowledgeRT == nil {
		t.Fatal("expected knowledgeRT to be non-nil")
	}
}

func TestBuildMessagesWithKnowledge(t *testing.T) {
	rt, err := New(
		WithOllama("llama3.2"),
		WithDefaultMemory(),
		WithKnowledge(),
		WithTrace(false),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	defer rt.Close()

	agent := rt.NewAgent("test", WithInstruction("help"))
	msgs := agent.buildMessages(context.Background(), "hello", "sess")
	// Should have at least system (instruction) + user messages.
	// Knowledge context may be empty if no memory data exists, which is fine.
	if len(msgs) < 2 {
		t.Fatal("expected at least system+user messages")
	}
	_ = rt.Close
}

func TestBuildMessagesWithoutKnowledge(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test", WithInstruction("help"))
	msgs := agent.buildMessages(context.Background(), "hello", "sess")
	// Without knowledge, no AKF context should be injected.
	for _, m := range msgs {
		if m.Role == roleSystem && strings.Contains(m.Content, "Nodes") {
			t.Fatal("knowledge context should not appear without WithKnowledge()")
		}
	}
}

func TestLoadConfigFile(t *testing.T) {
	tmp := tmpFile(t, "llm:\n  provider: openai\n  model: gpt-4o\nmemory:\n  enabled: true\n")
	defer func() { _ = os.Remove(tmp) }()

	cfg, err := LoadConfigFile(tmp)
	if err != nil {
		t.Fatalf("LoadConfigFile error: %v", err)
	}
	if cfg.LLM.Provider != "openai" || cfg.LLM.Model != "gpt-4o" {
		t.Fatal("config values mismatch")
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := LoadConfigFile("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestToOptions(t *testing.T) {
	cfg := &ConfigFile{LLM: LLMFileConfig{Provider: "ollama"}}
	opts, err := cfg.ToOptions()
	if err != nil || len(opts) == 0 {
		t.Fatal("ToOptions failed")
	}
}

func TestToOptionsUnknownProvider(t *testing.T) {
	cfg := &ConfigFile{LLM: LLMFileConfig{Provider: "unknown"}}
	_, err := cfg.ToOptions()
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestEvolveNotEnabled(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test", WithInstruction("be helpful"))
	_, err := rt.Evolve(context.Background(), agent, "task")
	if err == nil {
		t.Fatal("expected error when evolution not enabled")
	}
}

func TestEvolveNilAgent(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithEvolution(), WithTrace(false))
	defer rt.Close()
	_, err := rt.Evolve(context.Background(), nil, "task")
	if err == nil {
		t.Fatal("expected error for nil agent")
	}
}

func TestWithMCPMissingCommand(t *testing.T) {
	_, err := New(WithMCP(MCPConn{Name: "test"}), WithTrace(false))
	if err == nil {
		t.Fatal("expected error for MCP without command")
	}
}

func TestStream(t *testing.T) {
	rt := NewRuntime(WithOllama("nonexistent"), WithTrace(false))
	defer rt.Close()
	agent := rt.NewAgent("test", WithInstruction("hi"))
	ch, err := agent.Stream(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range ch {
		if chunk.Err != nil {
			return // expected, no LLM available
		}
	}
}

// tmpFile creates a temp file with given content and returns its path.
func tmpFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "ares-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(content)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

var calcTool = tools.ToolFunc{
	ToolName: "calculator",
	ToolDesc: "test tool",
	Fn:       func(ctx context.Context, p map[string]any) (any, error) { return "42", nil },
}

// ---- 4a/4b integration tests: Agent.Run delegates to agentloop.Engine ----
//
// These tests inject a mock LLM (implementing the unexported llmService
// interface) by overriding Runtime.llmSvc after construction. They verify the
// end-to-end wiring through Agent.Run → agentloop.Engine without a real LLM.

// mockLLMSvc scripts Generate responses per call. It implements llmService so
// it can be assigned to Runtime.llmSvc. When responses are exhausted it returns
// a fallback final answer so the loop terminates.
type mockLLMSvc struct {
	responses []*core.GenerateResponse
	calls     int
}

func (m *mockLLMSvc) Generate(_ context.Context, _ *core.GenerateRequest) (*core.GenerateResponse, error) {
	idx := m.calls
	m.calls++
	if idx >= len(m.responses) {
		return &core.GenerateResponse{Content: "mock fallback"}, nil
	}
	return m.responses[idx], nil
}

func (m *mockLLMSvc) GetProvider() core.LLMProvider { return core.LLMProviderOllama }
func (m *mockLLMSvc) GetModel() string              { return "mock-model" }
func (m *mockLLMSvc) Close()                        {}

// Compile-time check that mockLLMSvc satisfies the unexported llmService
// interface used by Runtime.llmSvc.
var _ llmService = (*mockLLMSvc)(nil)

// mockToolCall builds a core.ToolCall for scripted LLM responses.
func mockToolCall(id, name, args string) core.ToolCall {
	return core.ToolCall{
		ID:   id,
		Type: "function",
		Function: core.FunctionCall{
			Name:      name,
			Arguments: args,
		},
	}
}

// recordingMemMgr wraps a real memory.MemoryManager and records AddMessage
// calls so tests can assert which roles/content were persisted. All other
// methods delegate to the embedded manager.
type recordingMemMgr struct {
	memory.MemoryManager
	mu    sync.Mutex
	added []memEntry
}

type memEntry struct {
	sessionID string
	role      string
	content   string
}

// AddMessage records the call then delegates to the wrapped manager.
func (r *recordingMemMgr) AddMessage(ctx context.Context, sessionID, role, content string) error {
	r.mu.Lock()
	r.added = append(r.added, memEntry{sessionID: sessionID, role: role, content: content})
	r.mu.Unlock()
	return r.MemoryManager.AddMessage(ctx, sessionID, role, content)
}

func (r *recordingMemMgr) snapshot() []memEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]memEntry, len(r.added))
	copy(out, r.added)
	return out
}

// TestAgentRun_EmitsTaskCompletedEvent (4a) verifies that a successful agent
// run emits an EventTaskCompleted event carrying the original input and the LLM
// output in its payload. TaskCompleted emission is gated on distillSvc != nil,
// so the test sets a non-nil distillSvc (no subscriber runs because it is set
// after New, so the event is only inspected, not consumed).
func TestAgentRun_EmitsTaskCompletedEvent(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*core.GenerateResponse{
		{Content: "the answer is 4", Usage: core.TokenUsage{PromptTokens: 3, CompletionTokens: 5}},
	}}
	// A non-nil distillSvc enables TaskCompleted emission (mirrors distillSvc != nil
	// in the original Agent.Run). Set after New so no distillation subscriber runs.
	rt.distillSvc = &aresexp.DistillationService{}

	agent := rt.NewAgent("task-agent", WithInstruction("help"))
	res, err := agent.Run(context.Background(), "what is 2+2")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "the answer is 4" {
		t.Fatalf("Output = %q, want %q", res.Output, "the answer is 4")
	}

	evs, rerr := rt.eventStore.ReadAll(context.Background(), ares_events.ReadOptions{})
	if rerr != nil {
		t.Fatalf("ReadAll error: %v", rerr)
	}
	var found *ares_events.Event
	for _, ev := range evs {
		if ev.Type == ares_events.EventTaskCompleted {
			found = ev
			break
		}
	}
	if found == nil {
		t.Fatal("expected EventTaskCompleted in event store")
	}
	if got := found.Payload[ares_events.EventKeyTask]; got != "what is 2+2" {
		t.Errorf("task payload = %v, want %q", got, "what is 2+2")
	}
	if got := found.Payload[ares_events.EventKeyResult]; got != "the answer is 4" {
		t.Errorf("result payload = %v, want %q", got, "the answer is 4")
	}
	if got := found.Payload["agent_id"]; got != "task-agent" {
		t.Errorf("agent_id payload = %v, want %q", got, "task-agent")
	}
	if got := found.Payload[ares_events.EventKeyTenantID]; got != ares_events.DefaultTenantID {
		t.Errorf("tenant payload = %v, want %q", got, ares_events.DefaultTenantID)
	}
}

// TestAgentRun_ToolCallEvents (4a) verifies that a tool-calling run emits
// ToolCallStarted then ToolCallCompleted events on the agent-name stream with
// strictly increasing versions.
func TestAgentRun_ToolCallEvents(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	if err := rt.ToolRegistry().Register(calcTool); err != nil {
		t.Fatal(err)
	}
	rt.llmSvc = &mockLLMSvc{responses: []*core.GenerateResponse{
		{Content: "", ToolCalls: []core.ToolCall{mockToolCall("tc1", "calculator", `{}`)}},
		{Content: "computed"},
	}}

	agent := rt.NewAgent("evt-agent", WithTools(calcTool))
	res, err := agent.Run(context.Background(), "compute")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "computed" {
		t.Fatalf("Output = %q, want %q", res.Output, "computed")
	}
	if res.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1", res.ToolCalls)
	}

	evs, rerr := rt.eventStore.Read(context.Background(), "evt-agent", ares_events.ReadOptions{})
	if rerr != nil {
		t.Fatalf("Read error: %v", rerr)
	}
	// The agent stream now carries one EventLLMCall phase event per iteration
	// (thread-state observability) plus the tool call's Started/Completed:
	// iter0 = [LLMCall, Started, Completed], iter1 (final) = [LLMCall].
	if len(evs) != 4 {
		t.Fatalf("expected 4 agent events (2 LLM phase + 2 tool), got %d", len(evs))
	}
	if evs[0].Type != ares_events.EventLLMCall {
		t.Errorf("event[0].Type = %s, want %s", evs[0].Type, ares_events.EventLLMCall)
	}
	if evs[1].Type != ares_events.EventToolCallStarted {
		t.Errorf("event[1].Type = %s, want %s", evs[1].Type, ares_events.EventToolCallStarted)
	}
	if evs[2].Type != ares_events.EventToolCallCompleted {
		t.Errorf("event[2].Type = %s, want %s", evs[2].Type, ares_events.EventToolCallCompleted)
	}
	if evs[3].Type != ares_events.EventLLMCall {
		t.Errorf("event[3].Type = %s, want %s", evs[3].Type, ares_events.EventLLMCall)
	}
	if evs[2].Version <= evs[1].Version {
		t.Errorf("versions not increasing: %d then %d", evs[1].Version, evs[2].Version)
	}
	// The completed event payload carries the tool name and success flag.
	if got := evs[2].Payload["tool"]; got != "calculator" {
		t.Errorf("completed tool = %v, want %q", got, "calculator")
	}
	if got := evs[2].Payload["success"]; got != true {
		t.Errorf("completed success = %v, want true", got)
	}
}

// TestAgentRun_WithMemory_PersistsMessages (4b) verifies that a run with memory
// enabled persists both the user input (from buildMessages) and the assistant
// response (from the engine) via MemoryManager.AddMessage.
func TestAgentRun_WithMemory_PersistsMessages(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithDefaultMemory(), WithTrace(false))
	defer rt.Close()
	rec := &recordingMemMgr{MemoryManager: rt.memMgr}
	rt.memMgr = rec
	rt.llmSvc = &mockLLMSvc{responses: []*core.GenerateResponse{
		{Content: "hello back"},
	}}

	agent := rt.NewAgent("mem-agent", WithInstruction("help"))
	res, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "hello back" {
		t.Fatalf("Output = %q, want %q", res.Output, "hello back")
	}
	if !res.MemoryUsed {
		t.Fatal("MemoryUsed = false, want true")
	}

	added := rec.snapshot()
	// Expect at least a user message (buildMessages) and an assistant message (engine).
	var userContent, asstContent string
	roles := map[string]bool{}
	for _, m := range added {
		roles[m.role] = true
		switch m.role {
		case roleUser:
			userContent = m.content
		case roleAssistant:
			asstContent = m.content
		}
	}
	if !roles[roleUser] {
		t.Error("expected AddMessage call for user input")
	}
	if !roles[roleAssistant] {
		t.Error("expected AddMessage call for assistant response")
	}
	if userContent != "hi" {
		t.Errorf("user content = %q, want %q", userContent, "hi")
	}
	if asstContent != "hello back" {
		t.Errorf("assistant content = %q, want %q", asstContent, "hello back")
	}
}

// TestAgentRun_DelegatesToEngine verifies the delegation wiring: a mock LLM
// answer flows back through Agent.Run as the Result.Output, and token counts
// are mapped from the engine result into TokenUsage.
func TestAgentRun_DelegatesToEngine(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*core.GenerateResponse{
		{Content: "delegated", Usage: core.TokenUsage{PromptTokens: 7, CompletionTokens: 11}},
	}}

	agent := rt.NewAgent("del-agent", WithInstruction("help"))
	res, err := agent.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "delegated" {
		t.Fatalf("Output = %q, want %q", res.Output, "delegated")
	}
	if res.TokenUsage.Input != 7 {
		t.Errorf("TokenUsage.Input = %d, want 7", res.TokenUsage.Input)
	}
	if res.TokenUsage.Output != 11 {
		t.Errorf("TokenUsage.Output = %d, want 11", res.TokenUsage.Output)
	}
	if res.TokenUsage.Total != 18 {
		t.Errorf("TokenUsage.Total = %d, want 18", res.TokenUsage.Total)
	}
}

// ---- MustNew quickstart tests ----
//
// These tests exercise the zero-parameter MustNew entry point by overriding
// the package-level detectFn with a deterministic detector, so no network
// probe or environment variable is touched. Each test restores detectFn on
// cleanup via setDetectFn.

// setDetectFn overrides the package-level detectFn for the duration of the
// test, restoring the previous value on cleanup. Tests must use this instead
// of writing detectFn directly so cleanup is guaranteed even on failure.
func setDetectFn(t *testing.T, fn func(context.Context, time.Duration) *detector.Environment) {
	t.Helper()
	prev := detectFn
	detectFn = fn
	t.Cleanup(func() { detectFn = prev })
}

// TestMustNew_PanicNoLLM verifies that MustNew panics with a message
// containing "no LLM provider" when the detector returns an empty
// Environment (no Ollama, no API keys).
func TestMustNew_PanicNoLLM(t *testing.T) {
	setDetectFn(t, func(_ context.Context, _ time.Duration) *detector.Environment {
		return &detector.Environment{} // no provider detected
	})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected MustNew to panic, got none")
		}
		msg := fmt.Sprint(r)
		if !strings.Contains(msg, "no LLM provider") {
			t.Fatalf("panic message = %q, want substring %q", msg, "no LLM provider")
		}
	}()
	_ = MustNew()
}

// TestMustNew_Ollama verifies that MustNew returns a usable Runtime with
// default memory enabled when the detector reports a running Ollama daemon.
// The LLM client is created lazily, so no running server is required.
func TestMustNew_Ollama(t *testing.T) {
	setDetectFn(t, func(_ context.Context, _ time.Duration) *detector.Environment {
		return &detector.Environment{
			LLMProvider: "ollama",
			LLMModel:    "llama3.2",
			LLMEndpoint: "http://localhost:11434",
			HasOllama:   true,
		}
	})
	rt := MustNew()
	defer rt.Close()
	if rt == nil {
		t.Fatal("MustNew returned nil Runtime")
	}
	if !rt.memEnabled {
		t.Fatal("rt.memEnabled = false, want true (default memory should be enabled)")
	}
}

// TestMustNew_OpenAI verifies that MustNew returns a usable Runtime with
// default memory enabled when the detector reports an OpenAI API key. The
// API key is read from OPENAI_API_KEY to match buildOptsFromEnv's contract.
func TestMustNew_OpenAI(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	setDetectFn(t, func(_ context.Context, _ time.Duration) *detector.Environment {
		return &detector.Environment{
			LLMProvider:  "openai",
			LLMModel:     defaultOpenAIModel,
			HasOpenAIKey: true,
		}
	})
	rt := MustNew()
	defer rt.Close()
	if rt == nil {
		t.Fatal("MustNew returned nil Runtime")
	}
	if !rt.memEnabled {
		t.Fatal("rt.memEnabled = false, want true (default memory should be enabled)")
	}
}

// TestMustNew_DefaultMemoryEnabled asserts the defaultConfig flip holds
// across every provider supported by buildOptsFromEnv: each MustNew success
// path must yield a Runtime with memEnabled == true without an explicit
// WithDefaultMemory call. The anthropic case is covered here because the
// dedicated TestMustNew_Ollama / TestMustNew_OpenAI tests cover the other two.
func TestMustNew_DefaultMemoryEnabled(t *testing.T) {
	tests := []struct {
		name    string
		env     *detector.Environment
		envVars map[string]string
	}{
		{
			name: "ollama_default_memory_on",
			env: &detector.Environment{
				LLMProvider: "ollama",
				LLMModel:    "llama3.2",
				LLMEndpoint: "http://localhost:11434",
				HasOllama:   true,
			},
		},
		{
			name: "openai_default_memory_on",
			env: &detector.Environment{
				LLMProvider:  "openai",
				LLMModel:     defaultOpenAIModel,
				HasOpenAIKey: true,
			},
			envVars: map[string]string{"OPENAI_API_KEY": "test-key"},
		},
		{
			name: "anthropic_default_memory_on",
			env: &detector.Environment{
				LLMProvider:     "anthropic",
				LLMModel:        "claude-3-haiku",
				HasAnthropicKey: true,
			},
			envVars: map[string]string{"ANTHROPIC_API_KEY": "test-key"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			env := tt.env
			setDetectFn(t, func(_ context.Context, _ time.Duration) *detector.Environment {
				return env
			})
			rt := MustNew()
			defer rt.Close()
			if !rt.memEnabled {
				t.Fatalf("rt.memEnabled = false, want true for %s", tt.name)
			}
		})
	}
}
