package sdk

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/api/mcp"
	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/agentloop"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
)

// Agent represents a single agent bound to a Runtime. It carries a name, an
// optional system instruction, an optional set of tools, and an optional
// human-input approval hook.
type Agent struct {
	name        string
	instruction string
	tools       []tools.Tool
	runtime     *Runtime
	humanInput  HumanInputFunc
	maxIter     int
	// maxTokens caps the cumulative prompt+completion tokens per run (<=0 =
	// unbounded); passed to agentloop.Request.
	maxTokens int
	// timeout caps the total wall-clock duration per run (<=0 = no limit);
	// passed to agentloop.Request.
	timeout time.Duration
	// discovery gates runtime tool discovery (see WithToolDiscovery). When
	// false, Agent.Run is byte-for-byte identical to the legacy path.
	discovery bool
	// toolSource is the discovery source; nil means default (RegistrySource
	// over the Runtime registry). Only consulted when discovery is true.
	toolSource toolsource.ToolSource
	// selector narrows the available pool before each run; nil means
	// AllSelector. Only consulted when discovery is true.
	selector toolsource.ToolSelector
}

// HumanInputFunc is called when the agent needs human approval before executing
// a tool call. Return true to approve, false to skip the tool call, or an
// error to abort entirely.
type HumanInputFunc func(ctx context.Context, toolName string, args map[string]any) (approved bool, err error)

// StreamChunk represents a partial streaming result from an agent Run.
type StreamChunk struct {
	// Content is the partial text content.
	Content string
	// Done is true when the stream is complete.
	Done bool
	// Err is set when the stream encounters an error.
	Err error
	// Result is set when Done is true and no error occurred.
	Result *Result
}

// Stream runs the agent against the given input and streams results via a
// channel. The caller must read from the channel until Done is true or Err
// is non-nil.
//
// Usage:
//
//	ch, err := agent.Stream(ctx, "hello")
//	if err != nil { return err }
//	for chunk := range ch {
//	    if chunk.Err != nil { return chunk.Err }
//	    fmt.Print(chunk.Content)
//	}
func (a *Agent) Stream(ctx context.Context, input string) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 32)

	go func() {
		defer close(ch)

		// Run the full agent logic.
		result, err := a.Run(ctx, input)
		if err != nil {
			ch <- StreamChunk{Err: err, Done: true}
			return
		}

		// Simulate streaming by sending the output in chunks.
		runes := []rune(result.Output)
		chunkSize := 10
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			select {
			case ch <- StreamChunk{Content: string(runes[i:end])}:
			case <-ctx.Done():
				ch <- StreamChunk{Err: ctx.Err(), Done: true}
				return
			}
		}

		ch <- StreamChunk{Done: true, Result: result}
	}()

	return ch, nil
}

// Result holds the outcome of a single agent Run.
type Result struct {
	Output     string        `json:"output"`
	ToolCalls  int           `json:"tool_calls"`
	MemoryUsed bool          `json:"memory_used"`
	TokenUsage TokenUsage    `json:"token_usage"`
	Duration   time.Duration `json:"duration"`
}

// TokenUsage summarises LLM token consumption.
type TokenUsage struct {
	Input  int `json:"input"`
	Output int `json:"output"`
	Total  int `json:"total"`
}

// Run executes the agent against the given input and returns the result.
// It builds the message list (system instruction + memory/knowledge context +
// input), creates the memory session, then delegates the ReAct loop
// (LLM call → tool execution → feed back) to agentloop.Engine. The engine is
// the single execution path; Run no longer inlines the loop.
//
//  1. Create the memory session (when memory is enabled).
//  2. Build the message list (system instruction + memory context + input).
//  3. Delegate the ReAct loop to agentloop.Engine.
//  4. Map the engine Result back into the sdk Result.
func (a *Agent) Run(ctx context.Context, input string) (*Result, error) {
	start := time.Now()

	sessionID := uuid.NewString()
	if a.runtime.memEnabled && a.runtime.memMgr != nil {
		sid, err := a.runtime.memMgr.CreateSession(ctx, a.name)
		if err == nil {
			sessionID = sid
		}
	}

	messages := a.buildMessages(ctx, input, sessionID)
	// resolveTools returns the LLM tool defs, the tool executor, and (when
	// discovery is on) a runtime tool expander. When discovery is OFF this is
	// byte-for-byte identical to the legacy path: (toCoreTools(a.tools),
	// a.runtime.toolReg, nil).
	llmTools, toolExecutor, toolExpander := a.resolveTools(ctx, input)

	eng := &agentloop.Engine{
		LLM:            a.runtime.llmSvc,
		Tools:          toolExecutor,
		Events:         a.runtime.eventStore,
		Memory:         a.runtime.memMgr,
		Tracer:         a.traceTracer(),
		MemEnabled:     a.runtime.memEnabled,
		DistillEnabled: a.runtime.distillSvc != nil,
	}
	res, err := eng.Run(ctx, &agentloop.Request{
		Messages:     messages,
		Tools:        llmTools,
		MaxIter:      a.maxIter,
		MaxTokens:    a.maxTokens,
		Timeout:      a.timeout,
		AgentName:    a.name,
		SessionID:    sessionID,
		Input:        input,
		HumanInput:   agentloop.HumanInputFunc(a.humanInput),
		ToolExpander: toolExpander,
	})
	if err != nil {
		return nil, err
	}
	return &Result{
		Output:     res.Output,
		ToolCalls:  res.ToolCalls,
		MemoryUsed: res.MemoryUsed,
		TokenUsage: TokenUsage{
			Input:  res.InputTokens,
			Output: res.OutputTokens,
			Total:  res.InputTokens + res.OutputTokens,
		},
		Duration: time.Since(start),
	}, nil
}

// traceTracer returns log.Printf when tracing is enabled, nil otherwise. The
// agentloop engine treats a nil Tracer as "no trace logging", so this preserves
// the original a.runtime.trace gating without the engine needing a trace bool.
func (a *Agent) traceTracer() func(format string, args ...any) {
	if a.runtime.trace {
		return traceLog
	}
	return nil
}

// traceVerbRe matches a single Go printf verb (or an escaped "%%").
var traceVerbRe = regexp.MustCompile(`%%|%[-+ #0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?[vTtbcdoqxXUeEfFgGsSpw]`)

// traceLog adapts the agentloop Tracer (printf-style) to the structured sdk
// logger: the format verbs are stripped from the message and each printf
// argument is emitted as a positional structured field.
func traceLog(format string, args ...any) {
	msg := traceVerbRe.ReplaceAllStringFunc(format, func(m string) string {
		if m == "%%" {
			return "%"
		}
		return ""
	})
	kvs := make([]any, 0, len(args)*2)
	for i, a := range args {
		kvs = append(kvs, "arg"+strconv.Itoa(i), a)
	}
	log.Info(msg, kvs...)
}

// ---- internal helpers ----

func (a *Agent) buildMessages(ctx context.Context, input, sessionID string) []*core.LLMMessage {
	var msgs []*core.LLMMessage

	if a.instruction != "" {
		msgs = append(msgs, &core.LLMMessage{
			Role:    roleSystem,
			Content: a.instruction,
		})
	}

	// Inject memory context if available
	if a.runtime.memEnabled && a.runtime.memMgr != nil {
		ctxStr, err := a.runtime.memMgr.BuildContext(ctx, input, sessionID)
		if err == nil && ctxStr != "" {
			msgs = append(msgs, &core.LLMMessage{
				Role:    roleSystem,
				Content: ctxStr,
			})
		}
	}

	// Inject AKF knowledge context if available.
	if a.runtime.knowledgeEnabled && a.runtime.knowledgeRT != nil {
		budget := knowledge.TokenBudget{
			MaxTokens: 3000,
			Reserved:  1000,
			ForGraph:  2000,
		}
		graph, err := a.runtime.knowledgeRT.Execute(ctx, input, budget, nil)
		if err == nil && graph != nil && len(graph.Nodes) > 0 {
			c := compiler.NewDefaultCompiler()
			compiled, cErr := c.Compile(ctx, graph, compiler.CompileConfig{
				Formats:  []compiler.Format{compiler.FormatPrompt},
				MaxNodes: 50,
				MaxEdges: 50,
			})
			if cErr == nil && compiled != nil {
				if ctxStr, ok := compiled.Formats[compiler.FormatPrompt]; ok && ctxStr != "" {
					msgs = append(msgs, &core.LLMMessage{
						Role:    roleSystem,
						Content: ctxStr,
					})
				}
			}
		}
	}

	msgs = append(msgs, &core.LLMMessage{
		Role:    roleUser,
		Content: input,
	})

	if a.runtime.memEnabled && a.runtime.memMgr != nil {
		_ = a.runtime.memMgr.AddMessage(ctx, sessionID, roleUser, input)
	}

	return msgs
}

func (a *Agent) toCoreTools(tt []tools.Tool) []core.Tool {
	if len(tt) == 0 {
		return nil
	}
	out := make([]core.Tool, 0, len(tt))
	for _, t := range tt {
		params := t.Parameters()
		if params == nil {
			params = map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			}
		}
		out = append(out, core.Tool{
			Type: "function",
			Function: core.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  params,
			},
		})
	}
	return out
}

// parseArgs unmarshals a JSON arguments string into a map.
func parseArgs(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// mcpToolAdapter wraps an MCP client tool as an SDK tool so it can be used
// with the agent tool registry.
type mcpToolAdapter struct {
	name   string
	desc   string
	client *mcp.Client
}

// a mcpToolAdapter Name returns the MCP tool name.
func (a mcpToolAdapter) Name() string { return a.name }

// a mcpToolAdapter Description returns the MCP tool description.
func (a mcpToolAdapter) Description() string { return a.desc }

// a mcpToolAdapter Parameters returns nil since MCP schemas are handled by the client.
func (a mcpToolAdapter) Parameters() map[string]any { return nil }

// a mcpToolAdapter Capabilities returns nil since MCP tools expose no capabilities.
func (a mcpToolAdapter) Capabilities() []string { return nil }

// a mcpToolAdapter Execute calls the MCP tool with the given params.
func (a mcpToolAdapter) Execute(ctx context.Context, params map[string]any) (tools.Result, error) {
	result, err := a.client.CallTool(ctx, a.name, params)
	if err != nil {
		return tools.Result{Success: false, Data: err.Error()}, nil
	}
	var sb strings.Builder
	for _, c := range result.Content {
		sb.WriteString(c.Text)
	}
	return tools.Result{Success: !result.IsError, Data: sb.String()}, nil
}
