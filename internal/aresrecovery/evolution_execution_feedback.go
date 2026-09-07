package aresrecovery

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// ExecutionAttribution is the feedback loop's data source: it collects
// per-agent, per-capability execution outcomes (success/failure) so the
// Evolution system can attribute results to capabilities and feed them back
// into scheduler scoring. The loadTracker in cmd/ares/scheduler.go tracks
// per-agent outcomes; this struct adds the capability dimension and the
// query interface the Evolution system uses.
//
// The attribution is the first half of the feedback loop:
//
//	Agent executes task (capability C) → outcome (success/fail) →
//	ExecutionAttribution.Record(agentID, C, success) →
//	Evolution reads Attribution() → updates strategy →
//	updates loadTracker.Confidence → next Schedule sees the new confidence.
//
// Thread-safe; every method is safe for concurrent use.
type ExecutionAttribution struct {
	mu sync.Mutex
	// results maps "agentID|capability" → outcome counts.
	results map[string]*capabilityOutcome
	// agentResults maps agentID → aggregated outcome (all capabilities).
	agentResults map[string]*capabilityOutcome
}

// capabilityOutcome tracks success/failure counts for one (agent, capability)
// pair (or one agent's aggregate).
//
// Extended fields: latency (nanoseconds), retries, and recovery
// count — these feed the deterministic scorer so the evolution
// system can score strategies without calling an LLM.
type capabilityOutcome struct {
	success       int
	fail          int
	totalLatency  time.Duration
	totalRetries  int
	totalRecovers int
}

// NewExecutionAttribution creates an empty attribution store.
func NewExecutionAttribution() *ExecutionAttribution {
	return &ExecutionAttribution{
		results:      make(map[string]*capabilityOutcome),
		agentResults: make(map[string]*capabilityOutcome),
	}
}

// Record adds one execution outcome for an agent on a capability. Called by
// the scheduler after each quantum finalizes (the loadTracker.end call site
// is the natural production caller — same execution boundary, same outcome).
//
// Args:
//   - agentID: the executor that ran the task.
//   - capability: the task's required capability (the capability the agent
//     was scored against).
//   - success: true when the task completed, false when it failed.
//
// The attribution key is "agentID|capability", so agentID and capability must
// not contain the '|' separator. Entries violating that invariant are
// rejected with a log line instead of corrupting the key (EDGE-5: the
// invariant is enforced here, not only assumed by splitAttributionKey).
func (a *ExecutionAttribution) Record(agentID, capability string, success bool) {
	a.RecordWithMetrics(agentID, capability, success, 0, 0, 0)
}

// RecordWithMetrics is the extended Record that accepts latency,
// retry count, and recovery count alongside the success/failure outcome.
// These feed the deterministic scorer so attribution → strategy
// score works without any LLM call.
//
// Args:
//   - agentID: the executor that ran the task.
//   - capability: the task's required capability.
//   - success: true when the task completed, false when it failed.
//   - latency: the quantum's wall-clock duration (0 if unknown).
//   - retries: how many retries the task used (0 for first attempt).
//   - recovers: how many recovery replacements were needed (0 normally).
func (a *ExecutionAttribution) RecordWithMetrics(
	agentID, capability string,
	success bool,
	latency time.Duration,
	retries, recovers int,
) {
	if strings.Contains(agentID, "|") || strings.Contains(capability, "|") {
		slog.Warn("aresrecovery: reject attribution record: contains '|'",
			slog.String("agent_id", agentID), slog.String("capability", capability))
		return
	}
	key := agentID + "|" + capability
	a.mu.Lock()
	defer a.mu.Unlock()
	out, ok := a.results[key]
	if !ok {
		out = &capabilityOutcome{}
		a.results[key] = out
	}
	if success {
		out.success++
	} else {
		out.fail++
	}
	out.totalLatency += latency
	out.totalRetries += retries
	out.totalRecovers += recovers

	agent, ok := a.agentResults[agentID]
	if !ok {
		agent = &capabilityOutcome{}
		a.agentResults[agentID] = agent
	}
	if success {
		agent.success++
	} else {
		agent.fail++
	}
	agent.totalLatency += latency
	agent.totalRetries += retries
	agent.totalRecovers += recovers
}

// CapabilityConfidence returns the success rate [0,1] for an agent on a
// specific capability. Returns 1.0 (neutral prior) when no history exists —
// matching loadTracker.Confidence's contract so the scheduler can use either
// source interchangeably.
//
// Args:
//   - agentID: the executor.
//   - capability: the required capability.
//
// Returns:
//   - float64: success rate in [0,1], or 1.0 when no history.
func (a *ExecutionAttribution) CapabilityConfidence(agentID, capability string) float64 {
	key := agentID + "|" + capability
	a.mu.Lock()
	defer a.mu.Unlock()
	out, ok := a.results[key]
	if !ok || out.success+out.fail == 0 {
		return 1.0
	}
	return float64(out.success) / float64(out.success+out.fail)
}

// AgentConfidence returns the aggregate success rate [0,1] for an agent across
// all capabilities. Returns 1.0 (neutral prior) when no history exists.
func (a *ExecutionAttribution) AgentConfidence(agentID string) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out, ok := a.agentResults[agentID]
	if !ok || out.success+out.fail == 0 {
		return 1.0
	}
	return float64(out.success) / float64(out.success+out.fail)
}

// AttributionSnapshot is a point-in-time view of the attribution data, used
// by the Evolution system to read execution results and feed them back into
// scheduling policy.
type AttributionSnapshot struct {
	// PerCapability maps "agentID|capability" → {success, fail, rate}.
	PerCapability []CapabilityResult
	// PerAgent maps agentID → {success, fail, rate}.
	PerAgent []AgentResult
	// At is when the snapshot was taken.
	At time.Time
}

// CapabilityResult is one (agent, capability) pair's outcome summary.
// Extended with AvgLatency, AvgRetries, AvgRecovers for the
// deterministic scorer.
type CapabilityResult struct {
	AgentID     string
	Capability  string
	Success     int
	Fail        int
	Rate        float64
	AvgLatency  time.Duration
	AvgRetries  float64
	AvgRecovers float64
}

// AgentResult is one agent's aggregate outcome summary.
// Extended with AvgLatency, AvgRetries, AvgRecovers.
type AgentResult struct {
	AgentID     string
	Success     int
	Fail        int
	Rate        float64
	AvgLatency  time.Duration
	AvgRetries  float64
	AvgRecovers float64
}

// Snapshot returns a point-in-time copy of the attribution data. The
// Evolution system calls this to read execution results for policy updates.
func (a *ExecutionAttribution) Snapshot() AttributionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	caps := make([]CapabilityResult, 0, len(a.results))
	for key, out := range a.results {
		// Split "agentID|capability" back into parts.
		agentID, cap := splitAttributionKey(key)
		total := out.success + out.fail
		rate := 1.0
		var avgLatency time.Duration
		var avgRetries, avgRecovers float64
		if total > 0 {
			rate = float64(out.success) / float64(total)
			avgLatency = out.totalLatency / time.Duration(total)
			avgRetries = float64(out.totalRetries) / float64(total)
			avgRecovers = float64(out.totalRecovers) / float64(total)
		}
		caps = append(caps, CapabilityResult{
			AgentID:     agentID,
			Capability:  cap,
			Success:     out.success,
			Fail:        out.fail,
			Rate:        rate,
			AvgLatency:  avgLatency,
			AvgRetries:  avgRetries,
			AvgRecovers: avgRecovers,
		})
	}
	agents := make([]AgentResult, 0, len(a.agentResults))
	for agentID, out := range a.agentResults {
		total := out.success + out.fail
		rate := 1.0
		var avgLatency time.Duration
		var avgRetries, avgRecovers float64
		if total > 0 {
			rate = float64(out.success) / float64(total)
			avgLatency = out.totalLatency / time.Duration(total)
			avgRetries = float64(out.totalRetries) / float64(total)
			avgRecovers = float64(out.totalRecovers) / float64(total)
		}
		agents = append(agents, AgentResult{
			AgentID:     agentID,
			Success:     out.success,
			Fail:        out.fail,
			Rate:        rate,
			AvgLatency:  avgLatency,
			AvgRetries:  avgRetries,
			AvgRecovers: avgRecovers,
		})
	}
	return AttributionSnapshot{
		PerCapability: caps,
		PerAgent:      agents,
		At:            time.Now(),
	}
}

// splitAttributionKey splits "agentID|capability" into its parts. The
// agentID is everything before the first "|"; the capability is everything
// after. This is safe because agentIDs do not contain "|" (they are
// generated by the kernel from a restricted charset).
func splitAttributionKey(key string) (string, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// ExecutionResultSource is the interface the Evolution system uses to read
// execution attribution (采集真实执行结果). The scheduler's
// ExecutionAttribution implements this; the Evolution system reads the
// snapshot and updates its strategy based on the results.
type ExecutionResultSource interface {
	// Snapshot returns the current execution attribution.
	Snapshot() AttributionSnapshot
}

// Ensure ExecutionAttribution satisfies the source interface.
var _ ExecutionResultSource = (*ExecutionAttribution)(nil)

// ConfidenceInjector is the interface for feeding evolution-derived
// confidence back into the scheduler's scoring. The loadTracker
// implements this, so the Evolution system can update the scheduler's
// confidence without importing the scheduler package — the adapter is wired
// at the Kernel level.
type ConfidenceInjector interface {
	// SetAgentConfidence updates the confidence the scheduler uses for
	// agentID. A value in [0,1] sets the historical success rate; <= 0
	// resets to the neutral prior (1.0).
	SetAgentConfidence(agentID string, confidence float64)

	// SetCapabilityConfidence updates the per-capability confidence for
	// (agentID, capability). The scheduler scores candidates against the
	// task's exact capability with this more granular value when available
	// (ConfidenceFor in LoadTracker), falling back to the agent-level
	// confidence otherwise. A value in [0,1] sets the override; a negative
	// value (< 0) clears it.
	SetCapabilityConfidence(agentID, capability string, confidence float64)
}

// EvolutionFeedbackAdapter wires the ExecutionAttribution to the scheduler's
// ConfidenceInjector. It is the second half of the feedback loop:
//
//	ExecutionAttribution.Snapshot() → EvolutionFeedbackAdapter.Apply() →
//	ConfidenceInjector.SetAgentConfidence() → next Schedule sees the new
//	confidence.
//
// The adapter is safe for concurrent use (it delegates to the injector's
// thread-safe methods). Apply is idempotent: calling it with no new results
// is a no-op.
type EvolutionFeedbackAdapter struct {
	source   ExecutionResultSource
	injector ConfidenceInjector
}

// NewEvolutionFeedbackAdapter wires the attribution source to the confidence
// injector. Both must be non-nil; a nil adapter is a no-op (the loop checks).
func NewEvolutionFeedbackAdapter(source ExecutionResultSource, injector ConfidenceInjector) *EvolutionFeedbackAdapter {
	return &EvolutionFeedbackAdapter{source: source, injector: injector}
}

// Apply reads the current execution attribution and pushes the confidence
// into the scheduler's scoring — both per-agent and per-capability. After
// this call, the scheduler's next Schedule sees the updated confidence: a
// failure-heavy agent is downweighted, a success-heavy agent is preferred.
// The per-capability pushes (SetCapabilityConfidence) let the scheduler score
// an agent against the task's exact capability instead of a single aggregate
// value (per-capability attribution is consumed, not collected only).
//
// Args:
//   - ctx: unused (kept for signature symmetry with other Apply methods).
//
// Returns:
//   - int: the number of agents whose confidence was updated (per-agent
//     count; per-capability pushes are additional).
func (a *EvolutionFeedbackAdapter) Apply(_ context.Context) int {
	if a == nil || a.source == nil || a.injector == nil {
		return 0
	}
	snap := a.source.Snapshot()
	updated := 0
	for _, ar := range snap.PerAgent {
		// Only push a confidence when there is history (total > 0). A
		// zero-total entry means the agent has not executed yet — its
		// confidence stays at the neutral prior.
		if ar.Success+ar.Fail == 0 {
			continue
		}
		a.injector.SetAgentConfidence(ar.AgentID, ar.Rate)
		updated++
	}
	for _, cr := range snap.PerCapability {
		if cr.Success+cr.Fail == 0 {
			continue
		}
		a.injector.SetCapabilityConfidence(cr.AgentID, cr.Capability, cr.Rate)
	}
	return updated
}

// RunEvolutionFeedbackLoop periodically reads execution attribution and
// pushes the per-agent confidence into the scheduler. It applies once at
// startup so already-collected results are effective immediately, then
// re-applies on a fixed interval. Apply is idempotent.
//
// Args:
//   - ctx: stops the loop.
//   - adapter: the feedback adapter (nil disables the loop).
//   - interval: how often to apply; <= 0 uses a 10s default.
func RunEvolutionFeedbackLoop(ctx context.Context, adapter *EvolutionFeedbackAdapter, interval time.Duration) {
	if adapter == nil {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	apply := func(phase string) int {
		defer func() {
			if r := recover(); r != nil {
				// A panic must not kill the loop (kernel loops must
				// not crash the process), but it must be logged for
				// observability.
				slog.Error("feedback loop panic recovered",
					"phase", phase, "panic", r)
			}
		}()
		return adapter.Apply(ctx)
	}
	apply("startup")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			apply("tick")
		case <-ctx.Done():
			return
		}
	}
}
