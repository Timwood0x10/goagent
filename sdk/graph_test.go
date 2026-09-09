package sdk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// ---- helpers ----

// newTestRuntime creates a Runtime with a mock LLM so graph tests never hit
// the network. The caller must defer rt.Close().
func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := NewRuntime(WithOllama("llama3.2"), WithoutMemory(), WithTrace(false))
	rt.llmSvc = &mockLLMSvc{responses: []*llmcore.GenerateResponse{
		{Content: "graph-llm-result", Usage: llmcore.TokenUsage{PromptTokens: 1, CompletionTokens: 1}},
	}}
	return rt
}

// ---- Step 1: builder API ----

func TestGraphBuilder(t *testing.T) {
	t.Parallel()
	g := NewGraph("test")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("b", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "b", nil)

	snap := g.snapshot()
	if len(snap.nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(snap.nodes))
	}
	if len(snap.out["a"]) != 1 || snap.out["a"][0] != "b" {
		t.Fatalf("expected 1 edge a→b, got %v", snap.out["a"])
	}
}

func TestGraphBuilderErrors(t *testing.T) {
	t.Parallel()
	t.Run("duplicate_node_id", func(t *testing.T) {
		g := NewGraph("test")
		g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
		g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
		if g.buildErr == nil {
			t.Fatal("expected duplicate node error")
		}
	})
	t.Run("empty_node_id", func(t *testing.T) {
		g := NewGraph("test")
		g.AddNode("", func(_ context.Context, _ map[string]any) error { return nil })
		if g.buildErr == nil {
			t.Fatal("expected empty id error")
		}
	})
	t.Run("unsupported_exec_kind", func(t *testing.T) {
		g := NewGraph("test")
		g.AddNode("a", 42)
		if g.buildErr == nil {
			t.Fatal("expected unsupported kind error")
		}
	})
	t.Run("empty_edge_endpoint", func(t *testing.T) {
		g := NewGraph("test")
		g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
		g.AddEdge("", "a", nil)
		if g.buildErr == nil {
			t.Fatal("expected empty endpoint error")
		}
	})
	t.Run("nil_agent", func(t *testing.T) {
		g := NewGraph("test")
		var a *Agent
		g.AddNode("a", a)
		if g.buildErr == nil {
			t.Fatal("expected nil agent error")
		}
	})
	t.Run("nil_graph", func(t *testing.T) {
		g := NewGraph("test")
		var sub *Graph
		g.AddNode("a", sub)
		if g.buildErr == nil {
			t.Fatal("expected nil graph error")
		}
	})
}

func TestGraphRemoveNode(t *testing.T) {
	t.Parallel()
	g := NewGraph("test")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("b", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("c", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "b", nil)
	g.AddEdge("b", "c", nil)
	g.RemoveNode("b")

	snap := g.snapshot()
	if _, ok := snap.nodes["b"]; ok {
		t.Fatal("node b should be removed")
	}
	if len(snap.out["a"]) != 0 {
		t.Fatalf("edge a→b should be removed, got %v", snap.out["a"])
	}
	if len(snap.in["c"]) != 0 {
		t.Fatalf("edge b→c should be removed, got %v", snap.in["c"])
	}
}

func TestGraphRemoveEdge(t *testing.T) {
	t.Parallel()
	g := NewGraph("test")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("b", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "b", nil)
	g.RemoveEdge("a", "b")

	snap := g.snapshot()
	if len(snap.out["a"]) != 0 {
		t.Fatalf("edge a→b should be removed, got %v", snap.out["a"])
	}
}

// ---- Step 2: node executor types ----

func TestGraphNodeFunctionNode(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("fn")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = "hello"
		return nil
	})

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if res.NodeResults["a"] == nil {
		t.Fatal("expected result for node a")
	}
	if got := res.State["a"]; got != "hello" {
		t.Fatalf("state[a] = %v, want hello", got)
	}
}

func TestGraphNodeAgentNode(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	agent := rt.RegisterAgent("graph-agent", WithInstruction("test"))

	g := NewGraph("agent")
	g.AddNode("a", agent)
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		state["b"] = "done"
		return nil
	})
	g.AddEdge("a", "b", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if res.NodeResults["a"] == nil {
		t.Fatal("expected result for agent node a")
	}
	if res.NodeResults["a"].Output != "graph-llm-result" {
		t.Fatalf("agent node output = %q, want graph-llm-result",
			res.NodeResults["a"].Output)
	}
	if got := res.State["b"]; got != "done" {
		t.Fatalf("state[b] = %v, want done", got)
	}
}

func TestGraphNodeSubgraphNode(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	sub := NewGraph("child")
	sub.AddNode("x", func(_ context.Context, state map[string]any) error {
		state["x"] = "sub-result"
		return nil
	})

	g := NewGraph("parent")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = "parent-before"
		return nil
	})
	g.AddNode("b", sub)
	g.AddEdge("a", "b", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if got := res.State["x"]; got != "sub-result" {
		t.Fatalf("state[x] = %v, want sub-result", got)
	}
	if got := res.State["a"]; got != "parent-before" {
		t.Fatalf("state[a] = %v, want parent-before", got)
	}
}

// TestGraphSubgraphParallelSiblingRace is a regression test for the latent
// race where a subgraph node and a sibling function node run in the SAME
// round: the subgraph's child run shares the parent state map but carries its
// own mutex, so two different locks protect one map. Run with -race.
func TestGraphSubgraphParallelSiblingRace(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.Close()

	sub := NewGraph("child")
	sub.AddNode("x", func(_ context.Context, state map[string]any) error {
		time.Sleep(10 * time.Millisecond) // widen the race window
		state["x"] = "sub-result"
		return nil
	})

	g := NewGraph("parent")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = "parent-before"
		return nil
	})
	g.AddNode("b", sub) // subgraph node
	g.AddNode("c", func(_ context.Context, state map[string]any) error {
		time.Sleep(10 * time.Millisecond) // overlap with subgraph write
		state["c"] = "sibling"
		return nil
	})
	g.AddEdge("a", "b", nil)
	g.AddEdge("a", "c", nil)

	_, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
}

// recordingLLM echoes its last user message as the response and records every
// input it received, so tests can assert data flow between chained agent
// nodes.
type recordingLLM struct {
	mu     sync.Mutex
	inputs []string
}

func (m *recordingLLM) Generate(_ context.Context, req *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error) {
	var last string
	for _, msg := range req.Messages {
		if msg.Role == roleUser {
			last = msg.Content
		}
	}
	m.mu.Lock()
	m.inputs = append(m.inputs, last)
	m.mu.Unlock()
	return &llmcore.GenerateResponse{Content: "echo:" + last}, nil
}

func (m *recordingLLM) GetProvider() llmcore.LLMProvider { return llmcore.LLMProviderOllama }
func (m *recordingLLM) GetModel() string                 { return "mock-model" }
func (m *recordingLLM) Close()                           {}

var _ llmService = (*recordingLLM)(nil)

// TestGraphAgentChainDataFlow is a regression test for the pipeline data-flow
// gap: agent b (downstream) must receive agent a's OUTPUT as its input, not
// the stale global state["input"]. a echoes "echo:<input>", so a correct
// chain yields b's output "echo:echo:start"; the buggy version (b reads
// state["input"] again) yields "echo:start".
func TestGraphAgentChainDataFlow(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.Close()

	rec := &recordingLLM{}
	rt.llmSvc = rec

	agentA := rt.RegisterAgent("agent-a", WithInstruction("you are A"))
	agentB := rt.RegisterAgent("agent-b", WithInstruction("you are B"))

	g := NewGraph("chain")
	g.AddNode("seed", func(_ context.Context, state map[string]any) error {
		state["input"] = "start"
		return nil
	})
	g.AddNode("a", agentA)
	g.AddNode("b", agentB)
	g.AddEdge("seed", "a", nil)
	g.AddEdge("a", "b", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	// a: input "start" → output "echo:start" (stored at state["a"]).
	if got := res.State["a"]; got != "echo:start" {
		t.Fatalf("state[a] = %v, want echo:start", got)
	}
	// b: input MUST be a's output ("echo:start") → output "echo:echo:start".
	if got := res.State["b"]; got != "echo:echo:start" {
		t.Fatalf("state[b] = %v, want echo:echo:start (b must consume a's output, not state[input])", got)
	}
}

// systemCapturingLLM records the system instruction of every request so a
// test can assert that an AddNode'd agent's configuration (instruction/tools)
// actually reaches the LLM instead of being replaced by a bare stand-in.
type systemCapturingLLM struct {
	mu      sync.Mutex
	systems []string
}

func (m *systemCapturingLLM) Generate(_ context.Context, req *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error) {
	var sys string
	for _, msg := range req.Messages {
		if msg.Role == roleSystem {
			sys = msg.Content
		}
	}
	m.mu.Lock()
	m.systems = append(m.systems, sys)
	m.mu.Unlock()
	return &llmcore.GenerateResponse{Content: "ok"}, nil
}

func (m *systemCapturingLLM) GetProvider() llmcore.LLMProvider { return llmcore.LLMProviderOllama }
func (m *systemCapturingLLM) GetModel() string                 { return "mock-model" }
func (m *systemCapturingLLM) Close()                           {}

var _ llmService = (*systemCapturingLLM)(nil)

// TestGraphUnregisteredAgentKeepsConfig is a regression test for the config
// loss bug: an agent created via NewAgent (NOT RegisterAgent) and added with
// AddNode must run with its OWN instruction. The buggy version stored only the
// agent name, so Submit fell back to a bare capability-named agent with an
// empty instruction — the custom "MARKER-INSTRUCTION" never reached the LLM.
func TestGraphUnregisteredAgentKeepsConfig(t *testing.T) {
	rt := newTestRuntime(t)
	defer rt.Close()

	cap := &systemCapturingLLM{}
	rt.llmSvc = cap

	// NewAgent, deliberately NOT RegisterAgent — this is the path that lost
	// the configuration.
	agent := rt.NewAgent("solo", WithInstruction("MARKER-INSTRUCTION"))

	g := NewGraph("keep-config")
	g.AddNode("only", agent)

	_, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	found := false
	for _, s := range cap.systems {
		if strings.Contains(s, "MARKER-INSTRUCTION") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("agent node ran with a bare stand-in; custom instruction lost. systems=%v", cap.systems)
	}
}

// ---- Step 3: static DAG ----

func TestGraphDAGSerialChain(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	var order []string
	var mu sync.Mutex

	g := NewGraph("serial")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		mu.Lock()
		order = append(order, "a")
		mu.Unlock()
		state["step"] = 1
		return nil
	})
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		mu.Lock()
		order = append(order, "b")
		mu.Unlock()
		if state["step"].(int) != 1 {
			return errors.New("b: state not propagated")
		}
		state["step"] = 2
		return nil
	})
	g.AddNode("c", func(_ context.Context, state map[string]any) error {
		mu.Lock()
		order = append(order, "c")
		mu.Unlock()
		if state["step"].(int) != 2 {
			return errors.New("c: state not propagated")
		}
		state["step"] = 3
		return nil
	})
	g.AddEdge("a", "b", nil)
	g.AddEdge("b", "c", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("execution order = %v, want [a b c]", order)
	}
	if got := res.State["step"].(int); got != 3 {
		t.Fatalf("state[step] = %d, want 3", got)
	}
}

func TestGraphDAGFanOutFanIn(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	var mu sync.Mutex
	var concurrent, maxConcurrent int

	track := func() {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
	}
	untrack := func() {
		mu.Lock()
		concurrent--
		mu.Unlock()
	}

	// JoinAll wait semantics: d (fan-in node) must NOT run until BOTH b and c
	// have settled. b/c record completion here; d asserts both are done.
	var doneMu sync.Mutex
	bDone, cDone := false, false

	g := NewGraph("fan")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = true
		return nil
	})
	g.AddNode("b", func(_ context.Context, _ map[string]any) error {
		track()
		defer untrack()
		time.Sleep(50 * time.Millisecond)
		doneMu.Lock()
		bDone = true
		doneMu.Unlock()
		return nil
	})
	g.AddNode("c", func(_ context.Context, _ map[string]any) error {
		track()
		defer untrack()
		time.Sleep(50 * time.Millisecond)
		doneMu.Lock()
		cDone = true
		doneMu.Unlock()
		return nil
	})
	g.AddNode("d", func(_ context.Context, state map[string]any) error {
		doneMu.Lock()
		both := bDone && cDone
		doneMu.Unlock()
		if !both {
			return errors.New("fan-in node d ran before both b and c completed (JoinAll violated)")
		}
		state["d"] = true
		return nil
	})
	g.AddEdge("a", "b", nil)
	g.AddEdge("a", "c", nil)
	g.AddEdge("b", "d", nil)
	g.AddEdge("c", "d", nil)

	_, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	// b and c should have run concurrently (at least 2 at the same time).
	mu.Lock()
	mc := maxConcurrent
	mu.Unlock()
	if mc < 2 {
		t.Fatalf("b and c should have executed concurrently, max concurrent = %d", mc)
	}
}

func TestGraphDAGNodeFailure(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("failure")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error {
		return nil
	})
	g.AddNode("b", func(_ context.Context, _ map[string]any) error {
		return errors.New("b failed")
	})
	g.AddNode("d", func(_ context.Context, _ map[string]any) error {
		return nil
	})
	g.AddEdge("a", "b", nil)
	g.AddEdge("b", "d", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err == nil {
		t.Fatal("expected error from failed node b")
	}
	if !strings.Contains(err.Error(), "b failed") {
		t.Fatalf("error should mention b failed, got: %v", err)
	}
	if res == nil {
		t.Fatal("expected partial results")
	}
	if res.NodeResults["a"] == nil {
		t.Fatal("node a should have a result")
	}
	if res.NodeResults["d"] != nil {
		t.Fatal("node d should NOT have a result (depends on failed b)")
	}
}

func TestGraphDAGEmptyGraph(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("empty")
	_, err := rt.RunGraph(context.Background(), g)
	if err == nil {
		t.Fatal("expected error on empty graph")
	}
}

func TestGraphDAGSingleNode(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("single")
	g.AddNode("only", func(_ context.Context, state map[string]any) error {
		state["only"] = true
		return nil
	})

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if res.NodeResults["only"] == nil {
		t.Fatal("expected result for single node")
	}
}

func TestGraphDAGCycleWithoutRouter(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("cycle")
	g.MaxIterations = 3 // hard cap to terminate the cycle
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("b", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "b", nil)
	g.AddEdge("b", "a", nil) // creates a cycle

	// Should not hang — MaxIterations bounds re-execution.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rt.RunGraph(context.Background(), g)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunGraph with cycle hung — MaxIterations not enforced")
	}
}

// ---- Step 4: condition edges + router ----

func TestGraphConditionBranch(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("cond")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["route"] = "left"
		return nil
	})
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		state["b"] = true
		return nil
	})
	g.AddNode("c", func(_ context.Context, state map[string]any) error {
		state["c"] = true
		return nil
	})
	// Only the edge a→b fires when state["route"]=="left".
	g.AddEdge("a", "b", func(s map[string]any) bool { return s["route"] == "left" })
	g.AddEdge("a", "c", func(s map[string]any) bool { return s["route"] == "right" })

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if res.State["b"] != true {
		t.Fatal("branch b should have executed")
	}
	if res.State["c"] != nil {
		t.Fatal("branch c should NOT have executed")
	}
}

func TestGraphRouterLoop(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("router-loop")
	g.MaxIterations = 10
	var execCount atomic.Int64
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		c := execCount.Add(1)
		state["count"] = int(c)
		return nil
	})
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		state["b"] = true
		return nil
	})
	g.AddEdge("a", "b", nil)
	// Router: after a completes, jump to b (bypassing static edge is fine too);
	// after b completes, loop back to a until count >= 3.
	g.SetRouter(func(_ context.Context, completedID string, state map[string]any) string {
		c, _ := state["count"].(int)
		switch completedID {
		case "a":
			return "b" // a→b jump
		case "b":
			if c < 3 {
				return "a" // b→a loop
			}
		}
		return ""
	})

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if got := execCount.Load(); got < 3 {
		t.Fatalf("node a should have been re-executed >= 3 times, got %d", got)
	}
	if res.State["b"] != true {
		t.Fatal("node b should have executed at least once")
	}
}

// TestGraphFanInRouterSeesFirstCompletion locks the documented v1 limitation:
// when a single round settles MULTIPLE nodes (fan-in / orchestration), the
// router is seeded with only the FIRST completion in that round, not every
// one. This test pins that contract so a future change to multi-completion
// routing is a deliberate, test-visible decision rather than an accident.
func TestGraphFanInRouterSeesFirstCompletion(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	var mu sync.Mutex
	var routerSaw []string

	g := NewGraph("fanin-router")
	// a fans out to b and c; b and c settle in the SAME round.
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("b", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddNode("c", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "b", nil)
	g.AddEdge("a", "c", nil)
	g.SetRouter(func(_ context.Context, completedID string, _ map[string]any) string {
		mu.Lock()
		routerSaw = append(routerSaw, completedID)
		mu.Unlock()
		return ""
	})

	_, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The round that settles {b,c} must feed the router exactly ONE of them —
	// never both. (The round that settles {a} feeds "a".) So "b" and "c" never
	// both appear in routerSaw.
	sawB, sawC := false, false
	for _, id := range routerSaw {
		if id == "b" {
			sawB = true
		}
		if id == "c" {
			sawC = true
		}
	}
	if sawB && sawC {
		t.Fatalf("router saw both fan-in completions %v; v1 contract is FIRST-only", routerSaw)
	}
}

func TestGraphRouterEmptyReturn(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	var order []string
	var mu sync.Mutex

	g := NewGraph("router-empty")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error {
		mu.Lock()
		order = append(order, "a")
		mu.Unlock()
		return nil
	})
	g.AddNode("b", func(_ context.Context, _ map[string]any) error {
		mu.Lock()
		order = append(order, "b")
		mu.Unlock()
		return nil
	})
	g.AddEdge("a", "b", nil)
	// Router always returns "" — should fall back to static edges.
	g.SetRouter(func(_ context.Context, _ string, _ map[string]any) string {
		return ""
	})

	_, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("execution order = %v, want [a b] (static BFS)", order)
	}
}

func TestGraphRouterDeadloop(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("deadloop")
	g.MaxIterations = 3
	var execCount atomic.Int64
	g.AddNode("a", func(_ context.Context, _ map[string]any) error {
		execCount.Add(1)
		return nil
	})
	// Router always returns "a" — infinite loop, bounded by MaxIterations.
	g.SetRouter(func(_ context.Context, _ string, _ map[string]any) string {
		return "a"
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rt.RunGraph(context.Background(), g)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("router deadloop hung — MaxIterations not enforced")
	}
	if got := execCount.Load(); got > 3 {
		t.Fatalf("exec count = %d, should be <= MaxIterations(3)", got)
	}
}

// ---- Step 5: safety boundaries ----

func TestGraphTimeout(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("timeout")
	g.Timeout = 50 * time.Millisecond
	g.AddNode("a", func(ctx context.Context, _ map[string]any) error {
		select {
		case <-time.After(200 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	done := make(chan error, 1)
	go func() {
		_, err := rt.RunGraph(context.Background(), g)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunGraph hung on timeout")
	}
}

func TestGraphNodePanic(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("panic")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error {
		panic("boom")
	})
	g.AddNode("b", func(_ context.Context, _ map[string]any) error {
		return nil
	})
	g.AddEdge("a", "b", nil)

	res, err := rt.RunGraph(context.Background(), g)
	if err == nil {
		t.Fatal("expected error from panicked node")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("error should mention panic, got: %v", err)
	}
	if res == nil {
		t.Fatal("expected partial results")
	}
	// b should NOT have a result because a panicked → its outgoing edges die.
	if res.NodeResults["b"] != nil {
		t.Fatal("node b should NOT have a result (a panicked)")
	}
}

func TestGraphConcurrentMutationWhileRunning(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("concurrent-mut")
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = true
		return nil
	})
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		// Slow node so the concurrent AddNode has time to fire.
		time.Sleep(20 * time.Millisecond)
		state["b"] = true
		return nil
	})
	g.AddEdge("a", "b", nil)

	// Concurrently add a node while the graph is running.
	go func() {
		g.AddNode("c", func(_ context.Context, state map[string]any) error {
			state["c"] = true
			return nil
		})
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rt.RunGraph(context.Background(), g)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunGraph with concurrent mutation hung")
	}
}

func TestGraphForwardReferenceEdge(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("forward")
	// Edge added before the target node exists (forward reference).
	g.AddNode("a", func(_ context.Context, state map[string]any) error {
		state["a"] = true
		return nil
	})
	g.AddEdge("a", "b", nil) // b not yet added
	g.AddNode("b", func(_ context.Context, state map[string]any) error {
		state["b"] = true
		return nil
	})

	res, err := rt.RunGraph(context.Background(), g)
	if err != nil {
		t.Fatalf("RunGraph error: %v", err)
	}
	if res.State["b"] != true {
		t.Fatal("forward-referenced node b should have executed")
	}
}

func TestGraphEdgeToUnknownTarget(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("unknown-target")
	g.AddNode("a", func(_ context.Context, _ map[string]any) error { return nil })
	g.AddEdge("a", "nonexistent", nil)

	_, err := rt.RunGraph(context.Background(), g)
	if err == nil {
		t.Fatal("expected error for edge to unknown target")
	}
}

func TestGraphNilGraph(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	_, err := rt.RunGraph(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil graph")
	}
}

func TestGraphNodeNoExecutableKind(t *testing.T) {
	t.Parallel()
	rt := newTestRuntime(t)
	defer rt.Close()

	g := NewGraph("no-kind")
	// Manually construct a node with no exec set — this simulates an
	// impossible state to verify the default branch catches it.
	g.nodes["bad"] = graphNode{}
	g.order = append(g.order, "bad")

	_, err := rt.RunGraph(context.Background(), g)
	if err == nil {
		t.Fatal("expected error for node with no executable kind")
	}
}

// TestRunGraphMaxRoundConcurrency locks the concurrency cap: with
// MaxRoundConcurrency=N and N+ ready nodes in one round, at most N execute
// simultaneously (observed via a barrier that counts concurrent entrants).
func TestRunGraphMaxRoundConcurrency(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	var mu sync.Mutex
	inside, peak := 0, 0
	release := make(chan struct{})
	node := func() func(context.Context, map[string]any) error {
		return func(_ context.Context, _ map[string]any) error {
			mu.Lock()
			inside++
			if inside > peak {
				peak = inside
			}
			mu.Unlock()
			<-release
			mu.Lock()
			inside--
			mu.Unlock()
			return nil
		}
	}

	g := NewGraph("throttled").
		AddNode("n1", node()).
		AddNode("n2", node()).
		AddNode("n3", node()).
		AddNode("n4", node()).
		AddEdge("n1", "n2", nil).
		AddEdge("n1", "n3", nil).
		AddEdge("n1", "n4", nil)
	g.MaxRoundConcurrency = 2

	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()
	if _, err := rt.RunGraph(context.Background(), g); err != nil {
		t.Fatalf("RunGraph: %v", err)
	}
	if peak > 2 {
		t.Fatalf("concurrent peak = %d, want <= MaxRoundConcurrency(2)", peak)
	}
}
