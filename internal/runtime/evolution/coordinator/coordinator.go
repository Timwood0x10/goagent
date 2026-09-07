// Package coordinator provides the central decision-maker for runtime patches.
//
// Coordinator does NOT know where patches come from (GA, Chaos, LLM, Human, K8s Operator).
// Coordinator ONLY decides: Apply? Reject? Delay?
//
// Architecture:
//
//	Any Source → PatchProposal → Coordinator → Decision → Apply / Reject / Delay
package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// PatchSource identifies the origin of a patch proposal.
type PatchSource string

const (
	SourceGA        PatchSource = "genome"    // Genetic Algorithm
	SourceChaos     PatchSource = "chaos"     // Chaos Engineering
	SourceAKF       PatchSource = "akf"       // Knowledge Runtime
	SourceHuman     PatchSource = "human"     // Manual operator
	SourceLLM       PatchSource = "llm"       // LLM suggestion
	SourceK8s       PatchSource = "k8s"       // Kubernetes Operator
	SourceRule      PatchSource = "rule"      // Rule Engine
	SourceCandidate PatchSource = "candidate" // Verified evolution candidate
)

// PatchProposal is what the Coordinator receives.
// It wraps a RuntimePatch with metadata for the decision process.
type PatchProposal struct {
	Patch      patch.RuntimePatch `json:"patch"`
	Source     PatchSource        `json:"source"`
	Reason     string             `json:"reason"`   // why this patch was proposed
	Priority   int                `json:"priority"` // 1-10, higher = more urgent
	Fitness    float64            `json:"fitness"`  // GA fitness score (0-100), 0 = unknown
	Timestamp  time.Time          `json:"timestamp"`
	RetryCount int                `json:"retry_count"` // number of times this proposal was delayed and re-queued
}

// Decision is the Coordinator's output.
type Decision int

const (
	DecisionApply  Decision = iota // Apply the patch now
	DecisionReject                 // Reject the patch
	DecisionDelay                  // Revisit later (re-queued, bounded by retries)
	DecisionDrop                   // Permanently discard (retry budget exhausted)
)

// maxProposalRetries bounds how many times a delayed proposal is re-queued for
// review before it is permanently dropped, preventing an infinite delay loop.
// A proposal delayed more than this many times becomes a DecisionDrop so the
// discard is observable in DecisionHistory rather than a silent disappear.
const maxProposalRetries = 3

// String returns a human-readable name for the decision.
func (d Decision) String() string {
	switch d {
	case DecisionApply:
		return "apply"
	case DecisionReject:
		return "reject"
	case DecisionDelay:
		return "delay"
	case DecisionDrop:
		return "drop"
	default:
		return fmt.Sprintf("unknown(%d)", int(d))
	}
}

// PatchDecision pairs a proposal with a decision.
type PatchDecision struct {
	Proposal PatchProposal
	Decision Decision
	Reason   string // why this decision was made
	// ApplyError is non-nil when Decision == DecisionApply and the underlying
	// executor returned an error. Exposed here (not just in PatchHistory) so
	// callers monitoring DecisionHistory can observe apply failures without
	// cross-referencing PatchHistory. Nil for all non-apply decisions.
	ApplyError error
}

// PolicyGenome is the Coordinator's decision strategy — also evolvable.
type PolicyGenome struct {
	// AutoApplyThreshold: patches with priority >= this are auto-applied.
	AutoApplyThreshold int

	// MaxPatchesPerMinute: rate limit to prevent cascade failures.
	MaxPatchesPerMinute int

	// MinFitnessThreshold: GA patches with fitness below this are rejected.
	// Scale: 0-100, matching population BestScore. 0 = no threshold.
	// Only applies to SourceGA. Other sources bypass fitness checks.
	MinFitnessThreshold float64

	// ApplyFitnessThreshold: GA patches with fitness >= this are auto-applied.
	// Scale: 0-100, matching population BestScore. 0 = disabled.
	// Only applies to SourceGA. Other sources bypass fitness checks.
	ApplyFitnessThreshold float64

	// SelfHealingEnabled enables automatic repair patch generation when
	// chaos faults are detected. When enabled, the Coordinator monitors
	// patch failures and generated repair proposals.
	// Default: false (disabled, must be explicitly enabled).
	SelfHealingEnabled bool `json:"self_healing_enabled" yaml:"self_healing_enabled"`

	// SelfHealingMaxRetries is the maximum number of self-healing attempts
	// before the Coordinator stops trying to repair a failing component.
	// Default: 3.
	SelfHealingMaxRetries int `json:"self_healing_max_retries" yaml:"self_healing_max_retries"`
}

// DefaultPolicy returns a sensible default Coordinator policy.
//
// Calibration (post evidence-backed fitness pipeline, commit a952206e):
//   - MinFitnessThreshold = 30.0 — GA patches with fitness below this are
//     rejected outright. With 5 genomes each returning the mean success rate
//     in [0,1] scaled to [0,100], a value below 30 means at least one
//     subsystem is failing more often than it succeeds.
//   - ApplyFitnessThreshold = 70.0 — GA patches with fitness at or above
//     this are auto-applied. 70 means averaged success rate >= 70% across
//     memory/knowledge/recovery/workflow/scheduler, a strong signal that
//     the current configuration is healthy and the GA's proposed mutation
//     is worth trusting. Patches in [30, 70) land in the delay bucket for
//     operator review. Setting this to 100.0 disables GA auto-apply.
//
// Callers that need a different gate (e.g. stricter for production, looser
// for canary) construct a PolicyGenome explicitly instead of using DefaultPolicy.
func DefaultPolicy() PolicyGenome {
	return PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   4,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 70.0,
		SelfHealingEnabled:    false,
		SelfHealingMaxRetries: 3,
	}
}

// PatchResult records the outcome of a patch application.
type PatchResult struct {
	Proposal  PatchProposal
	AppliedAt time.Time
	Error     error
}

// EvolutionCoordinator collects PatchProposals from all sources and decides
// whether to apply, defer, or reject each patch.
//
// Coordinator does NOT know:
//   - How patches are generated (GA? Chaos? LLM? Human?)
//   - What a Genome is
//   - How Mutation or Crossover works
//
// Coordinator ONLY knows:
//   - A patch has been proposed
//   - Should I apply it now, delay it, or reject it?
type EvolutionCoordinator struct {
	mu           sync.RWMutex
	policy       PolicyGenome    // decision strategy (evolvable)
	proposals    []PatchProposal // pending proposals
	decisions    []PatchDecision // decision history
	patchHistory []PatchResult   // apply results
	patchReg     *patch.Registry // registry for applying patches
	deployer     PatchDeployer   // optional safe-promotion pipeline (nil = direct apply)

	// maxDecisions / maxPatchHistory cap the two append-only history slices:
	// Evaluate runs on the bootstrap 15-minute ticker for the process
	// lifetime, and every proposal appends a decision unconditionally. The
	// caps are generous — countRecentPatches only looks at a 1-minute window.
	maxDecisions    int
	maxPatchHistory int

	// Self-healing state.
	healingAttempts map[string]int // target -> number of healing attempts
	healingResults  []HealingAttempt
}

// HealingAttempt records a self-healing attempt by the Coordinator.
type HealingAttempt struct {
	Target    string    `json:"target"`
	PatchType string    `json:"patch_type"`
	Attempt   int       `json:"attempt"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// NewEvolutionCoordinator creates a new EvolutionCoordinator.
func NewEvolutionCoordinator(policy PolicyGenome, patchReg *patch.Registry) *EvolutionCoordinator {
	return &EvolutionCoordinator{
		policy:          policy,
		patchReg:        patchReg,
		healingAttempts: make(map[string]int),
		healingResults:  make([]HealingAttempt, 0),
		maxDecisions:    defaultMaxDecisions,
		maxPatchHistory: defaultMaxPatchHistory,
	}
}

// History caps: generous bounds — the decision budget logic only
// inspects a recent window, so older entries are pure archive.
const (
	defaultMaxDecisions    = 2048
	defaultMaxPatchHistory = 1024
)

// appendDecision records a decision, trimming the oldest entries when over
// the cap (caller must hold ec.mu).
func (ec *EvolutionCoordinator) appendDecision(d PatchDecision) {
	ec.decisions = append(ec.decisions, d)
	if len(ec.decisions) > ec.maxDecisions {
		ec.decisions = ec.decisions[len(ec.decisions)-ec.maxDecisions:]
	}
}

// appendPatchResult records an apply result with the same trimming contract
// as appendDecision (caller must hold ec.mu).
func (ec *EvolutionCoordinator) appendPatchResult(r PatchResult) {
	ec.patchHistory = append(ec.patchHistory, r)
	if len(ec.patchHistory) > ec.maxPatchHistory {
		ec.patchHistory = ec.patchHistory[len(ec.patchHistory)-ec.maxPatchHistory:]
	}
}

// PatchDeployer safely promotes a patch to the live runtime. It is optional:
// when nil or disabled, the Coordinator applies patches directly via patchReg,
// preserving the pre-deployment behavior. This keeps the Coordinator decoupled
// from the deployment package (it only depends on this interface).
type PatchDeployer interface {
	// Enabled reports whether auto-promotion to live is active.
	Enabled() bool
	// Deploy promotes the patch; returns a non-nil error only on catastrophic
	// failure (a normal reject/rollback is not an error).
	Deploy(ctx context.Context, p patch.RuntimePatch) error
}

// SetDeployer installs an optional safe-promotion pipeline. When set and
// enabled, accepted patches are promoted through it instead of applied
// directly. Safe to call once during wiring; nil clears it.
func (ec *EvolutionCoordinator) SetDeployer(d PatchDeployer) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.deployer = d
}

// Policy returns a snapshot of the current Coordinator policy. Callers (e.g.
// the GA adapter) use this to log the threshold values that produced a
// decision so a drop or reject is observable with its gating context.
func (ec *EvolutionCoordinator) Policy() PolicyGenome {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	return ec.policy
}

// ApplyEmergency applies a patch immediately, bypassing the decision process.
// Used for self-healing scenarios where a critical fault needs instant response.
// Returns the patch result or an error if the patch cannot be applied.
func (ec *EvolutionCoordinator) ApplyEmergency(ctx context.Context, patch patch.RuntimePatch) error {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	proposal := PatchProposal{
		Patch:     patch,
		Source:    SourceChaos,
		Reason:    "emergency: self-healing immediate apply",
		Priority:  10, // Maximum priority
		Timestamp: time.Now(),
	}

	err := ec.patchReg.Apply(ctx, patch)
	ec.appendDecision(PatchDecision{
		Proposal: proposal,
		Decision: DecisionApply,
		Reason:   "emergency: bypassed decision process",
	})
	ec.appendPatchResult(PatchResult{
		Proposal:  proposal,
		AppliedAt: time.Now(),
		Error:     err,
	})
	return err
}

// Submit receives a patch proposal from any source.
func (ec *EvolutionCoordinator) Submit(proposal PatchProposal) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.proposals = append(ec.proposals, proposal)
}

// PendingCount returns the number of pending proposals.
func (ec *EvolutionCoordinator) PendingCount() int {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	return len(ec.proposals)
}

// DecisionHistory returns all decisions made so far.
func (ec *EvolutionCoordinator) DecisionHistory() []PatchDecision {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	decisions := make([]PatchDecision, len(ec.decisions))
	copy(decisions, ec.decisions)
	return decisions
}

// PatchHistory returns all patch application results.
func (ec *EvolutionCoordinator) PatchHistory() []PatchResult {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	results := make([]PatchResult, len(ec.patchHistory))
	copy(results, ec.patchHistory)
	return results
}

// NotifySelfHealingAttempt records a self-healing attempt. Returns true if
// the Coordinator should proceed, false if disabled or max retries exceeded.
// Once exceeded, the refusal is sticky: subsequent calls for the same target
// return false without appending another record, so healingResults is bounded.
func (ec *EvolutionCoordinator) NotifySelfHealingAttempt(target string, patchType string) bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	if !ec.policy.SelfHealingEnabled {
		return false
	}

	// Sticky refusal: once exceeded, do not grow history further.
	if ec.healingAttempts[target] > ec.policy.SelfHealingMaxRetries {
		return false
	}

	ec.healingAttempts[target]++
	attempt := ec.healingAttempts[target]
	if attempt > ec.policy.SelfHealingMaxRetries {
		ec.healingResults = append(ec.healingResults, HealingAttempt{
			Target:    target,
			PatchType: patchType,
			Attempt:   attempt,
			Success:   false,
			Error:     "max retries exceeded",
			Timestamp: time.Now(),
		})
		return false
	}
	return true
}

// NotifySelfHealingOutcome records the result of a self-healing attempt.
// No-op when SelfHealingEnabled is false. The caller MUST have called
// NotifySelfHealingAttempt first for this target; if not, the outcome is
// recorded with an explicit "outcome recorded without attempt" marker so
// the misuse is observable in SelfHealingHistory rather than silently
// emitting Attempt: 0.
func (ec *EvolutionCoordinator) NotifySelfHealingOutcome(target string, patchType string, success bool, errMsg string) {
	ec.mu.Lock()
	defer ec.mu.Unlock()

	if !ec.policy.SelfHealingEnabled {
		return
	}

	attempt := ec.healingAttempts[target]
	err := errMsg
	if attempt == 0 {
		if err != "" {
			err += "; "
		}
		err += "outcome recorded without attempt"
	}

	ec.healingResults = append(ec.healingResults, HealingAttempt{
		Target:    target,
		PatchType: patchType,
		Attempt:   attempt,
		Success:   success,
		Error:     err,
		Timestamp: time.Now(),
	})
}

// SelfHealingHistory returns all self-healing attempts for observability.
func (ec *EvolutionCoordinator) SelfHealingHistory() []HealingAttempt {
	ec.mu.RLock()
	defer ec.mu.RUnlock()
	out := make([]HealingAttempt, len(ec.healingResults))
	copy(out, ec.healingResults)
	return out
}

// Evaluate processes all pending proposals and applies accepted patches.
//
// Per-proposal flow:
//  1. decide() returns Apply/Reject/Delay/Drop.
//  2. Side effects run (apply patch, re-queue delay, no-op for reject/drop).
//  3. The decision is appended to DecisionHistory AFTER side effects, so
//     ApplyError (when the executor fails) is captured on the decision
//     rather than only in PatchHistory. This makes apply failures observable
//     to callers that read DecisionHistory.
func (ec *EvolutionCoordinator) Evaluate(ctx context.Context) {
	ec.mu.Lock()
	pending := ec.proposals
	ec.proposals = nil
	policy := ec.policy
	ec.mu.Unlock()

	for _, proposal := range pending {
		decision := ec.decide(proposal)
		reason := decisionReason(decision, proposal, policy)

		var applyErr error
		switch decision {
		case DecisionApply:
			// Apply the patch. When a deployer is installed and enabled,
			// promote through the safe-deployment pipeline (staging → live);
			// otherwise apply directly to preserve prior behavior.
			ec.mu.Lock()
			d := ec.deployer
			ec.mu.Unlock()
			if d != nil && d.Enabled() {
				applyErr = d.Deploy(ctx, proposal.Patch)
			} else {
				applyErr = ec.patchReg.Apply(ctx, proposal.Patch)
			}
			ec.mu.Lock()
			ec.appendPatchResult(PatchResult{
				Proposal:  proposal,
				AppliedAt: time.Now(),
				Error:     applyErr,
			})
			ec.mu.Unlock()
		case DecisionDelay:
			// Re-queue for later review. Bounded by maxProposalRetries:
			// decide() already returned DecisionDrop when the budget is
			// exhausted, so reaching here means the proposal still has
			// retries left.
			proposal.RetryCount++
			ec.mu.Lock()
			ec.proposals = append(ec.proposals, proposal)
			ec.mu.Unlock()
		case DecisionReject:
			// Permanently rejected; do not re-queue.
		case DecisionDrop:
			// Permanently discarded; do not re-queue. The decision is
			// recorded in DecisionHistory so the drop is observable instead
			// of the silent disappear that previously happened when retry
			// budget was exhausted.
		}

		ec.mu.Lock()
		ec.appendDecision(PatchDecision{
			Proposal:   proposal,
			Decision:   decision,
			Reason:     reason,
			ApplyError: applyErr,
		})
		ec.mu.Unlock()
	}
}

// decide implements the decision policy.
// Source-specific routing:
//   - SourceGA: fitness-gated (apply ≥ ApplyFitnessThreshold, reject < MinFitnessThreshold)
//   - SourceChaos: emergency bypass via ApplyEmergency, not here
//   - SourceHuman/SourceLLM/other: fallback to priority + rate-limit rules
//   - Fitness == 0 (unset): treated as "no information" → fallback to priority rules
//
// Retry exhaustion: when a proposal's RetryCount has already hit
// maxProposalRetries and the underlying decision would be DecisionDelay, the
// function returns DecisionDrop instead so the discard is observable in
// DecisionHistory. Decisions that would NOT delay (Apply/Reject) ignore
// retry count: a proposal that finally qualifies for apply after rate-limit
// clears must still be applied, not dropped.
func (ec *EvolutionCoordinator) decide(proposal PatchProposal) Decision {
	ec.mu.RLock()
	policy := ec.policy
	ec.mu.RUnlock()

	// Rate limiting applies to all sources.
	recentCount := ec.countRecentPatches(1 * time.Minute)
	if recentCount >= policy.MaxPatchesPerMinute {
		if proposal.RetryCount >= maxProposalRetries {
			return DecisionDrop
		}
		return DecisionDelay
	}

	// GA source: fitness-gated decision.
	if proposal.Source == SourceGA && proposal.Fitness > 0 {
		if proposal.Fitness >= policy.ApplyFitnessThreshold {
			return DecisionApply
		}
		if proposal.Fitness < policy.MinFitnessThreshold {
			return DecisionReject
		}
		// Fitness between floor and threshold: delay for review.
		if proposal.RetryCount >= maxProposalRetries {
			return DecisionDrop
		}
		return DecisionDelay
	}

	// Non-GA sources or Fitness == 0: fallback to priority rules.
	if proposal.Priority >= policy.AutoApplyThreshold {
		return DecisionApply
	}

	// Below auto-apply threshold: delay for review rather than silently
	// applying. The previous fallthrough to DecisionApply meant all patches
	// got applied regardless of quality — rendering AutoApplyThreshold dead.
	if proposal.RetryCount >= maxProposalRetries {
		return DecisionDrop
	}
	return DecisionDelay
}

// countRecentPatches counts patch applications within the given duration.
func (ec *EvolutionCoordinator) countRecentPatches(d time.Duration) int {
	ec.mu.RLock()
	defer ec.mu.RUnlock()

	since := time.Now().Add(-d)
	var count int
	for _, r := range ec.patchHistory {
		if r.AppliedAt.After(since) {
			count++
		}
	}
	return count
}

// decisionReason returns a human-readable reason for the decision.
// The policy is passed in (rather than read from ec) so the reason reflects
// the same policy that produced the decision, and callers can unit-test the
// reason string for non-default policies without racing the mutex.
func decisionReason(d Decision, proposal PatchProposal, policy PolicyGenome) string {
	switch d {
	case DecisionApply:
		if proposal.Source == SourceGA && proposal.Fitness > 0 {
			return fmt.Sprintf("applying patch %s from %s: fitness %.1f >= threshold %.1f",
				proposal.Patch.Type, proposal.Source, proposal.Fitness, policy.ApplyFitnessThreshold)
		}
		return fmt.Sprintf("applying patch %s from %s (priority %d)",
			proposal.Patch.Type, proposal.Source, proposal.Priority)
	case DecisionReject:
		if proposal.Source == SourceGA && proposal.Fitness > 0 {
			return fmt.Sprintf("rejected patch %s from %s: fitness %.1f < min threshold %.1f",
				proposal.Patch.Type, proposal.Source, proposal.Fitness, policy.MinFitnessThreshold)
		}
		return fmt.Sprintf("rejected patch %s from %s: rate limited or blacklisted",
			proposal.Patch.Type, proposal.Source)
	case DecisionDelay:
		if proposal.Source == SourceGA && proposal.Fitness > 0 {
			return fmt.Sprintf("delayed patch %s from %s: fitness %.1f between floor %.1f and threshold %.1f (retry %d/%d)",
				proposal.Patch.Type, proposal.Source, proposal.Fitness,
				policy.MinFitnessThreshold, policy.ApplyFitnessThreshold,
				proposal.RetryCount, maxProposalRetries)
		}
		return fmt.Sprintf("delayed patch %s from %s: rate limited (retry %d/%d)",
			proposal.Patch.Type, proposal.Source, proposal.RetryCount, maxProposalRetries)
	case DecisionDrop:
		if proposal.Source == SourceGA && proposal.Fitness > 0 {
			return fmt.Sprintf("dropped patch %s from %s: fitness %.1f still between floor %.1f and threshold %.1f after %d retries",
				proposal.Patch.Type, proposal.Source, proposal.Fitness,
				policy.MinFitnessThreshold, policy.ApplyFitnessThreshold, proposal.RetryCount)
		}
		return fmt.Sprintf("dropped patch %s from %s: retry budget exhausted (%d retries)",
			proposal.Patch.Type, proposal.Source, proposal.RetryCount)
	default:
		return "unknown decision"
	}
}
