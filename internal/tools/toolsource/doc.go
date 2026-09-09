// Package toolsource provides MCP-style proactive tool discovery (ToolSource)
// and per-task tool selection (ToolSelector), wired back into agentloop.Engine.
//
// # Boundary
//
// toolsource discovers and selects executable tools (resources/core.Tool).
// Conversion of those tools into LLM tool structs (internal/llmcore.Tool) happens in
// the sdk layer, not here. This keeps toolsource decoupled from the LLM API
// and avoids an import cycle with agentloop.
//
// # Components
//
//   - ToolSource discovers available tools from one or more origins:
//     RegistrySource adapts core.Registry; StaticSource wraps an explicit
//     list; MultiSource merges several with first-wins name dedup
//     (priority: Static > Registry > MCP).
//   - ToolSelector narrows the available pool per run: AllSelector (default,
//     zero behavior change), TagSelector (TaggableTool tag match), and
//     CapabilitySelector (reuses planner.ToolResolver + planner.ToolScorer).
//   - discover_tools meta-tool: lets the LLM query the ToolSource at runtime
//     by name/description/tag and returns compact {name, description} entries
//     so the LLM can decide which tool to ask for; the Engine then expands the
//     chosen names via ToolExpander.
package toolsource
