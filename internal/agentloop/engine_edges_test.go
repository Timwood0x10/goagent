package agentloop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	tools "github.com/Timwood0x10/ares/internal/apitools"
	ares_events "github.com/Timwood0x10/ares/internal/ares_events"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// traceCapturer records every line forwarded to the engine's Tracer so tests
// can assert trace output without printing. Run invokes the Tracer
// synchronously, but the mutex keeps this safe under -race if a future engine
// ever emits from a goroutine.
type traceCapturer struct {
	mu    sync.Mutex
	lines []string
}

// trace is the Tracer-compatible callback that records the formatted line.
func (c *traceCapturer) trace(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

// snapshot returns a copy of the captured trace lines in order.
func (c *traceCapturer) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// traceContains reports whether any captured trace line contains sub.
func traceContains(lines []string, sub string) bool {
	for _, l := range lines {
		if contains(l, sub) {
			return true
		}
	}
	return false
}

// TestEngine_MaxIterDefault covers the branch where Request.MaxIter <= 0: the
// engine must fall back to DefaultMaxIterations, so an always-tool-calling LLM
// is capped at that number of iterations instead of looping forever.
func TestEngine_MaxIterDefault(t *testing.T) {
	tests := []struct {
		name    string
		maxIter int
	}{
		{name: "zero_maxiter_uses_default_cap", maxIter: 0},
		{name: "negative_maxiter_uses_default_cap", maxIter: -3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &mockLLM{responses: []*llmcore.GenerateResponse{
				{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("tc", "calc", `{}`)}},
			}}
			eng := &Engine{
				LLM: llm,
				Tools: &mockToolExecutor{results: map[string]tools.Result{
					"calc": {Success: true, Data: "42"},
				}},
			}
			req := &Request{
				Messages: []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
				Tools:    []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
				MaxIter:  tt.maxIter,
			}
			res, err := eng.Run(context.Background(), req)
			if err != nil {
				t.Fatalf("Engine.Run error: %v", err)
			}
			if res.Output != maxIterationsReachedMsg {
				t.Errorf("Output = %q, want %q", res.Output, maxIterationsReachedMsg)
			}
			if res.ToolCalls != DefaultMaxIterations {
				t.Errorf("ToolCalls = %d, want default cap %d", res.ToolCalls, DefaultMaxIterations)
			}
			if res.MemoryUsed {
				t.Error("MemoryUsed = true, want false (MemEnabled not set)")
			}
		})
	}
}

// TestEngine_ToolErrorAppendedToMessages covers the tool-execution error path:
// a failing tool must still be counted as a tool call, its error text fed back
// to the LLM as a tool message, and the Completed event marked success=false.
// The loop must continue to the final answer.
func TestEngine_ToolErrorAppendedToMessages(t *testing.T) {
	sink := &fakeEventSink{}
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("tc1", "calc", `{"a":1}`)}},
		{Content: "recovered answer"},
	}}
	toolEx := &mockToolExecutor{errs: map[string]error{"calc": errors.New("tool exploded")}}
	eng := &Engine{LLM: llm, Tools: toolEx, Events: sink, DistillEnabled: true}
	req := &Request{
		Messages:  []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:     []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:   5,
		AgentName: "err-agent",
		SessionID: "sess-err",
		Input:     "hi",
	}
	res, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	if res.Output != "recovered answer" {
		t.Errorf("Output = %q, want %q", res.Output, "recovered answer")
	}
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1 (failed tool still counts)", res.ToolCalls)
	}
	// The error text must reach the LLM as a tool-role message.
	reqs := llm.snapshotReqs()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(reqs))
	}
	if _, ok := findToolMessage(reqs[1].Messages, "Error: tool exploded"); !ok {
		t.Error("final iteration messages missing tool error text")
	}
	// 2 iterations × 1 EventLLMCall (thread-state phase) + Started + Completed
	// + TaskCompleted = 5 events in that order.
	evs := sink.snapshot()
	if len(evs) != 5 {
		t.Fatalf("expected 5 events, got %d", len(evs))
	}
	if evs[0].Type != ares_events.EventLLMCall {
		t.Fatalf("event[0].Type = %s, want %s", evs[0].Type, ares_events.EventLLMCall)
	}
	if evs[2].Type != ares_events.EventToolCallCompleted {
		t.Fatalf("event[2].Type = %s, want %s", evs[2].Type, ares_events.EventToolCallCompleted)
	}
	if got := evs[2].Payload["success"]; got != false {
		t.Errorf("Completed success = %v, want false", got)
	}
	if got := evs[2].Payload["result"]; got != "Error: tool exploded" {
		t.Errorf("Completed result = %v, want %q", got, "Error: tool exploded")
	}
}

// TestEngine_HumanRejectSkipsTool verifies a rejected tool call is neither
// executed nor emitted as events: the human sees the parsed args, a rejection
// tool message is fed back to the LLM, and the loop continues to the answer.
func TestEngine_HumanRejectSkipsTool(t *testing.T) {
	sink := &fakeEventSink{}
	var seenName string
	var seenArgs map[string]any
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("tc1", "calc", `{"x":1}`)}},
		{Content: "final answer"},
	}}
	toolEx := &mockToolExecutor{}
	eng := &Engine{
		LLM:            llm,
		Tools:          toolEx,
		Events:         sink,
		DistillEnabled: false, // keep the event list empty for the rejection check
	}
	req := &Request{
		Messages:  []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:     []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:   5,
		AgentName: "reject-agent",
		SessionID: "sess-reject",
		Input:     "hi",
		HumanInput: func(_ context.Context, toolName string, args map[string]any) (bool, error) {
			seenName = toolName
			seenArgs = args
			return false, nil
		},
	}
	res, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	if res.Output != "final answer" {
		t.Errorf("Output = %q, want %q", res.Output, "final answer")
	}
	if seenName != "calc" {
		t.Errorf("HumanInput saw tool name %q, want %q", seenName, "calc")
	}
	if seenArgs == nil || seenArgs["x"] != float64(1) {
		t.Errorf("HumanInput saw args %v, want map with x=1", seenArgs)
	}
	if got := toolEx.lastToolName(); got != "" {
		t.Errorf("tool executor ran %q despite rejection", got)
	}
	// The rejected tool must not be executed nor emitted as tool events; the
	// only events are the two LLM-call phase events (one per iteration).
	evs := sink.snapshot()
	if len(evs) != 2 {
		t.Fatalf("expected 2 LLM phase events, got %d: %+v", len(evs), evs)
	}
	for i, ev := range evs {
		if ev.Type != ares_events.EventLLMCall {
			t.Errorf("event[%d].Type = %s, want %s (no tool events on rejection)", i, ev.Type, ares_events.EventLLMCall)
		}
	}
	reqs := llm.snapshotReqs()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(reqs))
	}
	if _, ok := findToolMessage(reqs[1].Messages, "rejected by human operator"); !ok {
		t.Error("final iteration messages missing rejection tool message")
	}
}

// TestEngine_TracerInvoked covers the non-nil Tracer branch: every iteration
// must forward a trace line carrying the agent name.
func TestEngine_TracerInvoked(t *testing.T) {
	trc := &traceCapturer{}
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("tc1", "calc", `{}`)}},
		{Content: "done"},
	}}
	eng := &Engine{
		LLM:    llm,
		Tools:  &mockToolExecutor{results: map[string]tools.Result{"calc": {Success: true, Data: "42"}}},
		Tracer: trc.trace,
	}
	req := &Request{
		Messages:  []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:     []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:   5,
		AgentName: "trace-agent",
	}
	if _, err := eng.Run(context.Background(), req); err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	lines := trc.snapshot()
	if len(lines) == 0 {
		t.Fatal("expected trace lines, got none")
	}
	if !contains(lines[0], "trace-agent") {
		t.Errorf("first trace line %q missing agent name", lines[0])
	}
}

// TestEngine_DiscoverToolsParseFailed covers the discover_tools branch where
// the result is not JSON: the engine traces the failure, never calls the
// expander, and keeps the active tool set unchanged while the run continues.
func TestEngine_DiscoverToolsParseFailed(t *testing.T) {
	trc := &traceCapturer{}
	expander := &fakeToolExpander{tools: map[string]llmcore.Tool{
		"search": {Type: "function", Function: llmcore.FunctionDefinition{Name: "search"}},
	}}
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("d1", DiscoverToolsName, `{}`)}},
		{Content: "done"},
	}}
	toolEx := &mockToolExecutor{results: map[string]tools.Result{
		DiscoverToolsName: {Success: true, Data: "not-json-at-all"},
	}}
	eng := &Engine{LLM: llm, Tools: toolEx, Tracer: trc.trace}
	req := &Request{
		Messages:     []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:        []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:      5,
		AgentName:    "parse-agent",
		ToolExpander: expander,
	}
	res, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	if res.Output != "done" {
		t.Errorf("Output = %q, want %q", res.Output, "done")
	}
	if got := expander.callCount(); got != 0 {
		t.Errorf("expander call count = %d, want 0 (parse failed)", got)
	}
	reqs := llm.snapshotReqs()
	if len(reqs) != 2 || len(reqs[1].Tools) != 1 || reqs[1].Tools[0].Function.Name != "calc" {
		t.Errorf("active tools after parse failure = %+v, want only [calc]", reqs[1].Tools)
	}
	if !traceContains(trc.snapshot(), "parse failed") {
		t.Error("expected 'parse failed' trace line")
	}
}

// TestEngine_DiscoverToolsEmptyResult covers the discover_tools branch where
// the result parses to zero entries: the expander must not be called and the
// run completes normally.
func TestEngine_DiscoverToolsEmptyResult(t *testing.T) {
	expander := &fakeToolExpander{}
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("d1", DiscoverToolsName, `{}`)}},
		{Content: "done"},
	}}
	toolEx := &mockToolExecutor{results: map[string]tools.Result{
		DiscoverToolsName: {Success: true, Data: `[]`},
	}}
	eng := &Engine{LLM: llm, Tools: toolEx}
	req := &Request{
		Messages:     []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:        []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:      5,
		ToolExpander: expander,
	}
	res, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	if res.Output != "done" {
		t.Errorf("Output = %q, want %q", res.Output, "done")
	}
	if got := expander.callCount(); got != 0 {
		t.Errorf("expander call count = %d, want 0 (empty result)", got)
	}
}

// TestEngine_DiscoverToolsExpanderError covers the branch where ToolExpander
// returns an error: the engine traces the failure, keeps the active set
// unchanged, and the run still completes with the final answer.
func TestEngine_DiscoverToolsExpanderError(t *testing.T) {
	const discoverResult = `[{"name":"search","description":"s"}]`
	trc := &traceCapturer{}
	expander := &fakeToolExpander{
		tools: map[string]llmcore.Tool{
			"search": {Type: "function", Function: llmcore.FunctionDefinition{Name: "search"}},
		},
		err: errors.New("source down"),
	}
	llm := &mockLLM{responses: []*llmcore.GenerateResponse{
		{Content: "", ToolCalls: []llmcore.ToolCall{toolCall("d1", DiscoverToolsName, `{}`)}},
		{Content: "done"},
	}}
	toolEx := &mockToolExecutor{results: map[string]tools.Result{
		DiscoverToolsName: {Success: true, Data: discoverResult},
	}}
	eng := &Engine{LLM: llm, Tools: toolEx, Tracer: trc.trace}
	req := &Request{
		Messages:     []*llmcore.LLMMessage{{Role: "user", Content: "hi"}},
		Tools:        []llmcore.Tool{{Type: "function", Function: llmcore.FunctionDefinition{Name: "calc"}}},
		MaxIter:      5,
		AgentName:    "expand-agent",
		ToolExpander: expander,
	}
	res, err := eng.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Engine.Run error: %v", err)
	}
	if res.Output != "done" {
		t.Errorf("Output = %q, want %q", res.Output, "done")
	}
	reqs := llm.snapshotReqs()
	if len(reqs) != 2 || len(reqs[1].Tools) != 1 || reqs[1].Tools[0].Function.Name != "calc" {
		t.Errorf("active tools after expander error = %+v, want only [calc]", reqs[1].Tools)
	}
	if !traceContains(trc.snapshot(), "expand failed") {
		t.Error("expected 'expand failed' trace line")
	}
}

// TestParseArgs locks down the three parseArgs outcomes: empty and invalid
// JSON both yield nil, while a valid object is decoded into a map.
func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want map[string]any
	}{
		{name: "empty_string_returns_nil", raw: "", want: nil},
		{name: "invalid_json_returns_nil", raw: "not json", want: nil},
		{name: "valid_object_decoded", raw: `{"a":"b","n":1}`, want: map[string]any{"a": "b", "n": float64(1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseArgs(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseArgs(%q) = %v, want %v", tt.raw, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseArgs(%q)[%q] = %v, want %v", tt.raw, k, got[k], v)
				}
			}
		})
	}
}
