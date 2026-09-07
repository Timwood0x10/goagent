// Package runtime — shared runtime infrastructure for workflow execution.
//
// This file defines the Executable interface that unifies all node execution
// types (Agent, Tool, FuncNode) under a single contract.
//
// Current stage: interface extraction — existing types implement this
// interface via adapters; the single Runner
// will consume it natively.

package runtime

import "context"

// NodeOutput is the result of executing a single node.
type NodeOutput struct {
	// Data holds the structured output of the node. The schema depends on
	// the node type (Agent, Tool, Func, etc.).
	Data map[string]any
	// Error is populated when the node execution failed.
	Error string
}

// ExecutionContext carries the input and shared state for a single node
// execution. It is the minimal version of the future ExecutionScope.
type ExecutionContext struct {
	// Input is the node's resolved input string or structured data.
	Input string
	// Variables holds the workflow-level variables at the point of execution.
	Variables map[string]any
}

// Executable is the common execution interface for all workflow node types.
// Every node type — Agent, Tool, FuncNode — implements this
// interface, allowing the future single Runner to execute any node
// without type-switching on the binding.
type Executable interface {
	// Execute runs the node with the given context and returns the output.
	Execute(ctx context.Context, execCtx *ExecutionContext) (*NodeOutput, error)
}
