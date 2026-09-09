package sdk

import (
	"context"
	"log/slog"

	"github.com/Timwood0x10/ares/internal/agentloop"
	tools "github.com/Timwood0x10/ares/internal/apitools"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	rescore "github.com/Timwood0x10/ares/internal/tools/resources/core"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
)

// resolveTools builds the LLM tool definitions, the tool executor, and (when
// discovery is on) a runtime tool expander for one Agent.Run.
//
// When discovery is OFF the return is byte-for-byte identical to the legacy
// path: (toCoreTools(a.tools), a.runtime.toolReg, nil). When ON it builds a
// ToolSource, narrows via a ToolSelector, appends the discover_tools meta-tool
// to the LLM tool list, and returns a discoveringExecutor (so the meta-tool is
// callable without being registered in the public registry) plus a
// sourceExpander (so runtime-discovered names are expanded into LLM defs).
//
// The runtime's syscall tools (spawn_agent/create_task) are appended to
// the static LLM tool list in every fallback path, so an SDK agent sees them
// regardless of its own WithTools list or discovery state (the tools execute
// against the same toolReg the engine uses).
func (a *Agent) resolveTools(
	ctx context.Context,
	input string,
) ([]llmcore.Tool, agentloop.ToolExecutor, agentloop.ToolExpander) {
	legacy := func() ([]llmcore.Tool, agentloop.ToolExecutor, agentloop.ToolExpander) {
		llmTools := a.toCoreTools(a.tools)
		if a.runtime != nil {
			llmTools = append(llmTools, a.runtime.syscallTools...)
		}
		return llmTools, a.runtime.toolReg, nil
	}
	if !a.discovery {
		return legacy()
	}
	source := a.resolveToolSource()
	if source == nil {
		// resolveToolSource already logged the fallback reason.
		return legacy()
	}
	available, err := source.Tools(ctx)
	if err != nil {
		slog.Warn("sdk: discovery source.Tools failed; falling back to static tools",
			"error", err)
		return a.toCoreTools(a.tools), a.runtime.toolReg, nil
	}
	selected, err := a.selectTools(ctx, input, available)
	if err != nil {
		slog.Warn("sdk: discovery select failed; using full available set", "error", err)
		selected = available
	}
	llmTools := rescoreToolsToLLM(selected)
	// Append the discover_tools meta-tool so the LLM can search at runtime.
	metaTool := toolsource.NewDiscoverToolsTool(source)
	llmTools = append(llmTools, rescoreToolToLLM(metaTool))
	// Build a name→tool index over the full available snapshot so the executor
	// can run StaticSource/MultiSource-only tools the public registry does not
	// hold (the expander already draws from this same snapshot).
	availableByName := make(map[string]rescore.Tool, len(available))
	for _, t := range available {
		if t == nil {
			continue
		}
		availableByName[t.Name()] = t
	}
	executor := &discoveringExecutor{
		delegate:  a.runtime.toolReg,
		metaTool:  metaTool,
		available: availableByName,
	}
	expander := newSourceExpander(available)
	return llmTools, executor, expander
}

// resolveToolSource returns the configured ToolSource, or the default
// RegistrySource over the Runtime's core registry. Returns nil (and logs) when
// the default cannot be built, so resolveTools can fall back gracefully.
func (a *Agent) resolveToolSource() toolsource.ToolSource {
	if a.toolSource != nil {
		return a.toolSource
	}
	coreReg, err := a.runtime.toolReg.CoreRegistry()
	if err != nil {
		slog.Warn("sdk: discovery default source build failed; falling back to static tools",
			"error", err)
		return nil
	}
	return toolsource.NewRegistrySource(coreReg)
}

// selectTools narrows the available pool using the configured selector, or
// AllSelector when none is set.
func (a *Agent) selectTools(
	ctx context.Context,
	input string,
	available []rescore.Tool,
) ([]rescore.Tool, error) {
	selector := a.selector
	if selector == nil {
		selector = toolsource.AllSelector{}
	}
	return selector.Select(ctx, input, available)
}

// rescoreToolsToLLM converts a slice of internal llmcore.Tool to api/llmcore.Tool
// LLM structs, mirroring llmcore.Registry.GetLLMTools (build a ToolSchema per
// tool and call ToolSchemaToLLMTool). Nil tools are skipped.
func rescoreToolsToLLM(tt []rescore.Tool) []llmcore.Tool {
	if len(tt) == 0 {
		return nil
	}
	out := make([]llmcore.Tool, 0, len(tt))
	for _, t := range tt {
		if t == nil {
			continue
		}
		out = append(out, rescoreToolToLLM(t))
	}
	return out
}

// rescoreToolToLLM converts one internal llmcore.Tool to an api/llmcore.Tool LLM
// struct by building a ToolSchema and calling ToolSchemaToLLMTool (same path
// as llmcore.Registry.GetLLMTools).
func rescoreToolToLLM(t rescore.Tool) llmcore.Tool {
	schema := rescore.ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		Category:    t.Category(),
		Parameters:  t.Parameters(),
	}
	return rescore.ToolSchemaToLLMTool(schema)
}

// discoveringExecutor wraps the Runtime's tool registry so that calls to the
// discover_tools meta-tool (which is NOT registered in the api/tools.Registry)
// are dispatched to the internal meta-tool. Non-meta-tool names are resolved
// against the run-start `available` snapshot first (this covers tools sourced
// from a StaticSource/MultiSource that are NOT in the public registry), then
// fall back to the delegate registry (for tools registered after the snapshot
// was taken). This keeps the single execution path (rule 5.1) while injecting
// the meta-tool without polluting the public registry and without leaving
// MultiSource-only tools unexecutable.
type discoveringExecutor struct {
	delegate  agentloop.ToolExecutor  // a.runtime.toolReg
	metaTool  rescore.Tool            // toolsource.NewDiscoverToolsTool(source)
	available map[string]rescore.Tool // run-start snapshot by tool Name()
}

// Execute dispatches discover_tools to the meta-tool. Every other name is first
// looked up in the available snapshot (so StaticSource/MultiSource-only tools
// are executable), then delegated to the registry as a fallback for
// late-registered tools. The meta-tool's llmcore.Result is converted to
// tools.Result so the engine's ToolExecutor contract is satisfied.
func (d *discoveringExecutor) Execute(
	ctx context.Context,
	name string,
	args map[string]any,
) (tools.Result, error) {
	if name == toolsource.DiscoverToolsName {
		// map[string]any is map[string]interface{} in Go 1.18+, so args
		// passes directly to the internal Execute signature.
		res, err := d.metaTool.Execute(ctx, args)
		if err != nil {
			return tools.Result{Success: false, Data: err.Error()}, nil
		}
		return rescoreResultToToolsResult(res), nil
	}
	// Prefer the run-start snapshot: it is the source of truth for what the
	// LLM was allowed to see/call, and it holds StaticSource-only tools the
	// registry does not. Fall back to the live registry for tools registered
	// after the snapshot was captured.
	if t, ok := d.available[name]; ok {
		res, err := t.Execute(ctx, args)
		if err != nil {
			return tools.Result{Success: false, Data: err.Error()}, nil
		}
		return rescoreResultToToolsResult(res), nil
	}
	return d.delegate.Execute(ctx, name, args)
}

// rescoreResultToToolsResult maps an internal llmcore.Result to the public
// tools.Result. When Success is false and Data is nil, the Error string is
// promoted to Data so the engine's %v formatting surfaces a useful message
// to the LLM (mirroring mcpToolAdapter.Execute error handling).
func rescoreResultToToolsResult(r rescore.Result) tools.Result {
	data := r.Data
	if data == nil && r.Error != "" {
		data = r.Error
	}
	return tools.Result{Success: r.Success, Data: data}
}

// sourceExpander implements agentloop.ToolExpander by looking up names in the
// available tool snapshot captured at run start (the source.Tools result).
// The byName map is built once at construction — available is a read-only
// snapshot never mutated after that point, so no locking is needed.
// Missing names are silently skipped (the engine already dedups by name, so
// a missing name simply yields no new tool).
type sourceExpander struct {
	byName map[string]rescore.Tool
}

// newSourceExpander builds an expander with a pre-computed name→Tool index.
func newSourceExpander(available []rescore.Tool) *sourceExpander {
	m := make(map[string]rescore.Tool, len(available))
	for _, t := range available {
		if t != nil {
			m[t.Name()] = t
		}
	}
	return &sourceExpander{byName: m}
}

// Expand resolves names into LLM tool defs from the pre-built index.
// Each name is looked up by Name(); unknown names are skipped without error.
func (e *sourceExpander) Expand(_ context.Context, names []string) ([]llmcore.Tool, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]llmcore.Tool, 0, len(names))
	for _, n := range names {
		if t, ok := e.byName[n]; ok {
			out = append(out, rescoreToolToLLM(t))
		}
	}
	return out, nil
}

// Compile-time checks that the sdk discovery adapters satisfy the engine's
// narrow interfaces. If a signature drifts, these fail the build.
var (
	_ agentloop.ToolExecutor = (*discoveringExecutor)(nil)
	_ agentloop.ToolExpander = (*sourceExpander)(nil)
)
