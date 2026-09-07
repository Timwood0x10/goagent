// Tool calling — demonstrates how to create and use multiple tools with ARES.
//
// Purpose:
//
//	Show YAML-driven config plus custom tool registration — the main
//	customisation point for most projects. Multiple tools (calculator,
//	weather, string_tools) are registered and exercised across two tasks.
//
// Learning objectives (what this example teaches you):
//   - How to define several custom tools using tools.ToolFunc.
//   - How to register a slice of tools through the Runtime's ToolRegistry.
//   - How to run multiple conversational turns against the same Agent and
//     observe per-turn statistics (tool calls, tokens, duration).
//   - How to implement a minimal recursive-descent arithmetic evaluator
//     that backs the "calculator" tool.
//
// Core APIs used (with package paths):
//   - sdk.LoadConfigFile             — github.com/Timwood0x10/ares/sdk
//   - (*cfg.ConfigFile).ToOptions()  — github.com/Timwood0x10/ares/sdk
//   - sdk.NewRuntime                 — github.com/Timwood0x10/ares/sdk
//   - rt.ToolRegistry().Register     — github.com/Timwood0x10/ares/sdk
//   - rt.NewAgent                    — github.com/Timwood0x10/ares/sdk
//   - sdk.WithInstruction            — github.com/Timwood0x10/ares/sdk
//   - agent.Run                      — github.com/Timwood0x10/ares/sdk
//   - tools.ToolFunc                 — github.com/Timwood0x10/ares/api/tools
//   - tools.Tool (interface)         — github.com/Timwood0x10/ares/api/tools
//
// Run:
//
//	go run examples/02-tool-calling/main.go
//
// Expected output (when an LLM backend is configured):
//
//	---
//	🧑 Calculate (15*23 + 100) / 5
//	🤖 <the assistant's answer>
//	   tools: 1 calls | tokens: <n> | took: <duration>
//
//	---
//	🧑 Reverse the string 'hello world' and uppercase it
//	🤖 <the assistant's answer>
//	   tools: 1 calls | tokens: <n> | took: <duration>
//
// Try adding a new tool to the customTools slice, or changing the
// ToolDesc text to see how the LLM picks tools differently.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and wire everything ──
	// LoadConfigFile reads the YAML config; ToOptions converts it to Runtime
	// options that auto-wire LLM, memory, distillation, AKG, and evolution.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		return
	}
	opts, err := cfg.ToOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ config: %v\n", err)
		return
	}
	// NewRuntime builds the runtime; LLM and all subsystems are auto-wired.
	rt := sdk.NewRuntime(opts...)
	// defer Close releases connections and background resources.
	defer rt.Close()

	// ── Step 2: Register custom tools (the only customisation needed) ──
	// Most projects only need custom tools in Go; everything else is YAML-driven.
	// Loop over the customTools slice and Register each one.
	for _, t := range customTools {
		if err := rt.ToolRegistry().Register(t); err != nil {
			fmt.Fprintf(os.Stderr, "❌ register %s: %v\n", t.Name(), err)
			return
		}
	}

	// ── Step 3: Create Agent ──
	// NewAgent creates a named Agent on the current Runtime.
	// WithInstruction sets the system prompt that guides tool selection.
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction(`You are a helpful assistant with access to tools.
Use the calculator for math, weather for forecasts, and string_tools for text operations.`),
	)

	// ── Step 4: Run multiple conversational turns ──
	// Each task is sent as a separate user message via agent.Run.
	// The Result includes Output text, ToolCalls count, TokenUsage, and Duration.
	tasks := []string{
		"Calculate (15*23 + 100) / 5",
		"Reverse the string 'hello world' and uppercase it",
	}
	for _, input := range tasks {
		fmt.Printf("\n---\n🧑 %s\n", input)
		result, err := agent.Run(ctx, input)
		if err != nil {
			// On per-turn error, print and continue to the next task.
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			continue
		}
		fmt.Printf("🤖 %s\n", result.Output)
		fmt.Printf("   tools: %d calls | tokens: %d | took: %v\n",
			result.ToolCalls, result.TokenUsage.Total, result.Duration)
	}
}

// ── Custom Tools ─────────────────────────────────────────────
// customTools is the slice of tools registered with the Runtime.
var customTools = []tools.Tool{
	calculatorTool,
	weatherTool,
	stringTool,
}

// calculatorTool evaluates a basic arithmetic expression using simpleEval.
var calculatorTool = tools.ToolFunc{
	ToolName: "calculator",
	ToolDesc: "Evaluate a mathematical expression",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		// Extract the "expression" parameter (the LLM generates this).
		expr, _ := params["expression"].(string)
		result, err := simpleEval(expr)
		if err != nil {
			return nil, fmt.Errorf("eval %q: %w", expr, err)
		}
		return fmt.Sprintf("result of %s = %v", expr, result), nil
	},
}

// weatherTool returns a mock weather forecast for a given city.
var weatherTool = tools.ToolFunc{
	ToolName: "get_weather",
	ToolDesc: "Get the current weather for a city",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		city, _ := params["city"].(string)
		return fmt.Sprintf("Weather in %s: 22°C, partly cloudy", city), nil
	},
}

// stringTool performs string operations: reverse, uppercase, lowercase, word_count.
var stringTool = tools.ToolFunc{
	ToolName: "string_tools",
	ToolDesc: "String operations: reverse, uppercase, lowercase, word_count",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		op, _ := params["operation"].(string)
		text, _ := params["text"].(string)
		switch op {
		case "reverse":
			// Reverse the rune slice in place to handle multi-byte characters.
			runes := []rune(text)
			for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
				runes[i], runes[j] = runes[j], runes[i]
			}
			return string(runes), nil
		case "uppercase":
			return strings.ToUpper(text), nil
		case "lowercase":
			return strings.ToLower(text), nil
		case "word_count":
			// strings.Fields splits on whitespace and returns a slice; its length is the word count.
			return len(strings.Fields(text)), nil
		default:
			return nil, fmt.Errorf("unknown operation: %s", op)
		}
	},
}

// simpleEval evaluates basic arithmetic expressions for demo purposes.
func simpleEval(expr string) (float64, error) {
	// Remove all whitespace before tokenising.
	expr = strings.ReplaceAll(expr, " ", "")
	if expr == "" {
		return 0, fmt.Errorf("empty expression")
	}
	// Validate: only digits and arithmetic operators are allowed.
	for _, c := range expr {
		if !strings.ContainsRune("0123456789+-*/().", c) {
			return 0, fmt.Errorf("invalid character: %c", c)
		}
	}
	tokens := tokenize(expr)
	result, err := parseExpr(tokens)
	if err != nil {
		return 0, err
	}
	return result, nil
}

// tokenize splits an expression string into number and operator tokens.
func tokenize(expr string) []string {
	var tokens []string
	var current strings.Builder
	for _, c := range expr {
		if strings.ContainsRune("+-*/()", c) {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
			tokens = append(tokens, string(c))
		} else {
			current.WriteRune(c)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// parseExpr is the entry point of the recursive-descent parser.
func parseExpr(tokens []string) (float64, error) {
	p := &tokenParser{tokens: tokens}
	result, err := p.parseAddSub()
	if err != nil {
		return 0, err
	}
	// After parsing, there should be no leftover tokens.
	if p.pos < len(p.tokens) {
		return 0, fmt.Errorf("unexpected token: %s", p.tokens[p.pos])
	}
	return result, nil
}

// tokenParser holds the token list and current position.
type tokenParser struct {
	tokens []string
	pos    int
}

// peek returns the current token without advancing.
func (p *tokenParser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return ""
}

// consume returns the current token and advances the position.
func (p *tokenParser) consume() string {
	tok := p.peek()
	p.pos++
	return tok
}

// parseAddSub handles addition and subtraction (lowest precedence).
func (p *tokenParser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}
	for {
		op := p.peek()
		if op != "+" && op != "-" {
			break
		}
		p.consume()
		right, err := p.parseMulDiv()
		if err != nil {
			return 0, err
		}
		if op == "+" {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

// parseMulDiv handles multiplication and division (higher precedence).
func (p *tokenParser) parseMulDiv() (float64, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return 0, err
	}
	for {
		op := p.peek()
		if op != "*" && op != "/" {
			break
		}
		p.consume()
		right, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		if op == "*" {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
	}
	return left, nil
}

// parsePrimary handles numbers, parenthesised sub-expressions, and unary minus.
func (p *tokenParser) parsePrimary() (float64, error) {
	tok := p.peek()
	if tok == "" {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	if tok == "(" {
		p.consume()
		val, err := p.parseAddSub()
		if err != nil {
			return 0, err
		}
		if p.peek() != ")" {
			return 0, fmt.Errorf("expected closing parenthesis")
		}
		p.consume()
		return val, nil
	}
	if tok == "-" {
		// Unary minus: parse the primary after it and negate.
		p.consume()
		val, err := p.parsePrimary()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}
	p.consume()
	val, err := strconv.ParseFloat(tok, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", tok, err)
	}
	return val, nil
}
