package mutation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"
)

// LLMClient defines the interface for LLM-based hint generation.
// Implementations can wrap service-layer LLM clients or provide mock implementations
// for testing. This interface is intentionally minimal to avoid coupling to any
// specific LLM package.
type LLMClient interface {
	// Generate sends a prompt to the LLM and returns the response text.
	Generate(ctx context.Context, prompt string) (string, error)
}

// LLMHintProviderConfig configures the LLM-based hint provider.
type LLMHintProviderConfig struct {
	// PromptTemplate is the Go template for generating the LLM hint request.
	// Available template variables: {{.TaskType}}, {{.RecentOutcomes}}.
	// Default: DefaultHintPromptTemplate
	PromptTemplate string

	// MaxHistory is the maximum number of recent outcomes to include in the prompt.
	// Default: 10
	MaxHistory int

	// DefaultConfidence is the confidence value assigned to LLM-generated hints
	// when the response doesn't specify one. Range [0.0, 1.0]. Default: 0.6.
	DefaultConfidence float64
}

// DefaultHintPromptTemplate is the default prompt template for hint generation.
// It asks the LLM to analyze recent strategy outcomes and suggest improvements.
const DefaultHintPromptTemplate = `You are an evolution strategy advisor analyzing agent deployment outcomes. Based on recent strategy results, suggest improvements.

Task Type: {{.TaskType}}

Recent outcomes (most recent first):
{{range .RecentOutcomes}}- Strategy {{.StrategyID}}: success={{.Success}}, score={{.Score}}, mutation={{.MutationType}}
{{end}}

Provide up to 3 evolution hints in JSON array format. Each hint should include:
- "problem": what went wrong or can be improved
- "solution": the recommended approach
- "constraints": any constraints to consider (optional array)
- "confidence": your confidence 0.0-1.0

Return ONLY valid JSON. No markdown, no explanation.
`

func defaultLLMHintProviderConfig() LLMHintProviderConfig {
	return LLMHintProviderConfig{
		PromptTemplate:    DefaultHintPromptTemplate,
		MaxHistory:        10,
		DefaultConfidence: 0.6,
	}
}

// LLMHintProvider implements HintProvider using an LLM client to generate
// evolution hints from recent strategy outcomes. It stores a sliding window
// of outcomes and queries the LLM to synthesize actionable hints.
//
// All LLM errors are handled gracefully — failed generations return empty
// hint slices rather than propagating errors — ensuring the mutation pipeline
// degrades gracefully when the LLM is unavailable.
type LLMHintProvider struct {
	client LLMClient
	config LLMHintProviderConfig

	mu       sync.RWMutex
	outcomes []StrategyOutcome
}

// NewLLMHintProvider creates a new LLM-based hint provider.
// Returns an error if client is nil. Config fields are optional; zero values
// fall back to defaults.
func NewLLMHintProvider(client LLMClient, cfg *LLMHintProviderConfig) (*LLMHintProvider, error) {
	if client == nil {
		return nil, errors.New("LLM client is required")
	}

	config := defaultLLMHintProviderConfig()
	if cfg != nil {
		if cfg.PromptTemplate != "" {
			config.PromptTemplate = cfg.PromptTemplate
		}
		if cfg.MaxHistory > 0 {
			config.MaxHistory = cfg.MaxHistory
		}
		if cfg.DefaultConfidence > 0 {
			config.DefaultConfidence = cfg.DefaultConfidence
		}
	}

	return &LLMHintProvider{
		client:   client,
		config:   config,
		outcomes: make([]StrategyOutcome, 0, config.MaxHistory),
	}, nil
}

// HintsForTask returns evolution hints by querying the LLM with recent
// strategy outcomes relevant to the given task type. Returns an empty slice
// with nil error on any failure (LLM error, parse error, no outcomes) to
// ensure graceful degradation.
func (p *LLMHintProvider) HintsForTask(ctx context.Context, taskType string, limit int) ([]EvolutionHint, error) {
	p.mu.RLock()
	recent := p.recentOutcomes()
	p.mu.RUnlock()

	if len(recent) == 0 {
		return []EvolutionHint{}, nil
	}

	prompt, err := p.buildPrompt(taskType, recent)
	if err != nil {
		slog.WarnContext(ctx, "[LLMHintProvider] Failed to build prompt", "error", err)
		return []EvolutionHint{}, nil
	}

	resp, err := p.client.Generate(ctx, prompt)
	if err != nil {
		slog.WarnContext(ctx, "[LLMHintProvider] LLM generation failed", "error", err)
		return []EvolutionHint{}, nil
	}

	hints, err := p.parseResponse(resp, limit)
	if err != nil {
		slog.WarnContext(ctx, "[LLMHintProvider] Failed to parse LLM response",
			"error", err, "response_length", len(resp))
		return []EvolutionHint{}, nil
	}

	return hints, nil
}

// RecordStrategyOutcome stores a strategy outcome for future hint generation.
// Maintains a sliding window of the most recent outcomes up to MaxHistory.
func (p *LLMHintProvider) RecordStrategyOutcome(ctx context.Context, outcome StrategyOutcome) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if outcome.Timestamp.IsZero() {
		outcome.Timestamp = time.Now()
	}

	p.outcomes = append(p.outcomes, outcome)
	if len(p.outcomes) > p.config.MaxHistory {
		p.outcomes = p.outcomes[len(p.outcomes)-p.config.MaxHistory:]
	}

	slog.DebugContext(ctx, "[LLMHintProvider] Recorded strategy outcome",
		"strategy_id", outcome.StrategyID,
		"success", outcome.Success,
		"score", outcome.Score,
		"history_size", len(p.outcomes))

	return nil
}

// Outcomes returns a copy of all stored outcomes (for testing/inspection).
func (p *LLMHintProvider) Outcomes() []StrategyOutcome {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]StrategyOutcome, len(p.outcomes))
	copy(result, p.outcomes)
	return result
}

// recentOutcomes returns the most recent outcomes in reverse chronological
// order. Must be called with at least read lock held.
func (p *LLMHintProvider) recentOutcomes() []StrategyOutcome {
	n := len(p.outcomes)
	if n == 0 {
		return nil
	}
	count := n
	if count > p.config.MaxHistory {
		count = p.config.MaxHistory
	}
	result := make([]StrategyOutcome, count)
	for i := 0; i < count; i++ {
		result[i] = p.outcomes[n-1-i]
	}
	return result
}

// promptData holds template variables for the hint prompt.
type promptData struct {
	TaskType       string
	RecentOutcomes []StrategyOutcome
}

// buildPrompt constructs the LLM prompt using the configured Go template.
func (p *LLMHintProvider) buildPrompt(taskType string, outcomes []StrategyOutcome) (string, error) {
	tmpl, err := template.New("hint").Parse(p.config.PromptTemplate)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}

	data := promptData{
		TaskType:       taskType,
		RecentOutcomes: outcomes,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}

	return buf.String(), nil
}

// ExtractJSONBracket finds the outermost JSON object or array in a string.
// It handles, in order:
//  1. Content inside a markdown code fence (```json ... ``` or ``` ... ```)
//  2. The first outermost bracket pair in any surrounding text
//
// Uses a bracket-depth counter with string-skipping to correctly match
// nested structures and brackets inside string literals.
// Returns empty string if no valid JSON is found.
func ExtractJSONBracket(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// Try extracting from markdown code fences first.
	if idx := strings.Index(s, "```"); idx >= 0 {
		start := idx + 3
		if nl := strings.Index(s[start:], "\n"); nl >= 0 {
			start += nl + 1
		}
		end := strings.LastIndex(s, "```")
		var inner string
		if end > start {
			inner = strings.TrimSpace(s[start:end])
		} else {
			inner = strings.TrimSpace(s[start:])
		}
		if extracted := extractOutermostBracket(inner); extracted != "" {
			return extracted
		}
		return inner
	}

	if extracted := extractOutermostBracket(s); extracted != "" {
		return extracted
	}
	return ""
}

// extractOutermostBracket finds the first outermost [..] or {..} pair using
// a bracket-depth counter with string-skipping for correct nesting handling.
func extractOutermostBracket(s string) string {
	// Find whichever open bracket comes first.
	braceIdx := strings.IndexByte(s, '{')
	bracketIdx := strings.IndexByte(s, '[')

	openIdx := -1
	var open, close byte
	if braceIdx >= 0 && (bracketIdx < 0 || braceIdx < bracketIdx) {
		open, close = '{', '}'
		openIdx = braceIdx
	} else if bracketIdx >= 0 {
		open, close = '[', ']'
		openIdx = bracketIdx
	}
	if openIdx < 0 {
		return ""
	}

	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[openIdx : i+1]
			}
		case '"':
			// Skip string contents to avoid counting brackets inside strings.
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == '"' {
					break
				}
				i++
			}
		}
	}
	return ""
}

// llmHintJSON is the JSON structure for LLM-generated hints.
type llmHintJSON struct {
	Problem     string   `json:"problem"`
	Solution    string   `json:"solution"`
	Constraints []string `json:"constraints,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
}

// parseResponse parses the LLM JSON response into EvolutionHint slice.
// Handles markdown code fences, explanatory text around the JSON, and
// plain whitespace — any format an LLM might produce despite instructions.
func (p *LLMHintProvider) parseResponse(resp string, limit int) ([]EvolutionHint, error) {
	jsonStr := ExtractJSONBracket(resp)
	if jsonStr == "" {
		return nil, errors.New("no JSON array found in response")
	}

	var hints []llmHintJSON
	if err := json.Unmarshal([]byte(jsonStr), &hints); err != nil {
		return nil, fmt.Errorf("unmarshal hints: %w", err)
	}

	if len(hints) > limit {
		hints = hints[:limit]
	}

	result := make([]EvolutionHint, len(hints))
	for i, h := range hints {
		confidence := h.Confidence
		if confidence <= 0 {
			confidence = p.config.DefaultConfidence
		}
		if confidence > 1.0 {
			confidence = 1.0
		}

		result[i] = EvolutionHint{
			ID:          fmt.Sprintf("llm-hint-%d", time.Now().UnixNano()+int64(i)),
			TaskType:    "", // inferred from context, not returned by LLM
			Problem:     h.Problem,
			Solution:    h.Solution,
			Constraints: h.Constraints,
			Confidence:  confidence,
		}
	}

	return result, nil
}
