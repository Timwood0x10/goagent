// Package evolution provides LLM adapters for the runtime evolution system.
//
// LLM is a participant in evolution, not a leader. This adapter converts
// natural-language LLM suggestions into structured PatchProposals that the
// Coordinator can evaluate alongside GA, Chaos, AKF, and Human sources.
package evolution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

const llmSource = "llm"

// LLMAdapter converts natural-language LLM suggestions into PatchProposals.
// It is one of many PatchProposal producers — no special status in the Coordinator.
type LLMAdapter struct{}

// NewLLMAdapter creates a new LLM adapter.
func NewLLMAdapter() *LLMAdapter {
	return &LLMAdapter{}
}

// ParseResult holds a parsed PatchProposal and any parse warnings.
type ParseResult struct {
	Proposal coordinator.PatchProposal
	Warning  string // non-fatal parse issue
}

// Parse converts an LLM suggestion string into zero or more PatchProposals.
// Returns an error only if the suggestion is structurally unparseable.
//
// Supported formats:
//
//	"insert node <id> after <dep>"     → PatchInsertNode
//	"remove node <id>"                 → PatchRemoveNode
//	"replace node <id> with <type>"    → PatchReplaceNode
//	"add edge <from> -> <to>"          → PatchAddEdge
//	"remove edge <from> -> <to>"       → PatchRemoveEdge
//	"change scheduler to <type>"       → PatchChangeScheduler
//	"change topk to <n>"               → PatchChangeBudget
//	"change reducer to <strategy>"     → PatchChangeReducer
//	"change planner to <strategy>"     → PatchChangePlanner
//	"change recovery to <strategy>"    → PatchChangeRecoveryStrategy
func (a *LLMAdapter) Parse(_ context.Context, suggestion string) ([]ParseResult, error) {
	if suggestion == "" {
		return nil, errors.New("llm: empty suggestion")
	}

	// Normalize whitespace: collapse multiple spaces, trim.
	text := strings.Join(strings.Fields(suggestion), " ")
	lower := strings.ToLower(text)

	// Try each pattern.
	switch {
	case strings.HasPrefix(lower, "insert node"):
		return a.parseInsertNode(text)

	case strings.HasPrefix(lower, "remove node"):
		return a.parseRemoveNode(text)

	case strings.HasPrefix(lower, "replace node"):
		return a.parseReplaceNode(text)

	case strings.HasPrefix(lower, "add edge"):
		return a.parseAddEdge(text)

	case strings.HasPrefix(lower, "remove edge"):
		return a.parseRemoveEdge(text)

	case strings.HasPrefix(lower, "change scheduler"):
		return a.parseChangeScheduler(text)

	case strings.HasPrefix(lower, "change topk"):
		return a.parseChangeBudget(text)

	case strings.HasPrefix(lower, "change reducer"):
		return a.parseChangeReducer(text)

	case strings.HasPrefix(lower, "change planner"):
		return a.parseChangePlanner(text)

	case strings.HasPrefix(lower, "change recovery"):
		return a.parseChangeRecovery(text)

	default:
		return nil, fmt.Errorf("llm: unrecognized suggestion format: %q", text)
	}
}

// ── Parsers ────────────────────────────────

func (a *LLMAdapter) parseInsertNode(text string) ([]ParseResult, error) {
	// "insert node <id> after <dep>"
	parts := strings.Fields(text)
	if len(parts) < 5 || parts[3] != "after" {
		return nil, errors.New("llm: insert node format: 'insert node <id> after <dep>'")
	}
	nodeID := parts[2]
	dep := parts[4]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchInsertNode,
				Target: nodeID,
				Value:  dep,
				Reason: fmt.Sprintf("llm suggested: insert %s after %s", nodeID, dep),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested inserting node %s", nodeID),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseRemoveNode(text string) ([]ParseResult, error) {
	// "remove node <id>"
	parts := strings.Fields(text)
	if len(parts) < 3 {
		return nil, errors.New("llm: remove node format: 'remove node <id>'")
	}
	nodeID := parts[2]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchRemoveNode,
				Target: nodeID,
				Reason: fmt.Sprintf("llm suggested: remove %s", nodeID),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested removing node %s", nodeID),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseReplaceNode(text string) ([]ParseResult, error) {
	// "replace node <id> with <agentType>"
	parts := strings.Fields(text)
	if len(parts) < 5 || parts[3] != "with" {
		return nil, errors.New("llm: replace node format: 'replace node <id> with <agent_type>'")
	}
	nodeID := parts[2]
	agentType := parts[4]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchReplaceNode,
				Target: nodeID,
				Value:  agentType,
				Reason: fmt.Sprintf("llm suggested: replace %s with %s", nodeID, agentType),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested replacing node %s with %s", nodeID, agentType),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseAddEdge(text string) ([]ParseResult, error) {
	// "add edge <from> -> <to>"
	parts := strings.Fields(text)
	if len(parts) < 5 || parts[3] != "->" {
		return nil, errors.New("llm: add edge format: 'add edge <from> -> <to>'")
	}
	from := parts[2]
	to := parts[4]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchAddEdge,
				Target: from,
				Value:  to,
				Reason: fmt.Sprintf("llm suggested: edge %s -> %s", from, to),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested adding edge %s→%s", from, to),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseRemoveEdge(text string) ([]ParseResult, error) {
	// "remove edge <from> -> <to>"
	parts := strings.Fields(text)
	if len(parts) < 5 || parts[3] != "->" {
		return nil, errors.New("llm: remove edge format: 'remove edge <from> -> <to>'")
	}
	from := parts[2]
	to := parts[4]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchRemoveEdge,
				Target: from,
				Value:  to,
				Reason: fmt.Sprintf("llm suggested: remove edge %s -> %s", from, to),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested removing edge %s→%s", from, to),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseChangeScheduler(text string) ([]ParseResult, error) {
	// "change scheduler to <type>"
	parts := strings.Fields(text)
	if len(parts) < 4 || parts[2] != "to" {
		return nil, errors.New("llm: change scheduler format: 'change scheduler to <type>'")
	}
	schedType := parts[3]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchChangeScheduler,
				Target: "graph.scheduler",
				Value:  schedType,
				Reason: fmt.Sprintf("llm suggested: scheduler -> %s", schedType),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested changing scheduler to %s", schedType),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseChangeBudget(text string) ([]ParseResult, error) {
	// "change topk to <n>"
	parts := strings.Fields(text)
	if len(parts) < 4 || parts[2] != "to" {
		return nil, errors.New("llm: change topk format: 'change topk to <n>'")
	}
	//nolint:gosec // not security-sensitive
	var n int
	if _, err := fmt.Sscanf(parts[3], "%d", &n); err != nil {
		return nil, fmt.Errorf("llm: invalid topk value %q: %w", parts[3], err)
	}

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchChangeBudget,
				Target: "knowledge.planner.max_results",
				Value:  n,
				Reason: fmt.Sprintf("llm suggested: topk -> %d", n),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested changing TopK to %d", n),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseChangeReducer(text string) ([]ParseResult, error) {
	// "change reducer to <strategy>"
	parts := strings.Fields(text)
	if len(parts) < 4 || parts[2] != "to" {
		return nil, errors.New("llm: change reducer format: 'change reducer to <strategy>'")
	}
	strategy := parts[3]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchChangeReducer,
				Target: "knowledge.planner.reducer",
				Value:  strategy,
				Reason: fmt.Sprintf("llm suggested: reducer -> %s", strategy),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested changing reducer to %s", strategy),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseChangePlanner(text string) ([]ParseResult, error) {
	// "change planner to <strategy>"
	parts := strings.Fields(text)
	if len(parts) < 4 || parts[2] != "to" {
		return nil, errors.New("llm: change planner format: 'change planner to <strategy>'")
	}
	strategy := parts[3]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchChangePlanner,
				Target: "knowledge.planner.strategy",
				Value:  strategy,
				Reason: fmt.Sprintf("llm suggested: planner -> %s", strategy),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested changing planner to %s", strategy),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}

func (a *LLMAdapter) parseChangeRecovery(text string) ([]ParseResult, error) {
	// "change recovery to <strategy>"
	parts := strings.Fields(text)
	if len(parts) < 4 || parts[2] != "to" {
		return nil, errors.New("llm: change recovery format: 'change recovery to <strategy>'")
	}
	strategy := parts[3]

	return []ParseResult{{
		Proposal: coordinator.PatchProposal{
			Patch: patch.RuntimePatch{
				Type:   patch.PatchChangeRecoveryStrategy,
				Target: "recovery.strategy",
				Value:  strategy,
				Reason: fmt.Sprintf("llm suggested: recovery -> %s", strategy),
				Source: llmSource,
			},
			Source:    coordinator.SourceLLM,
			Reason:    fmt.Sprintf("LLM suggested changing recovery to %s", strategy),
			Priority:  4,
			Timestamp: time.Now(),
		},
	}}, nil
}
