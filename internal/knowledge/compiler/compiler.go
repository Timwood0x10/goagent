package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

// Format specifies the output format for compilation.
type Format string

const (
	FormatPrompt     Format = "prompt"
	FormatMarkdown   Format = "markdown"
	FormatJSON       Format = "json"
	FormatXML        Format = "xml"
	FormatToolSchema Format = "tool_schema"
)

// CompileConfig controls the compilation process.
type CompileConfig struct {
	Formats    []Format `json:"formats"`     // Output formats to generate
	MaxTokens  int      `json:"max_tokens"`  // Max token budget for output
	IncludeRaw bool     `json:"include_raw"` // Include Raw content
	MaxNodes   int      `json:"max_nodes"`   // Max nodes to include (0 = all)
	MaxEdges   int      `json:"max_edges"`   // Max edges to include (0 = all)
}

// CompiledContext is the result of compilation, containing the graph data
// formatted as one or more output formats.
type CompiledContext struct {
	Intent  string            `json:"intent"`
	Formats map[Format]string `json:"formats"`
	Metrics CompileMetrics    `json:"metrics"`
}

// CompileMetrics tracks compilation statistics.
type CompileMetrics struct {
	InputNodes       int     `json:"input_nodes"`
	InputEdges       int     `json:"input_edges"`
	OutputTokens     int     `json:"output_tokens"`
	CompressionRatio float64 `json:"compression_ratio"`
}

// Compiler compiles a WorkingGraph into LLM-ready contexts.
type Compiler interface {
	// Compile converts a WorkingGraph into the requested formats.
	Compile(ctx context.Context, graph *knowledge.WorkingGraph, cfg CompileConfig) (*CompiledContext, error)
}

// DefaultCompiler is the default implementation of Compiler.
type DefaultCompiler struct{}

// NewDefaultCompiler creates a new DefaultCompiler.
func NewDefaultCompiler() *DefaultCompiler {
	return &DefaultCompiler{}
}

// Compile converts the graph into the requested output formats.
func (c *DefaultCompiler) Compile(_ context.Context, graph *knowledge.WorkingGraph, cfg CompileConfig) (*CompiledContext, error) {
	if graph == nil {
		return nil, errors.New("graph cannot be nil")
	}

	formats := make(map[Format]string)
	metrics := CompileMetrics{
		InputNodes: len(graph.Nodes),
		InputEdges: len(graph.Edges),
	}

	for _, f := range cfg.Formats {
		var content string
		var err error
		switch f {
		case FormatPrompt:
			content, err = c.formatPrompt(graph, cfg)
		case FormatMarkdown:
			content, err = c.formatMarkdown(graph, cfg)
		case FormatJSON:
			content, err = c.formatJSON(graph, cfg)
		case FormatXML:
			content, err = c.formatXML(graph, cfg)
		case FormatToolSchema:
			content, err = c.formatToolSchema(graph, cfg)
		default:
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("format %s: %w", f, err)
		}
		formats[f] = content
		metrics.OutputTokens += estimateTokens(content)
	}

	if metrics.InputNodes > 0 {
		metrics.CompressionRatio = float64(metrics.OutputTokens) / float64(metrics.InputNodes*50)
	}

	return &CompiledContext{
		Intent:  goalFromGraph(graph),
		Formats: formats,
		Metrics: metrics,
	}, nil
}

// goalFromGraph derives a readable intent description from the graph.
func goalFromGraph(graph *knowledge.WorkingGraph) string {
	if graph == nil || len(graph.Nodes) == 0 {
		return ""
	}
	// Use the first non-empty summary as the intent.
	for _, obj := range graph.Nodes {
		if obj.Summary != "" {
			return obj.Summary
		}
	}
	return "knowledge context"
}

// formatPrompt generates a structured prompt for LLM chat completion.
func (c *DefaultCompiler) formatPrompt(graph *knowledge.WorkingGraph, cfg CompileConfig) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "## Knowledge Context\n\n")

	// Nodes section.
	nodeCount := len(graph.Nodes)
	if cfg.MaxNodes > 0 && cfg.MaxNodes < nodeCount {
		nodeCount = cfg.MaxNodes
	}

	fmt.Fprintf(&b, "### Nodes (%d)\n\n", nodeCount)
	count := 0
	for _, obj := range graph.Nodes {
		if cfg.MaxNodes > 0 && count >= cfg.MaxNodes {
			break
		}
		summary := obj.Summary
		if summary == "" {
			summary = obj.ID
		}
		fmt.Fprintf(&b, "- **%s**", obj.ID)
		if obj.Type != "" {
			fmt.Fprintf(&b, " [%s]", obj.Type)
		}
		fmt.Fprintf(&b, ": %s", summary)
		if obj.Confidence > 0 {
			fmt.Fprintf(&b, " (conf: %.2f)", obj.Confidence)
		}
		b.WriteString("\n")
		count++
	}

	// Edges section.
	if len(graph.Edges) > 0 {
		edgeCount := len(graph.Edges)
		if cfg.MaxEdges > 0 && cfg.MaxEdges < edgeCount {
			edgeCount = cfg.MaxEdges
		}
		fmt.Fprintf(&b, "\n### Relations (%d)\n\n", edgeCount)
		for i, e := range graph.Edges {
			if cfg.MaxEdges > 0 && i >= cfg.MaxEdges {
				break
			}
			fmt.Fprintf(&b, "- %s --[%s]--> %s", e.From, e.Name, e.To)
			if e.Score > 0 {
				fmt.Fprintf(&b, " (score: %.2f)", e.Score)
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

// formatMarkdown generates a human-readable markdown document.
// Unlike formatPrompt which targets LLM consumption, this format is optimized
// for human reading with full headers, bullet lists, and structured sections.
func (c *DefaultCompiler) formatMarkdown(graph *knowledge.WorkingGraph, cfg CompileConfig) (string, error) {
	var b strings.Builder

	fmt.Fprintf(&b, "# Knowledge Context\n\n")

	// Nodes section.
	nodeCount := len(graph.Nodes)
	if cfg.MaxNodes > 0 && cfg.MaxNodes < nodeCount {
		nodeCount = cfg.MaxNodes
	}

	fmt.Fprintf(&b, "## Nodes (%d)\n\n", nodeCount)
	count := 0
	for _, obj := range graph.Nodes {
		if cfg.MaxNodes > 0 && count >= cfg.MaxNodes {
			break
		}
		fmt.Fprintf(&b, "- **`%s`**", obj.ID)
		if obj.Type != "" {
			fmt.Fprintf(&b, " `[%s]`", obj.Type)
		}
		if obj.Namespace != "" {
			fmt.Fprintf(&b, " `[%s]`", obj.Namespace)
		}
		b.WriteString("\n")
		if obj.Summary != "" {
			fmt.Fprintf(&b, "  - Summary: %s\n", obj.Summary)
		}
		if obj.Confidence > 0 {
			fmt.Fprintf(&b, "  - Confidence: %.2f\n", obj.Confidence)
		}
		if len(obj.Tags) > 0 {
			fmt.Fprintf(&b, "  - Tags: `%s`\n", strings.Join(obj.Tags, "`, `"))
		}
		count++
	}

	// Edges section.
	if len(graph.Edges) > 0 {
		edgeCount := len(graph.Edges)
		if cfg.MaxEdges > 0 && cfg.MaxEdges < edgeCount {
			edgeCount = cfg.MaxEdges
		}
		fmt.Fprintf(&b, "\n## Relations (%d)\n\n", edgeCount)
		b.WriteString("| From | Relation | To | Score |\n")
		b.WriteString("|------|----------|----|-------|\n")
		for i, e := range graph.Edges {
			if cfg.MaxEdges > 0 && i >= cfg.MaxEdges {
				break
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.2f |\n", e.From, e.Name, e.To, e.Score)
		}
	}

	return b.String(), nil
}

// formatJSON generates a JSON-serializable representation.
func (c *DefaultCompiler) formatJSON(graph *knowledge.WorkingGraph, cfg CompileConfig) (string, error) {
	var b strings.Builder
	b.WriteString("{\n  \"nodes\": [\n")

	first := true
	count := 0
	for _, obj := range graph.Nodes {
		if cfg.MaxNodes > 0 && count >= cfg.MaxNodes {
			break
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		// Use json.Marshal for string fields: %q produces Go string-literal
		// escaping (e.g. \n stays as two chars), which is not valid JSON
		// when the raw content contains control characters.
		idJSON, _ := json.Marshal(obj.ID)
		typeJSON, _ := json.Marshal(string(obj.Type))
		summaryJSON, _ := json.Marshal(obj.Summary)
		fmt.Fprintf(&b, "    {\"id\":%s,\"type\":%s,\"summary\":%s,\"confidence\":%.2f}",
			idJSON, typeJSON, summaryJSON, obj.Confidence)
		count++
	}

	b.WriteString("\n  ],\n  \"edges\": [\n")
	first = true
	for i, e := range graph.Edges {
		if cfg.MaxEdges > 0 && i >= cfg.MaxEdges {
			break
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		fmt.Fprintf(&b, "    {\"from\":%q,\"to\":%q,\"name\":%q,\"score\":%.2f}",
			e.From, e.To, e.Name, e.Score)
	}

	b.WriteString("\n  ]\n}\n")
	return b.String(), nil
}

// formatXML generates an XML representation of the graph.
func (c *DefaultCompiler) formatXML(graph *knowledge.WorkingGraph, cfg CompileConfig) (string, error) {
	var b strings.Builder
	b.WriteString("<knowledge_context>\n")

	// Nodes section.
	nodeCount := len(graph.Nodes)
	if cfg.MaxNodes > 0 && cfg.MaxNodes < nodeCount {
		nodeCount = cfg.MaxNodes
	}
	fmt.Fprintf(&b, "  <nodes count=\"%d\">\n", nodeCount)
	count := 0
	for _, obj := range graph.Nodes {
		if cfg.MaxNodes > 0 && count >= cfg.MaxNodes {
			break
		}
		// Use proper XML attribute escaping: %q produces Go string quotes
		// which break XML when the value contains & or <.
		fmt.Fprintf(&b, "    <node id=%s type=%s confidence=\"%.2f\">\n",
			escapeXMLAttr(obj.ID), escapeXMLAttr(string(obj.Type)), obj.Confidence)
		if obj.Summary != "" {
			fmt.Fprintf(&b, "      <summary>%s</summary>\n", escapeXML(obj.Summary))
		}
		b.WriteString("    </node>\n")
		count++
	}
	b.WriteString("  </nodes>\n")

	// Edges section.
	if len(graph.Edges) > 0 {
		edgeCount := len(graph.Edges)
		if cfg.MaxEdges > 0 && cfg.MaxEdges < edgeCount {
			edgeCount = cfg.MaxEdges
		}
		fmt.Fprintf(&b, "  <relations count=\"%d\">\n", edgeCount)
		for i, e := range graph.Edges {
			if cfg.MaxEdges > 0 && i >= cfg.MaxEdges {
				break
			}
			fmt.Fprintf(&b, "    <relation from=%q to=%q name=%q score=\"%.2f\"/>\n",
				e.From, e.To, e.Name, e.Score)
		}
		b.WriteString("  </relations>\n")
	}

	b.WriteString("</knowledge_context>\n")
	return b.String(), nil
}

// formatToolSchema generates a JSON Schema representation for tool calling.
func (c *DefaultCompiler) formatToolSchema(graph *knowledge.WorkingGraph, cfg CompileConfig) (string, error) {
	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("  \"$schema\": \"http://json-schema.org/draft-07/schema#\",\n")
	b.WriteString("  \"type\": \"object\",\n")
	b.WriteString("  \"properties\": {\n")
	b.WriteString("    \"nodes\": {\n")
	b.WriteString("      \"type\": \"array\",\n")
	b.WriteString("      \"description\": \"Knowledge nodes relevant to the task\",\n")
	b.WriteString("      \"items\": {\n")
	b.WriteString("        \"type\": \"object\",\n")
	b.WriteString("        \"properties\": {\n")
	b.WriteString("          \"id\": {\"type\": \"string\"},\n")
	b.WriteString("          \"type\": {\"type\": \"string\"},\n")
	b.WriteString("          \"summary\": {\"type\": \"string\"},\n")
	b.WriteString("          \"confidence\": {\"type\": \"number\"}\n")
	b.WriteString("        },\n")
	b.WriteString("        \"required\": [\"id\", \"summary\"]\n")
	b.WriteString("      }\n")
	b.WriteString("    },\n")
	b.WriteString("    \"relations\": {\n")
	b.WriteString("      \"type\": \"array\",\n")
	b.WriteString("      \"description\": \"Relations between knowledge nodes\",\n")
	b.WriteString("      \"items\": {\n")
	b.WriteString("        \"type\": \"object\",\n")
	b.WriteString("        \"properties\": {\n")
	b.WriteString("          \"from\": {\"type\": \"string\"},\n")
	b.WriteString("          \"to\": {\"type\": \"string\"},\n")
	b.WriteString("          \"name\": {\"type\": \"string\"},\n")
	b.WriteString("          \"score\": {\"type\": \"number\"}\n")
	b.WriteString("        },\n")
	b.WriteString("        \"required\": [\"from\", \"to\", \"name\"]\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  },\n")
	b.WriteString("  \"required\": [\"nodes\"]\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// escapeXML escapes special XML characters in a string.
func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// escapeXMLAttr escapes a value and wraps it in double quotes for use as
// an XML attribute value.
func escapeXMLAttr(s string) string {
	return "\"" + escapeXML(s) + "\""
}

// estimateTokens provides a rough estimate of token count.
func estimateTokens(s string) int {
	// Rough estimate: ~4 chars per token for English text.
	return len(s) / 4
}
