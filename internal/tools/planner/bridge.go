package planner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// ToolExecutionBridge wraps a tool registry with planner-based fallback.
//
// When a tool call arrives (from LLM or direct invocation):
//  1. Try the named tool directly.
//  2. If not found, use the CapabilityPlanner to resolve the user's
//     intent and select the best tool automatically.
//
// This bridge ensures backward compatibility: existing LLM tool-name
// execution continues to work unchanged, while the planner fallback
// handles cases where the LLM does not specify a valid tool name.
type ToolExecutionBridge struct {
	registry *core.Registry
	planner  *Planner
	evidence EvidenceStore
	log      *slog.Logger // Cached module logger to avoid per-invocation allocation.
}

// NewToolExecutionBridge creates a bridge with planner fallback.
// Returns error if registry or planner is nil.
func NewToolExecutionBridge(registry *core.Registry, planner *Planner, evidence EvidenceStore) (*ToolExecutionBridge, error) {
	if registry == nil {
		return nil, errors.New("tool_bridge: registry is nil")
	}
	if planner == nil {
		return nil, errors.New("tool_bridge: planner is nil")
	}
	if evidence == nil {
		evidence = NewMemoryEvidenceStore()
	}
	return &ToolExecutionBridge{
		registry: registry,
		planner:  planner,
		evidence: evidence,
		log:      logger.Module("tool_bridge"),
	}, nil
}

// Execute runs a tool by name with fallback to planner resolution.
//
// For single-step plans, the tool is executed directly.
// For multi-step plans, the bridge validates the DAG, executes steps in
// dependency order, and supports per-step fallback tools.
//
// Args:
//
//	ctx - cancellation and timeout context.
//	toolName - tool name from LLM (may be empty for planner-only).
//	params - parameters to pass to the tool.
//	userRequest - original user request for planner fallback.
//
// Returns:
//
//	result - tool execution result (for multi-step, the final step's result).
//	err - error if all resolution paths fail.
func (b *ToolExecutionBridge) Execute(
	ctx context.Context,
	toolName string,
	params map[string]interface{},
	userRequest string,
) (core.Result, error) {
	log := b.log

	// Path 1: Try the explicitly named tool.
	if toolName != "" {
		tool, exists := b.registry.Get(toolName)
		if exists {
			start := time.Now()
			result, err := tool.Execute(ctx, params)
			latency := time.Since(start)

			// Save execution evidence. The direct path must populate CapabilityName
			// so the evidence scorer (keyed by ToolName:CapabilityName) can match
			// it with planner-based path evidence. Without this, the most common
			// execution path (LLM directly naming a known tool) produces evidence
			// that the scorer never consumes (REVIEW #15a).
			capName := primaryCapabilityName(tool)
			if saveErr := b.evidence.Save(ctx, &ToolEvidence{
				ToolName:       toolName,
				CapabilityName: capName,
				Success:        err == nil && result.Success,
				Latency:        latency,
				Timestamp:      time.Now(),
			}); saveErr != nil {
				log.Warn("tool_bridge: failed to save evidence",
					"tool", toolName,
					"error", saveErr,
				)
			}

			log.Info("tool_bridge: direct execution",
				"tool", toolName,
				"success", err == nil && result.Success,
				"latency_ms", latency.Milliseconds(),
			)
			return result, err
		}

		log.Warn("tool_bridge: tool not found, triggering planner fallback",
			"tool", toolName,
		)
	}

	// Path 2: Planner fallback — resolve intent and build execution plan.
	if userRequest == "" {
		return core.Result{}, fmt.Errorf("tool_bridge: tool %q not found and no user request for fallback", toolName)
	}

	plan, pErr := b.planner.Plan(ctx, userRequest)
	if pErr != nil {
		return core.Result{}, fmt.Errorf("tool_bridge: planner fallback failed: %w", pErr)
	}

	if len(plan.Steps) == 0 {
		return core.Result{}, fmt.Errorf("tool_bridge: planner produced zero steps for %q", userRequest)
	}

	// Validate the plan DAG before execution.
	// Structural errors (cycle, missing dependency) block execution.
	// IO incompatibility warnings are advisory and do not block.
	validator := NewDAGValidator()
	if errs := validator.Validate(plan); len(errs) > 0 {
		for _, e := range errs {
			// Hard-block on structural errors.
			if e.Code == "cycle_detected" || e.Code == "missing_dependency" || e.Code == "incompatible_io" ||
				e.Code == "duplicate_id" || e.Code == "empty_id" {
				return core.Result{}, fmt.Errorf("tool_bridge: plan DAG invalid: %w", e)
			}
			// Advisory warnings only (IO incompatibility, etc).
			log.Warn("tool_bridge: plan DAG advisory",
				"plan_id", plan.PlanID,
				"code", e.Code,
				"message", e.Message,
			)
		}
	}

	// Execute plan steps in dependency order.
	if plan.IsMultiStep {
		return b.executeMultiStep(ctx, plan, params)
	}

	// Single step execution.
	return b.executeSingleStep(ctx, plan, params)
}

// ExecutePlan runs a pre-built execution plan without planner fallback.
// This is useful for executing manually constructed plans, test fixtures,
// or re-running previously planned DAGs.
//
// The plan is validated before execution. Invalid plans return errors without
// executing any steps.
//
// Args:
//
//	ctx - cancellation and timeout context.
//	plan - the pre-built execution plan (must be non-nil).
//	params - user-provided parameters that override plan defaults.
//
// Returns:
//
//	result - the final step's result (for multi-step, the last step's result).
//	err - error if execution fails.
func (b *ToolExecutionBridge) ExecutePlan(
	ctx context.Context,
	plan *ExecutionPlan,
	params map[string]interface{},
) (core.Result, error) {
	if plan == nil {
		return core.Result{}, errors.New("tool_bridge: plan is nil")
	}
	if len(plan.Steps) == 0 {
		return core.Result{}, errors.New("tool_bridge: plan has no steps")
	}

	log := b.log

	// Validate the plan DAG before execution.
	validator := NewDAGValidator()
	if errs := validator.Validate(plan); len(errs) > 0 {
		for _, e := range errs {
			if e.Code == "cycle_detected" || e.Code == "missing_dependency" || e.Code == "incompatible_io" {
				return core.Result{}, fmt.Errorf("tool_bridge: plan DAG invalid: %w", e)
			}
			log.Warn("tool_bridge: plan DAG advisory",
				"plan_id", plan.PlanID,
				"code", e.Code,
				"message", e.Message,
			)
		}
	}

	if plan.IsMultiStep {
		return b.executeMultiStep(ctx, plan, params)
	}
	return b.executeSingleStep(ctx, plan, params)
}

// executeSingleStep runs a single-step plan with merged parameters.
func (b *ToolExecutionBridge) executeSingleStep(
	ctx context.Context,
	plan *ExecutionPlan,
	params map[string]interface{},
) (core.Result, error) {
	step := plan.Steps[0]

	// Merge plan defaults with user params (user params win).
	mergedParams := make(map[string]interface{})
	for k, v := range step.Parameters {
		mergedParams[k] = v
	}
	for k, v := range params {
		mergedParams[k] = v
	}

	return b.executeStep(ctx, step, mergedParams)
}

// executeMultiStep runs a multi-step plan in dependency order.
// It computes a topological order from the step dependencies, then executes
// each step sequentially, passing results forward where applicable.
func (b *ToolExecutionBridge) executeMultiStep(
	ctx context.Context,
	plan *ExecutionPlan,
	params map[string]interface{},
) (core.Result, error) {
	log := b.log

	// Topological sort: compute execution order from dependencies.
	order, err := topoSort(plan.Steps)
	if err != nil {
		return core.Result{}, fmt.Errorf("tool_bridge: %w", err)
	}
	stepResults := make(map[string]core.Result)

	var lastResult core.Result

	for _, stepID := range order {
		// Find the step by StepID.
		var step *ExecutionStep
		for i := range plan.Steps {
			if plan.Steps[i].StepID == stepID {
				step = &plan.Steps[i]
				break
			}
		}
		if step == nil {
			return core.Result{}, fmt.Errorf("tool_bridge: step %q not found in plan", stepID)
		}

		// Merge params: cascade previous step output, then plan defaults, then user params.
		mergedParams := make(map[string]interface{})
		for k, v := range step.Parameters {
			mergedParams[k] = v
		}
		// Map dependency step output to current step's parameter names by capability type.
		if len(step.DependsOn) > 0 {
			for _, depID := range step.DependsOn {
				depResult, ok := stepResults[depID]
				if !ok || depResult.Data == nil {
					continue
				}
				// Find the dependency step's capability name.
				var depCapaName string
				for i := range plan.Steps {
					if plan.Steps[i].StepID == depID {
						depCapaName = plan.Steps[i].CapabilityName
						break
					}
				}
				depStep := ExecutionStep{
					StepID:         depID,
					CapabilityName: depCapaName,
				}
				if err := b.bindDependencyOutput(mergedParams, *step, depStep, depResult); err != nil {
					return core.Result{}, err
				}
			}
		}
		for k, v := range params {
			mergedParams[k] = v
		}

		// Execute the step with fallback support.
		result, err := b.executeStepWithFallback(ctx, step, mergedParams)
		if err != nil {
			return core.Result{}, fmt.Errorf("tool_bridge: step %q failed: %w", stepID, err)
		}
		stepResults[stepID] = result
		lastResult = result

		log.Info("tool_bridge: multi-step step complete",
			"plan_id", plan.PlanID,
			"step", stepID,
			"tool", step.ToolName,
			"success", result.Success,
		)
	}

	return lastResult, nil
}

// primaryCapabilityName extracts the first capability name from a tool's
// Capabilities() list. Returns an empty string when the tool has no
// capabilities, preserving backward compatibility with tools that do not
// declare capabilities.
func primaryCapabilityName(tool core.Tool) string {
	caps := tool.Capabilities()
	if len(caps) == 0 {
		return ""
	}
	return string(caps[0])
}

// executeStep runs a single step with the given parameters and saves evidence.
func (b *ToolExecutionBridge) executeStep(
	ctx context.Context,
	step ExecutionStep,
	params map[string]interface{},
) (core.Result, error) {
	log := b.log
	tool, exists := b.registry.Get(step.ToolName)
	if !exists {
		return core.Result{}, fmt.Errorf("tool_bridge: tool %q not registered", step.ToolName)
	}

	start := time.Now()
	result, err := tool.Execute(ctx, params)
	latency := time.Since(start)

	// Save execution evidence for future scoring.
	saveErr := b.evidence.Save(ctx, &ToolEvidence{
		ToolName:       step.ToolName,
		CapabilityName: step.CapabilityName,
		Success:        err == nil && result.Success,
		Latency:        latency,
		Timestamp:      time.Now(),
	})
	if saveErr != nil {
		log.Warn("tool_bridge: failed to save evidence",
			"tool", step.ToolName,
			"error", saveErr,
		)
	}

	log.Info("tool_bridge: step execution",
		"tool", step.ToolName,
		"success", err == nil && result.Success,
		"latency_ms", latency.Milliseconds(),
	)

	return result, err
}

// executeStepWithFallback tries the primary tool, then each fallback in order.
func (b *ToolExecutionBridge) executeStepWithFallback(
	ctx context.Context,
	step *ExecutionStep,
	params map[string]interface{},
) (core.Result, error) {
	log := b.log
	// Try primary tool first.
	tools := make([]string, 1, 1+len(step.FallbackToolNames))
	tools[0] = step.ToolName
	tools = append(tools, step.FallbackToolNames...)

	var lastErr error
	for _, toolName := range tools {
		tool, exists := b.registry.Get(toolName)
		if !exists {
			continue
		}

		start := time.Now()
		result, err := tool.Execute(ctx, params)
		latency := time.Since(start)

		log.Info("tool_bridge: fallback attempt",
			"tool", toolName,
			"success", err == nil && result.Success,
			"latency_ms", latency.Milliseconds(),
		)

		if err == nil && result.Success {
			// Persist execution evidence for the successful attempt. The
			// multi-step path (executeMultiStep) goes through this method and
			// previously never reached executeStep — the only caller of
			// evidence.Save — so multi-step plans recorded no evidence at all.
			saveErr := b.evidence.Save(ctx, &ToolEvidence{
				ToolName:       toolName,
				CapabilityName: step.CapabilityName,
				Success:        true,
				Latency:        latency,
				Timestamp:      time.Now(),
			})
			if saveErr != nil {
				log.Warn("tool_bridge: failed to save evidence",
					"tool", toolName,
					"error", saveErr,
				)
			}
			return result, nil
		}
		if err != nil {
			lastErr = err
		} else if !result.Success && result.Error != "" {
			// result.Error is a string carried by the tool result, not an error chain;
			// errors.New carries it directly, avoiding a pointless fmt.Errorf("%s") wrapper.
			lastErr = errors.New(result.Error)
		}
	}

	return core.Result{}, fmt.Errorf("tool_bridge: all tools failed for step %q: %w",
		step.StepID, lastErr)
}

// topoSort returns step IDs in topological order (dependencies first).
// Returns an error if a cycle prevents complete ordering.
func topoSort(steps []ExecutionStep) ([]string, error) {
	// Build graph.
	inDegree := make(map[string]int)
	children := make(map[string][]string)
	for _, step := range steps {
		if _, ok := inDegree[step.StepID]; !ok {
			inDegree[step.StepID] = 0
		}
		for _, dep := range step.DependsOn {
			children[dep] = append(children[dep], step.StepID)
			inDegree[step.StepID]++
		}
	}

	// Kahn's algorithm.
	var order []string
	queue := make([]string, 0)
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, child := range children[id] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	// If not all steps were ordered, a cycle or missing dependency exists.
	if len(order) != len(steps) {
		return order, fmt.Errorf("tool_bridge: cycle or missing dependency in step dependencies: ordered %d of %d steps",
			len(order), len(steps))
	}

	return order, nil
}

// bindDependencyOutput maps one dependency result into the current step's expected input parameters.
func (b *ToolExecutionBridge) bindDependencyOutput(
	params map[string]interface{},
	step ExecutionStep,
	depStep ExecutionStep,
	depResult core.Result,
) error {
	value, ok := outputValueForCapability(depStep.CapabilityName, depResult.Data)
	if !ok {
		return nil
	}

	for _, targetName := range b.inputParamNamesForStep(step) {
		if !isEmptyParam(params[targetName]) {
			continue
		}
		params[targetName] = value
		return nil
	}

	return nil
}

// outputValueForCapability extracts the best output value from a dependency result.
func outputValueForCapability(capability string, data interface{}) (interface{}, bool) {
	if data == nil {
		return nil, false
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return data, true
	}

	for _, name := range outputParamNamesForCapability(capability) {
		if value, exists := dataMap[name]; exists && !isEmptyParam(value) {
			return value, true
		}
	}

	return nil, false
}

// inputParamNamesForStep returns preferred parameter names for dependency binding.
func (b *ToolExecutionBridge) inputParamNamesForStep(step ExecutionStep) []string {
	names := make([]string, 0, 8)

	if tool, ok := b.registry.Get(step.ToolName); ok {
		if schema := tool.Parameters(); schema != nil {
			names = append(names, schema.Required...)
		}
	}

	names = append(names, inputParamNamesForCapability(step.CapabilityName)...)
	for name := range step.Parameters {
		names = append(names, name)
	}

	return uniqueStrings(names)
}

// outputParamNamesForCapability returns likely output field names for a capability.
func outputParamNamesForCapability(capability string) []string {
	switch capability {
	case "PDFParsing", "TextExtraction":
		return []string{"text", "content"}
	case "Arithmetic", "Summation":
		return []string{"result", "value", "number"}
	case "Hashing":
		return []string{"output", "hash"}
	case "StringManipulation":
		return []string{"output", "text"}
	case "WebSearch":
		return []string{"results", "output"}
	case "JSONProcessing":
		return []string{"output", "data"}
	default:
		return []string{"output", "result", "data"}
	}
}

// inputParamNamesForCapability returns likely input field names for a capability.
func inputParamNamesForCapability(capability string) []string {
	switch capability {
	case "StringManipulation", "Hashing", "Base64":
		return []string{"input", "text", "content"}
	case "Regex":
		return []string{"text", "input", "content"}
	case "Embedding":
		return []string{"text", "input", "content"}
	case "JSONProcessing":
		return []string{"data", "input", "content"}
	case "Arithmetic", "Summation":
		return []string{"expression", "input", "value"}
	case "WebSearch":
		return []string{"query", "input"}
	case "HTTPRequest":
		return []string{"url", "input"}
	default:
		return []string{"input", "text", "content", "data"}
	}
}

// isEmptyParam reports whether a parameter should be filled by dependency binding.
func isEmptyParam(value interface{}) bool {
	if value == nil {
		return true
	}
	if s, ok := value.(string); ok {
		return s == ""
	}
	return false
}

// uniqueStrings removes duplicates while preserving order.
func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
