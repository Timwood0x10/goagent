package sdk

import (
	"context"
	"strings"
	"sync"
	"testing"

	tools "github.com/Timwood0x10/ares/internal/apitools"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	rescore "github.com/Timwood0x10/ares/internal/tools/resources/core"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
)

// captureLLMSvc scripts Generate responses per call AND captures every
// GenerateRequest so tests can assert on Tools/Messages per iteration. It
// implements the unexported llmService interface so it can replace
// Runtime.llmSvc. When responses are exhausted it returns a fallback final
// answer so the loop terminates.
type captureLLMSvc struct {
	mu        sync.Mutex
	responses []*llmcore.GenerateResponse
	reqs      []*llmcore.GenerateRequest
	calls     int
}

func (m *captureLLMSvc) Generate(_ context.Context, req *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.calls
	m.calls++
	// Capture the request pointer so tests can assert on Tools/Messages per
	// iteration. The engine builds a fresh GenerateRequest each iteration.
	m.reqs = append(m.reqs, req)
	if idx >= len(m.responses) {
		return &llmcore.GenerateResponse{Content: "mock fallback"}, nil
	}
	return m.responses[idx], nil
}

func (m *captureLLMSvc) GetProvider() llmcore.LLMProvider { return llmcore.LLMProviderOllama }
func (m *captureLLMSvc) GetModel() string                 { return "mock-model" }
func (m *captureLLMSvc) Close()                           {}

// snapshotReqs returns a copy of the captured GenerateRequests in call order.
func (m *captureLLMSvc) snapshotReqs() []*llmcore.GenerateRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*llmcore.GenerateRequest, len(m.reqs))
	copy(out, m.reqs)
	return out
}

// Compile-time check that captureLLMSvc satisfies the unexported llmService
// interface used by Runtime.llmSvc.
var _ llmService = (*captureLLMSvc)(nil)

// toolNamesIn returns a set of tool Function.Names from the given tools.
// Used for membership assertions without caring about order.
func toolNamesIn(tls []llmcore.Tool) map[string]bool {
	m := make(map[string]bool, len(tls))
	for _, t := range tls {
		m[t.Function.Name] = true
	}
	return m
}

// findToolMessageContent searches messages for the first tool-role message
// whose content contains sub. Returns true when found. Used to confirm a tool
// was dispatched and its result recorded in the conversation.
func findToolMessageContent(msgs []*llmcore.LLMMessage, sub string) bool {
	for _, m := range msgs {
		if m.Role == roleTool && strings.Contains(m.Content, sub) {
			return true
		}
	}
	return false
}

// emptySelector returns no tools regardless of input. Used to verify that
// runtime-discovered tools (expanded via ToolExpander) appear in later
// iterations even when the selector excludes them from the initial set.
type emptySelector struct{}

func (emptySelector) Select(_ context.Context, _ string, _ []rescore.Tool) ([]rescore.Tool, error) {
	return nil, nil
}

// Compile-time check that emptySelector satisfies toolsource.ToolSelector.
var _ toolsource.ToolSelector = emptySelector{}

// greeterTool is a test tool registered in the runtime registry but NOT
// attached via WithTools, so tests can assert it surfaces only through
// discovery. It returns "hello, <name>".
var greeterTool = tools.ToolFunc{
	ToolName: "greeter",
	ToolDesc: "greets a person by name",
	Fn: func(_ context.Context, p map[string]any) (any, error) {
		name, _ := p["name"].(string)
		return "hello, " + name, nil
	},
}

// echoerTool is a second test tool in the registry, used to ensure the
// discover_tools search returns only matching tools (not all tools).
var echoerTool = tools.ToolFunc{
	ToolName: "echoer",
	ToolDesc: "echoes back the input text",
	Fn: func(_ context.Context, p map[string]any) (any, error) {
		text, _ := p["text"].(string)
		return text, nil
	},
}

// TestAgent_DiscoveryOff_BackwardCompat verifies that when discovery is OFF,
// the LLM receives exactly the tools from WithTools (no discover_tools
// meta-tool) and the ToolExecutor is the plain runtime registry. This is the
// backward-compatibility contract (rule 5.5): default behaviour is unchanged.
func TestAgent_DiscoveryOff_BackwardCompat(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	llm := &captureLLMSvc{responses: []*llmcore.GenerateResponse{{Content: "done"}}}
	rt.llmSvc = llm

	agent := rt.NewAgent("bc-agent", WithTools(calcTool))
	res, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "done" {
		t.Fatalf("Output = %q, want %q", res.Output, "done")
	}

	reqs := llm.snapshotReqs()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(reqs))
	}
	// Exactly one tool: calcTool ("calculator"). No discover_tools, no builtins.
	if len(reqs[0].Tools) != 1 {
		t.Fatalf("expected exactly 1 tool, got %d: %+v", len(reqs[0].Tools), reqs[0].Tools)
	}
	names := toolNamesIn(reqs[0].Tools)
	if !names["calculator"] {
		t.Errorf("expected 'calculator' in tools, got: %+v", names)
	}
	if names[toolsource.DiscoverToolsName] {
		t.Errorf("discover_tools must NOT be present when discovery is off")
	}
}

// TestAgent_DiscoveryOn_ExposesRegistryTools verifies that when discovery is
// ON with the default RegistrySource + AllSelector, tools registered in the
// runtime registry (but NOT in WithTools) are exposed to the LLM.
func TestAgent_DiscoveryOn_ExposesRegistryTools(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	if err := rt.ToolRegistry().Register(greeterTool); err != nil {
		t.Fatal(err)
	}
	llm := &captureLLMSvc{responses: []*llmcore.GenerateResponse{{Content: "done"}}}
	rt.llmSvc = llm

	// No WithTools: greeter is only in the registry. Discovery ON with default
	// AllSelector should surface it from the RegistrySource.
	agent := rt.NewAgent("disc-agent", WithToolDiscovery())
	if _, err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	reqs := llm.snapshotReqs()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNamesIn(reqs[0].Tools)
	if !names["greeter"] {
		t.Errorf("expected 'greeter' (registry tool) in iter0 tools, got: %+v", names)
	}
}

// TestAgent_DiscoveryOn_MetaToolPresent verifies that when discovery is ON,
// the discover_tools meta-tool is present in the LLM tool list.
func TestAgent_DiscoveryOn_MetaToolPresent(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	llm := &captureLLMSvc{responses: []*llmcore.GenerateResponse{{Content: "done"}}}
	rt.llmSvc = llm

	agent := rt.NewAgent("meta-agent", WithToolDiscovery())
	if _, err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	reqs := llm.snapshotReqs()
	if len(reqs) == 0 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNamesIn(reqs[0].Tools)
	if !names[toolsource.DiscoverToolsName] {
		t.Errorf("expected 'discover_tools' in iter0 tools, got: %+v", names)
	}
}

// TestAgent_DiscoveryOn_Expansion verifies the full runtime discovery flow:
//   - iter0: the LLM calls discover_tools("greeter"); greeter is NOT yet in
//     the active tool set (emptySelector excluded it from the initial set).
//   - The meta-tool (real toolsource.NewDiscoverToolsTool, not mocked)
//     searches the registry source and returns greeter as a JSON array.
//   - The engine calls ToolExpander.Expand(["greeter"]), which resolves
//     greeter from the available pool into an LLM tool def.
//   - iter1: greeter IS in the active tool set; the LLM calls it.
//   - iter2: final answer. The greeter tool's result ("hello, world")
//     appears in the tool messages, proving dispatch through the registry.
func TestAgent_DiscoveryOn_Expansion(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	if err := rt.ToolRegistry().Register(greeterTool); err != nil {
		t.Fatal(err)
	}
	if err := rt.ToolRegistry().Register(echoerTool); err != nil {
		t.Fatal(err)
	}
	llm := &captureLLMSvc{responses: []*llmcore.GenerateResponse{
		// iter0: call discover_tools searching for "greeter".
		{Content: "", ToolCalls: []llmcore.ToolCall{
			mockToolCall("d1", toolsource.DiscoverToolsName, `{"query":"greeter"}`),
		}},
		// iter1: call the discovered greeter tool.
		{Content: "", ToolCalls: []llmcore.ToolCall{
			mockToolCall("g1", "greeter", `{"name":"world"}`),
		}},
		// iter2: final answer.
		{Content: "greeted"},
	}}
	rt.llmSvc = llm

	// emptySelector returns no tools, so iter0 has only discover_tools.
	// greeter is in the registry source but excluded from the initial set,
	// so it can only enter the active set via runtime expansion.
	agent := rt.NewAgent("exp-agent", WithToolDiscovery(), WithToolSelector(emptySelector{}))
	res, err := agent.Run(context.Background(), "greet someone")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "greeted" {
		t.Fatalf("Output = %q, want %q", res.Output, "greeted")
	}
	if res.ToolCalls != 2 {
		t.Fatalf("ToolCalls = %d, want 2 (discover_tools + greeter)", res.ToolCalls)
	}

	reqs := llm.snapshotReqs()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(reqs))
	}
	// iter0: greeter must NOT be present (selector excluded it).
	iter0Names := toolNamesIn(reqs[0].Tools)
	if iter0Names["greeter"] {
		t.Errorf("iter0 must NOT include 'greeter' (not yet discovered): %+v", iter0Names)
	}
	if !iter0Names[toolsource.DiscoverToolsName] {
		t.Errorf("iter0 must include 'discover_tools': %+v", iter0Names)
	}
	// iter1: greeter MUST be present (expanded by ToolExpander).
	iter1Names := toolNamesIn(reqs[1].Tools)
	if !iter1Names["greeter"] {
		t.Errorf("iter1 must include 'greeter' (expanded): %+v", iter1Names)
	}
	// The greeter tool was dispatched: its result "hello, world" appears in
	// the messages of iter2's request (as a tool-role message).
	if !findToolMessageContent(reqs[2].Messages, "hello, world") {
		t.Errorf("expected greeter result 'hello, world' in iter2 messages, not found")
	}
}

// uppercaseTool is a rescore.Tool that lives ONLY in a StaticSource — it is
// never registered in the runtime api/tools.Registry. It uppercases the
// "text" param. Used to verify discoveringExecutor can execute
// StaticSource/MultiSource-only tools (regression for the executor-vs-expander
// source mismatch where the executor delegated only to the registry).
type uppercaseTool struct{}

func (uppercaseTool) Name() string                       { return "uppercase" }
func (uppercaseTool) Description() string                { return "Uppercase the input text" }
func (uppercaseTool) Category() rescore.ToolCategory     { return rescore.CategoryCore }
func (uppercaseTool) Capabilities() []rescore.Capability { return nil }
func (uppercaseTool) Parameters() *rescore.ParameterSchema {
	return &rescore.ParameterSchema{
		Type: "object",
		Properties: map[string]*rescore.Parameter{
			"text": {Type: "string", Description: "text to uppercase"},
		},
		Required: []string{"text"},
	}
}

func (uppercaseTool) Execute(_ context.Context, p map[string]interface{}) (rescore.Result, error) {
	text, _ := p["text"].(string)
	return rescore.NewResult(true, strings.ToUpper(text)), nil
}

// TestAgent_Discovery_StaticSourceOnlyToolExecutes verifies that a tool
// available only through a StaticSource (NOT registered in the runtime
// registry) can be both exposed to the LLM and executed end-to-end. This is
// the core MultiSource use case: before the fix, discoveringExecutor delegated
// every non-meta-tool call to the registry and returned "tool not found" for
// static-only tools.
func TestAgent_Discovery_StaticSourceOnlyToolExecutes(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	// Build a MultiSource: the runtime registry (empty of uppercase) + a
	// StaticSource holding ONLY uppercase. uppercase is intentionally NOT
	// registered via rt.ToolRegistry().
	coreReg, err := rt.ToolRegistry().CoreRegistry()
	if err != nil {
		t.Fatalf("core registry: %v", err)
	}
	src := toolsource.NewMultiSource(
		toolsource.NewRegistrySource(coreReg),
		toolsource.NewStaticSource([]rescore.Tool{uppercaseTool{}}),
	)

	llm := &captureLLMSvc{responses: []*llmcore.GenerateResponse{
		// iter0: call the static-only tool directly (AllSelector exposes it).
		{Content: "", ToolCalls: []llmcore.ToolCall{
			mockToolCall("u1", "uppercase", `{"text":"hi"}`),
		}},
		// iter1: final answer echoing the uppercased result.
		{Content: "done"},
	}}
	rt.llmSvc = llm

	agent := rt.NewAgent("static-agent", WithToolDiscovery(), WithToolSource(src))
	res, err := agent.Run(context.Background(), "uppercase hi")
	if err != nil {
		t.Fatalf("Agent.Run error: %v", err)
	}
	if res.Output != "done" {
		t.Fatalf("Output = %q, want %q", res.Output, "done")
	}
	if res.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1 (uppercase)", res.ToolCalls)
	}
	// The static-only tool was executed: its result "HI" must appear in the
	// tool-role message of iter1's request. Before the fix this was
	// "Error: tool not found: uppercase".
	reqs := llm.snapshotReqs()
	if len(reqs) < 2 {
		t.Fatalf("expected >=2 LLM calls, got %d", len(reqs))
	}
	if !findToolMessageContent(reqs[1].Messages, "HI") {
		t.Errorf("expected uppercase result 'HI' in iter1 messages; got: %v", reqs[1].Messages)
	}
}
