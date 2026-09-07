// Package agents defines shared runtime contracts for live agents.
package agents

import (
	"context"
	"strconv"
	"strings"
)

// ActiveStrategy is the runtime view of the evolution strategy currently
// deployed to live agents. It carries an optional prompt-template override
// and per-call LLM parameter overrides (temperature, max_tokens, top_k).
type ActiveStrategy struct {
	// ID identifies the source strategy (for tracing/logging).
	ID string
	// Prompt optionally overrides the agent's default prompt template.
	Prompt string
	// Params carries LLM parameter overrides applied on each LLM call.
	Params map[string]any
}

// StrategySource yields the currently-active strategy so live agents can be
// steered at runtime (prompt + LLM params). It is intentionally decoupled
// from the evolution engine internals; adapters in the wiring layer convert
// engine-specific stores into this interface.
type StrategySource interface {
	// GetActiveStrategy returns the active strategy, or nil if none is set.
	GetActiveStrategy(ctx context.Context) (*ActiveStrategy, error)
}

// ParamKeyTools is the Params key that carries the tool whitelist. The value
// is a comma-separated string of tool names (e.g. "web_search,calculator").
// An empty or missing value means "no filter" — all registered tools are
// advertised to the LLM (zero-value usable, code_rules_v2 §5.4).
const ParamKeyTools = "tools"

// ParamKeyBudget and ParamKeyPrior are the node-level ToolStep attributes a
// ProjectStep-generated task payload may carry (Y1 方案C C5). budget caps how
// many times a tool step may run per session (enforced at schema-gating time);
// prior is a hint injected into the prompt that biases (but never disables) the
// tool. Both ride Step.Metadata → PlanStep.Payload (planprojection.ProjectStep),
// exactly like ParamKeyTools.
const (
	ParamKeyBudget = "budget"
	ParamKeyPrior  = "prior"
)

// MergeNodeParams overlays node-level task payload attributes onto a strategy's
// assembled params, with NODE OVER GLOBAL priority (§8.5). A ToolStep node's
// Metadata is projected into task.Payload by planprojection.ProjectStep; this
// makes those node attributes win over the global active strategy's Params so a
// per-node choice is actually observable by the executor. The node keys
// (tools/budget/prior) are promoted verbatim from the payload — the executor
// decides how to interpret each (schema filter / budget gate / prompt hint).
func MergeNodeParams(params map[string]any, payload map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	if payload == nil {
		return params
	}
	for _, key := range []string{ParamKeyTools, ParamKeyBudget, ParamKeyPrior} {
		if v, ok := payload[key]; ok {
			params[key] = v
		}
	}
	return params
}

// ToolNamesFromParams is the SINGLE parser for the Params["tools"] whitelist
// string. It returns the DEDUPLICATED, trimmed, non-empty tool names in
// first-occurrence order, or nil when no whitelist is configured (missing /
// empty / non-string value = "all tools allowed").
//
// Every consumer of the whitelist must go through this function. It exists as
// its own export because there are two shapes of consumer and they used to
// each re-implement the split: the execution bodies need a lookup set
// (ToolWhitelistFromParams, below) while the selection-time guardrail needs a
// countable slice (ares_evolution's tool-set guard bounds len()). Two copies of
// "split on comma and trim" drift in exactly the ways mutation produces —
// trailing separators ("a,b,") and repeated names ("a,a") — and then the
// guardrail would bound a count of tools the executor never enables, jailing a
// candidate that is actually within budget. len(names) == len(set), always.
func ToolNamesFromParams(params map[string]any) []string {
	if len(params) == 0 {
		return nil
	}
	raw, ok := params[ParamKeyTools].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var names []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// ToolBudgetFromParams parses Params["budget"] into a per-tool call cap for the
// current session. It returns 0 when the key is missing, malformed, or
// non-positive — meaning UNLIMITED (zero-value usable, code_rules_v2 §5.4), so
// a strategy that never sets a budget behaves exactly as before this gate
// existed.
//
// The value rides Step.Metadata → PlanStep.Payload as JSON, so it may arrive as
// a string ("50"), an int, or a float64. All three are accepted; anything else
// is treated as "no budget" rather than an error, because a malformed mutation
// must not break execution — it must only fail to restrict it.
func ToolBudgetFromParams(params map[string]any) int {
	if len(params) == 0 {
		return 0
	}
	switch v := params[ParamKeyBudget].(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v >= 1 {
			return int(v)
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// ToolAllowedByBudget reports whether a tool may still be advertised to the LLM
// given how many times it has already run this session and the node budget.
//
// This is the read side of the §11 C5 acceptance card ("budget 用尽后 schema
// 过滤"): the gate is applied where the tool SCHEMAS are assembled, not at
// CallTool time, for the same reason the whitelist is — letting the model see an
// exhausted tool and then rejecting the call wastes a round and pollutes the
// not_found metric (code_rules_v2 §5.3).
//
// budget <= 0 means unlimited, so this returns true.
func ToolAllowedByBudget(name string, uses map[string]int, budget int) bool {
	if budget <= 0 {
		return true
	}
	return uses[name] < budget
}

// PriorHintFromParams returns the trimmed Params["prior"] hint, or "" when
// absent. prior is advisory ONLY: it biases the model through the prompt and
// must never remove a tool from the advertised set — that is the whitelist's and
// the budget's job. Keeping the two effects separate is what makes a bad prior
// harmless.
func PriorHintFromParams(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	s, ok := params[ParamKeyPrior].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// ApplyPriorHint prepends the node's prior hint to a rendered prompt. Returns
// the prompt unchanged when the hint is empty, so the non-evolved path produces
// byte-identical prompts.
func ApplyPriorHint(prompt, hint string) string {
	if hint == "" {
		return prompt
	}
	return "Tool preference hint (advisory, does not restrict which tools you may call): " +
		hint + "\n\n" + prompt
}

// ToolWhitelistFromParams extracts the tool whitelist from a strategy's
// Params map as a lookup set. Returns nil when no whitelist is configured
// (meaning "all tools allowed"). Names are parsed by ToolNamesFromParams, so
// the set and the guardrail's count can never disagree.
//
// This is the Y.3-ACT wiring point: the execution bodies (planner cognition,
// agentloop engine) call this on the params they received to filter which
// ToolSchemas reach the LLM. Filtering happens BEFORE the LLM sees the tool list, not at CallTool
// time — letting the model see a tool and then rejecting the call wastes a
// round and pollutes the not_found metric (code_rules_v2 §5.3).
func ToolWhitelistFromParams(params map[string]any) map[string]bool {
	names := ToolNamesFromParams(params)
	if len(names) == 0 {
		return nil
	}
	whitelist := make(map[string]bool, len(names))
	for _, name := range names {
		whitelist[name] = true
	}
	return whitelist
}
