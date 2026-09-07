// Package evolution provides production guardrails for the autonomous
// evolution system. These guardrails detect dangerous conditions and
// trigger protective actions before they cause harm.
package evolution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// GuardrailErrorCode is a machine-readable identifier for guardrail events.
type GuardrailErrorCode string

const (
	// ErrCodeUnevaluatedPopulation indicates a majority of the population is unevaluated.
	ErrCodeUnevaluatedPopulation GuardrailErrorCode = "EVAL_UNEVALUATED_POPULATION"
	// ErrCodeStagnation indicates no improvement for too many generations.
	ErrCodeStagnation GuardrailErrorCode = "EVAL_STAGNATION"
	// ErrCodeBaselineRegression indicates the best score regressed below baseline.
	ErrCodeBaselineRegression GuardrailErrorCode = "EVAL_BASELINE_REGRESSION"
	// ErrCodeLineageConcentration indicates a single lineage dominates the population.
	ErrCodeLineageConcentration GuardrailErrorCode = "EVAL_LINEAGE_CONCENTRATION"
	// ErrCodeScoreDecline indicates a significant score decline.
	ErrCodeScoreDecline GuardrailErrorCode = "EVAL_SCORE_DECLINE"
	// ErrCodeDiversityCollapse indicates critically low population diversity.
	ErrCodeDiversityCollapse GuardrailErrorCode = "EVAL_DIVERSITY_COLLAPSE"
	// ErrCodeInvalidToolSet indicates an evolved tool whitelist violates the
	// upper bound or would leave the agent with zero enabled tools (C6). The
	// former wastes the tool budget; the latter is the exact dead-end the
	// zero-intersection fallback guard (chat_cognition.go / sub/executor.go)
	// prevents at runtime — the guardrail catches it at selection time, before
	// a bad mutation is promoted.
	ErrCodeInvalidToolSet GuardrailErrorCode = "EVAL_INVALID_TOOL_SET"
)

// GuardrailError wraps an error code with metadata for automated handling.
type GuardrailError struct {
	Code       GuardrailErrorCode
	Message    string
	Generation int
	Score      float64
	Threshold  float64
}

// Error returns a formatted string describing the guardrail error.
//
// Returns:
//   - string: formatted error message including code, message, generation, score, and threshold.
func (e *GuardrailError) Error() string {
	return fmt.Sprintf("[%s] %s (gen=%d, score=%.2f, threshold=%.2f)",
		e.Code, e.Message, e.Generation, e.Score, e.Threshold)
}

// GuardrailLevel indicates the severity of a guardrail trigger.
type GuardrailLevel int

const (
	// GuardrailInfo is informational; no action required.
	GuardrailInfo GuardrailLevel = iota + 1
	// GuardrailWarning indicates a concerning condition that should be monitored.
	GuardrailWarning
	// GuardrailCritical requires immediate intervention (e.g., stop evolution).
	GuardrailCritical
)

// GuardrailEvent records a guardrail trigger with context.
type GuardrailEvent struct {
	// Level is the severity level.
	Level GuardrailLevel
	// Rule is the name of the guardrail rule that triggered.
	Rule string
	// Message describes what happened.
	Message string
	// ErrorCode is the machine-readable error code for automated handling.
	ErrorCode GuardrailErrorCode
	// Score is the relevant score at the time of the event (e.g., best score).
	Score float64
	// Generation when this event occurred.
	Generation int
	// Timestamp when this event occurred.
	Timestamp time.Time
	// SuggestedAction is the recommended remediation.
	SuggestedAction string
}

// GuardrailResult is the outcome of running all guardrails.
type GuardrailResult struct {
	// ShouldStop indicates evolution should halt immediately.
	ShouldStop bool
	// Events lists all triggered guardrails (may include non-critical ones).
	Events []GuardrailEvent
}

// GuardrailEventHandler is called when a guardrail event fires.
// Implementations can record metrics, send alerts, or trigger other actions.
type GuardrailEventHandler func(event GuardrailEvent)

// EvolutionGuardrails runs safety checks before and after each evolution cycle.
type EvolutionGuardrails struct {
	mu sync.RWMutex

	// BaselineScore is the score to beat; strategies below this are regressions.
	BaselineScore float64

	// MaxStagnantGenerations triggers warning after this many gens without improvement.
	MaxStagnantGenerations int

	// StagnantCount counts consecutive generations without improvement.
	stagnantCount int

	// bestBySource tracks the best score ever seen per evolution path
	// (e.g. "dream_cycle" feeds winRate in [0,1], "genome" feeds raw
	// BestScore in another scale). Keeping them separate prevents one path's
	// scale from polluting the other's baseline-regression threshold.
	bestBySource map[string]float64

	// LastImprovementGeneration records which generation last saw improvement.
	lastImprovementGen int

	// MaxLineageShare is the maximum allowed share for a single lineage (0-1, 0=disabled).
	MaxLineageShare float64

	// MaxToolsEnabled is the upper bound on the size of an evolved tool
	// whitelist (C6). 0 disables the upper-bound check (all tools allowed, the
	// pre-C6 behavior). A positive value means a Params["tools"] set larger than
	// this is rejected at selection time — a spawned tool set that exceeds the
	// budget both wastes capacity and may surface tools the deployment never
	// intended to expose.
	MaxToolsEnabled int

	// requireAnyTool, when true, rejects an evolved whitelist that enables ZERO
	// tools — the agent would otherwise be handed an empty tool list (the
	// zero-intersection dead-end the runtime guard falls back from). Guarded by
	// the manager so a legitimate "text-only" strategy is not rejected by
	// default.
	requireAnyTool bool

	// knownTools is the vocabulary of ACTUALLY REGISTERED tool names (§8.6-3).
	// Nil/empty disables the check (no vocabulary supplied = cannot judge).
	// Without it the guardrail only bounds the SIZE of a whitelist: a mutation
	// that produces three names none of which exist passes the bound, then hits
	// the runtime zero-intersection fallback and is silently promoted to "all
	// tools enabled" — the exact opposite of what the whitelist asked for.
	// Comparing against the registry catches that at selection time.
	knownTools map[string]bool

	// Events stores historical guardrail events.
	events []GuardrailEvent

	// MaxEvents limits stored events (0=unlimited).
	MaxEvents int

	// eventHandler is called on each guardrail event (optional).
	eventHandler GuardrailEventHandler
}

// GuardrailOption configures EvolutionGuardrails.
type GuardrailOption func(*EvolutionGuardrails)

// WithBaselineScore sets the minimum acceptable strategy score.
func WithBaselineScore(score float64) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.BaselineScore = score
	}
}

// WithMaxStagnantGenerations sets the stagnation detection threshold.
func WithMaxStagnantGenerations(n int) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.MaxStagnantGenerations = n
	}
}

// WithMaxLineageShare sets the maximum allowed lineage concentration.
func WithMaxLineageShare(share float64) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.MaxLineageShare = share
	}
}

// WithMaxToolsEnabled sets the upper bound on an evolved tool whitelist size
// (C6). A non-positive value disables the bound.
func WithMaxToolsEnabled(n int) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.MaxToolsEnabled = n
	}
}

// WithRequireAnyTool enables the "zero enabled tools is invalid" check (C6).
// Off by default so text-only strategies are not rejected; the deployment turns
// it on when it wants every strategy to advertise at least one tool.
func WithRequireAnyTool(enabled bool) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.requireAnyTool = enabled
	}
}

// WithKnownTools supplies the registered-tool vocabulary used to validate that
// an evolved whitelist names tools that actually exist (§8.6-3). An empty or nil
// set disables the check, preserving pre-existing behavior for deployments that
// cannot enumerate their registry at guardrail-construction time.
func WithKnownTools(names []string) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		if len(names) == 0 {
			g.knownTools = nil
			return
		}
		known := make(map[string]bool, len(names))
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" {
				known[n] = true
			}
		}
		if len(known) == 0 {
			g.knownTools = nil
			return
		}
		g.knownTools = known
	}
}

// WithGuardrailEventHandler sets a callback for guardrail events.
// The handler is invoked synchronously after each event is recorded.
func WithGuardrailEventHandler(handler GuardrailEventHandler) GuardrailOption {
	return func(g *EvolutionGuardrails) {
		g.eventHandler = handler
	}
}

// NewEvolutionGuardrails creates a new guardrail checker.
//
// Args:
//   - opts: configuration options for the guardrail checker
//
// Returns:
//   - *EvolutionGuardrails: configured guardrail instance
//   - error: always nil (reserved for future validation)
func NewEvolutionGuardrails(opts ...GuardrailOption) (*EvolutionGuardrails, error) {
	g := &EvolutionGuardrails{
		BaselineScore:          0,
		MaxStagnantGenerations: 10,
		MaxLineageShare:        0.8,
		MaxEvents:              1000,
		bestBySource:           make(map[string]float64),
	}
	for _, opt := range opts {
		opt(g)
	}
	return g, nil
}

// PreEvolveCheck runs guardrails BEFORE an evolution cycle.
//
// Checks:
//  1. All individuals evaluated guardrail — if >50% of population has Score==-1 (unevaluated), return Critical
//  2. Stagnation check — if stagnantCount >= MaxStagnantGenerations, return Warning
//
// Args:
//   - ctx: context for cancellation
//   - currentBest: current population's best score
//   - generation: current generation number
//   - totalPop: total population size
//   - unevaluatedCount: number of individuals with Score == -1
//
// Returns:
//   - *GuardrailResult: result containing any triggered guardrails and stop recommendation
func (g *EvolutionGuardrails) PreEvolveCheck(ctx context.Context, currentBest float64, generation int, totalPop, unevaluatedCount int) *GuardrailResult {
	result := &GuardrailResult{
		ShouldStop: false,
		Events:     []GuardrailEvent{},
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Check 1: Unevaluated population guardrail
	if totalPop > 0 {
		unevaluatedRatio := float64(unevaluatedCount) / float64(totalPop)
		if unevaluatedRatio > 0.5 {
			event := GuardrailEvent{
				Level:           GuardrailCritical,
				Rule:            "unevaluated_population",
				ErrorCode:       ErrCodeUnevaluatedPopulation,
				Message:         "majority population unevaluated",
				Score:           currentBest,
				Generation:      generation,
				Timestamp:       time.Now(),
				SuggestedAction: "evaluate all individuals before proceeding",
			}
			log.Warn("guardrail: critical - majority population unevaluated",
				"code", ErrCodeUnevaluatedPopulation,
				"score", currentBest,
				"generation", generation,
				"ratio", unevaluatedRatio,
				"total_pop", totalPop,
				"unevaluated", unevaluatedCount,
			)
			result.Events = append(result.Events, event)
			result.ShouldStop = true
			g.recordEventLocked(event)
		}
	}

	// Check 2: Stagnation guardrail
	if g.stagnantCount >= g.MaxStagnantGenerations && g.MaxStagnantGenerations > 0 {
		event := GuardrailEvent{
			Level:           GuardrailWarning,
			Rule:            "stagnation",
			ErrorCode:       ErrCodeStagnation,
			Message:         fmt.Sprintf("no improvement for %d generations", g.stagnantCount),
			Score:           currentBest,
			Generation:      generation,
			Timestamp:       time.Now(),
			SuggestedAction: "consider increasing mutation rate or introducing diversity",
		}
		log.Warn("guardrail: warning - stagnation detected",
			"code", ErrCodeStagnation,
			"score", currentBest,
			"generation", generation,
			"stagnant_count", g.stagnantCount,
			"threshold", g.MaxStagnantGenerations,
		)
		result.Events = append(result.Events, event)
		g.recordEventLocked(event)
	}

	return result
}

// PostEvolveCheck runs guardrails AFTER an evolution cycle.
//
// Checks:
//  1. Best regression — if new best < BaselineScore, return Critical ("strategy failed to beat baseline")
//  2. Improvement tracking — update stagnation counter
//  3. Lineage concentration — if top lineage > MaxLineageShare, return Warning
//
// Args:
//   - ctx: context for cancellation
//   - newBest: new population's best score after evolution
//   - generation: generation number
//   - lineageShares: map[lineageID]count (can be nil if unavailable)
//
// Returns:
//   - *GuardrailResult: result containing any triggered guardrails and stop recommendation
func (g *EvolutionGuardrails) PostEvolveCheck(ctx context.Context, newBest float64, generation int, lineageShares map[string]int) *GuardrailResult {
	return g.postEvolveCheckForSource(ctx, "", newBest, generation, lineageShares)
}

// PostEvolveCheckForSource runs guardrails after an evolution cycle for a
// specific evolution path. The source distinguishes score scales (dream_cycle
// winRate [0,1] vs genome BestScore), so baseline regression compares against
// the path's own best instead of a cross-scale value.
func (g *EvolutionGuardrails) PostEvolveCheckForSource(ctx context.Context, source string, newBest float64, generation int, lineageShares map[string]int) *GuardrailResult {
	return g.postEvolveCheckForSource(ctx, source, newBest, generation, lineageShares)
}

func (g *EvolutionGuardrails) postEvolveCheckForSource(ctx context.Context, source string, newBest float64, generation int, lineageShares map[string]int) *GuardrailResult {
	result := &GuardrailResult{
		ShouldStop: false,
		Events:     []GuardrailEvent{},
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	bestKnown := g.bestBySource[source]

	// Check 1: Baseline regression guardrail
	// Uses BaselineScore (if explicitly set) or the path's best as the threshold.
	baseline := g.BaselineScore
	if baseline <= 0 {
		baseline = bestKnown
	}
	if newBest < baseline && baseline > 0 {
		event := GuardrailEvent{
			Level:           GuardrailCritical,
			Rule:            "baseline_regression",
			ErrorCode:       ErrCodeBaselineRegression,
			Message:         "best score regressed below baseline",
			Score:           newBest,
			Generation:      generation,
			Timestamp:       time.Now(),
			SuggestedAction: "review recent changes and consider reverting to previous best strategy",
		}
		log.Warn("guardrail: critical - baseline regression",
			"code", ErrCodeBaselineRegression,
			"score", newBest,
			"generation", generation,
			"new_best", newBest,
			"baseline", g.BaselineScore,
			"threshold", g.BaselineScore,
		)
		result.Events = append(result.Events, event)
		result.ShouldStop = true
		g.recordEventLocked(event)
	}

	// Check 2: Improvement tracking and stagnation counter update
	if newBest > bestKnown {
		g.stagnantCount = 0
		g.bestBySource[source] = newBest
		g.lastImprovementGen = generation
		log.Info("guardrail: improvement detected",
			"generation", generation,
			"new_best", newBest,
			"previous_best", bestKnown,
		)
	} else {
		g.stagnantCount++
		log.Info("guardrail: no improvement",
			"generation", generation,
			"new_best", newBest,
			"best_known", bestKnown,
			"stagnant_count", g.stagnantCount,
		)
	}

	// Check 3: Lineage concentration guardrail
	if lineageShares != nil && g.MaxLineageShare > 0 {
		total := 0
		for _, count := range lineageShares {
			total += count
		}
		if total > 0 {
			maxCount := 0
			for _, count := range lineageShares {
				if count > maxCount {
					maxCount = count
				}
			}
			maxShare := float64(maxCount) / float64(total)
			if maxShare > g.MaxLineageShare {
				event := GuardrailEvent{
					Level:           GuardrailWarning,
					Rule:            "lineage_concentration",
					ErrorCode:       ErrCodeLineageConcentration,
					Message:         fmt.Sprintf("lineage concentration %.2f exceeds threshold %.2f", maxShare, g.MaxLineageShare),
					Score:           newBest,
					Generation:      generation,
					Timestamp:       time.Now(),
					SuggestedAction: "increase selection pressure or introduce external diversity",
				}
				log.Warn("guardrail: warning - lineage concentration",
					"code", ErrCodeLineageConcentration,
					"score", newBest,
					"generation", generation,
					"max_share", maxShare,
					"threshold", g.MaxLineageShare,
				)
				result.Events = append(result.Events, event)
				g.recordEventLocked(event)
			}
		}
	}

	return result
}

// RecordEvent stores a guardrail event for later review.
//
// Args:
//   - event: the guardrail event to record
func (g *EvolutionGuardrails) RecordEvent(event GuardrailEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recordEventLocked(event)
}

// ValidateToolSet checks an evolved tool whitelist against the C6 guards: an
// upper bound on set size and (optionally) the "at least one tool" invariant.
// It does NOT mutate any state — it reports whether a candidate's chosen tool
// set may move into selection/promotion. Called at selection time, before a bad
// mutation is promoted, complementing the runtime zero-intersection fallback.
//
// Args:
//   - generation: the generation number for event attribution.
//   - tools: the parsed tool names the candidate whitelist would enable.
//
// Returns a GuardrailResult; ShouldStop=true means the proposed set is invalid.
func (g *EvolutionGuardrails) ValidateToolSet(generation int, tools []string) *GuardrailResult {
	result := &GuardrailResult{ShouldStop: false, Events: []GuardrailEvent{}}

	g.mu.RLock()
	maxTools := g.MaxToolsEnabled
	requireAny := g.requireAnyTool
	known := g.knownTools
	g.mu.RUnlock()

	if maxTools > 0 && len(tools) > maxTools {
		event := GuardrailEvent{
			Level:           GuardrailWarning,
			Rule:            "tool_set_upper_bound",
			ErrorCode:       ErrCodeInvalidToolSet,
			Message:         fmt.Sprintf("evolved tool whitelist (%d tools) exceeds upper bound %d", len(tools), maxTools),
			Score:           float64(len(tools)),
			Generation:      generation,
			Timestamp:       time.Now(),
			SuggestedAction: "cap the tool whitelist during mutation",
		}
		log.Warn("guardrail: tool set exceeds upper bound",
			"code", ErrCodeInvalidToolSet,
			"generation", generation,
			"tool_count", len(tools),
			"max_tools", maxTools,
		)
		result.Events = append(result.Events, event)
		result.ShouldStop = true
		g.RecordEvent(event)
	}

	if requireAny && len(tools) == 0 {
		event := GuardrailEvent{
			Level:           GuardrailCritical,
			Rule:            "tool_set_empty",
			ErrorCode:       ErrCodeInvalidToolSet,
			Message:         "evolved strategy enables zero tools",
			Generation:      generation,
			Timestamp:       time.Now(),
			SuggestedAction: "ensure the mutated tool whitelist keeps at least one tool enabled",
		}
		log.Warn("guardrail: evolved strategy enables zero tools",
			"code", ErrCodeInvalidToolSet,
			"generation", generation,
		)
		result.Events = append(result.Events, event)
		result.ShouldStop = true
		g.RecordEvent(event)
	}

	// §8.6-3 vocabulary alignment: every named tool must exist in the registry.
	// A whitelist of names that do not resolve is not a narrower tool set — at
	// runtime it intersects to zero and the executors fall back to the FULL set,
	// so the strategy silently becomes the broadest possible one. Reject it here
	// instead of discovering it as an unexplained capability expansion.
	if len(known) > 0 && len(tools) > 0 {
		var unknown []string
		for _, name := range tools {
			if !known[name] {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			event := GuardrailEvent{
				Level:           GuardrailCritical,
				Rule:            "tool_set_unknown_name",
				ErrorCode:       ErrCodeInvalidToolSet,
				Message:         fmt.Sprintf("evolved tool whitelist names %d unregistered tool(s): %s", len(unknown), strings.Join(unknown, ",")),
				Score:           float64(len(unknown)),
				Generation:      generation,
				Timestamp:       time.Now(),
				SuggestedAction: "restrict mutation to the registered tool vocabulary",
			}
			log.Warn("guardrail: tool whitelist names unregistered tools",
				"code", ErrCodeInvalidToolSet,
				"generation", generation,
				"unknown", unknown,
				"known_count", len(known),
			)
			result.Events = append(result.Events, event)
			result.ShouldStop = true
			g.RecordEvent(event)
		}
	}

	return result
}

// recordEventLocked stores an event and invokes the handler if set.
// Caller must hold lock.
func (g *EvolutionGuardrails) recordEventLocked(event GuardrailEvent) {
	g.events = append(g.events, event)
	// Enforce MaxEvents limit.
	if g.MaxEvents > 0 && len(g.events) > g.MaxEvents {
		g.events = g.events[len(g.events)-g.MaxEvents:]
	}
	// Invoke post-action handler if configured.
	if g.eventHandler != nil {
		g.eventHandler(event)
	}
}

// Events returns all recorded events (copy).
//
// Returns:
//   - []GuardrailEvent: copy of all stored events
func (g *EvolutionGuardrails) Events() []GuardrailEvent {
	g.mu.RLock()
	defer g.mu.RUnlock()

	eventsCopy := make([]GuardrailEvent, len(g.events))
	copy(eventsCopy, g.events)
	return eventsCopy
}

// ToGuardrailError converts a guardrail event to a machine-readable error
// for automated retry, alert, downgrade, or rollback decisions.
//
// Returns nil if the event has no error code or if the event's Rule is
// unrecognized. When non-nil, the returned *GuardrailError implements the
// error interface and can be used in type-switch or errors.Is logic.
//
// Args:
//   - event: the guardrail event to convert
//
// Returns:
//   - *GuardrailError: machine-readable error with score and threshold metadata, or nil
func (g *EvolutionGuardrails) ToGuardrailError(event GuardrailEvent) *GuardrailError {
	// Find the threshold based on event context.
	var threshold float64
	switch event.Rule {
	case "unevaluated_population":
		threshold = 0.5 // >50% unevaluated
	case "stagnation":
		threshold = float64(g.MaxStagnantGenerations)
	case "baseline_regression":
		threshold = g.BaselineScore
	case "lineage_concentration":
		threshold = g.MaxLineageShare
	default:
		return nil
	}

	return &GuardrailError{
		Code:       event.ErrorCode,
		Message:    event.Message,
		Generation: event.Generation,
		Score:      event.Score,
		Threshold:  threshold,
	}
}

// Reset clears stagnation counters and events.
func (g *EvolutionGuardrails) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.stagnantCount = 0
	g.bestBySource = make(map[string]float64)
	g.lastImprovementGen = 0
	g.events = []GuardrailEvent{}
}

// StagnantCount returns the current stagnation counter.
//
// Returns:
//   - int: number of consecutive generations without improvement
func (g *EvolutionGuardrails) StagnantCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.stagnantCount
}
