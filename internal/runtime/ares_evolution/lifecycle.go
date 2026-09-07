// lifecycle.go provides the StrategyLifecycle — the sole orchestrator that
// can change the active strategy. It implements the candidate state machine:
//
//	CANDIDATE → SHADOW → ACTIVE → DEGRADED → (rollback to previous)
//
// The lifecycle is the single submission entry point: only Submit(candidate)
// can change the active strategy. GA's deployBestStrategy now calls Submit
// instead of Deploy directly (B2 fix). Before promoting, the lifecycle runs
// four serial verify gates (B2/B3 fix). After promotion, a background watch
// loop feeds real runtime samples into RollbackPolicy and triggers
// Rollback when degradation is detected (B1 fix).
//
// NIL-SAFETY / LEGACY PATH (review P1-3, made explicit): the lifecycle
// itself has NO unconditional deploy fallback — when Enabled is false or the
// lifecycle is not wired, Submit is a no-op and the active strategy is never
// changed through this type. The only legacy path lives in
// GenomePopulationAdapter.Run: when a.lifecycle == nil it falls back to
// deployBestStrategy (the pre-B2 direct Deploy call) for systems built
// without a lifecycle. There is no way to bypass the G2 shadow gate through
// the lifecycle once it IS wired: the gate is registered fail-closed and
// Submit runs every registered gate.
package evolution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// CandidateState identifies where a candidate strategy is in the lifecycle.
type CandidateState int

const (
	// StateCandidate is the initial state: GA produced a candidate but it
	// has not passed any verification gate yet.
	StateCandidate CandidateState = iota
	// StateShadow means the candidate is undergoing shadow evaluation.
	// It is NOT visible to the live agent.
	StateShadow
	// StateActive means the candidate has been promoted to the active
	// strategy. The live agent reads it via GetActiveStrategy.
	StateActive
	// StateDegraded means the active strategy's runtime performance has
	// dropped below the rollback threshold and Rollback is pending.
	StateDegraded
)

// String returns the human-readable name of the candidate state.
func (s CandidateState) String() string {
	switch s {
	case StateCandidate:
		return "candidate"
	case StateShadow:
		return "shadow"
	case StateActive:
		return "active"
	case StateDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// VerifyGate is a single verification checkpoint in the promote pipeline.
// Each gate returns whether the candidate passed, a normalized score (when
// applicable), and a human-readable reason for rejection.
type VerifyGate interface {
	// Name identifies the gate (e.g. "guardrail", "shadow", "eval").
	Name() string
	// Check evaluates the candidate against the currently active strategy.
	// Returns pass=true when the candidate may proceed to the next gate.
	Check(ctx context.Context, cand, active *mutation.Strategy) (pass bool, score float64, reason string)
}

// LifecycleConfig groups all StrategyLifecycle settings.
//
// Note on scope: only settings the lifecycle itself consumes live here.
// Rollback thresholds and shadow thresholds are consumed by
// ActiveStrategyManager / ShadowEvaluator respectively, built from
// SystemConfig.RollbackPolicyConfig / SystemConfig.ShadowEvalConfig — they
// are deliberately NOT duplicated in this struct.
type LifecycleConfig struct {
	// Enabled activates the lifecycle orchestrator. When false, Submit
	// falls back to the legacy direct-deploy path (backward compatible).
	Enabled bool `json:"enabled"`
	// FitnessWindow is the number of runtime samples to keep for rollback
	// evaluation.
	FitnessWindow int `json:"fitness_window"`
	// MinSamplesBeforeJudge is the minimum runtime sample count before
	// promote/rollback decisions are made.
	MinSamplesBeforeJudge int `json:"min_samples_before_judge"`
	// ColdStartScore is the fallback fitness when no evidence exists.
	ColdStartScore float64 `json:"cold_start_score"`
	// Weights controls per-source fitness contribution.
	Weights FitnessWeights `json:"weights"`
	// WatchInterval is the rollback watch-loop tick interval. Zero or
	// negative falls back to defaultWatchInterval.
	WatchInterval time.Duration `json:"watch_interval"`
	// BlacklistGenerations is how many generations a rolled-back candidate
	// stays banned from re-nomination (§9: rollback oscillation damping).
	// Zero or negative falls back to defaultBlacklistGenerations.
	BlacklistGenerations int `json:"blacklist_generations"`
	// MinActiveDuration is how long a promoted strategy must stay active
	// before another candidate may replace it (evolution loop closure E2,
	// promote throttling). Without it the GA ticker could rotate strategies
	// faster than the rollback window accumulates evidence, making
	// degradation undetectable in principle — this is a CORRECTNESS
	// precondition of opening the promote path, not an optional optimization.
	// Zero falls back to 3 × WatchInterval, so at least three rollback
	// windows are observed between promotes. The residency clock starts at
	// the first GATED promote (the one-shot seed deploy does not start it:
	// the seed is the baseline §9 relies on, and rejecting the first real
	// candidate after it would leave the loop permanently empty).
	MinActiveDuration time.Duration `json:"min_active_duration"`
	// RollbackArmed reports whether the post-deployment rollback watch loop
	// may trigger an automatic Rollback (evolution loop closure E2). It is
	// the second half of the shadow-gate safety invariant: skipping
	// PRE-deployment verification is allowed only when POST-deployment
	// verification is armed. When false, evaluateAndMaybeRollback never
	// fires and the wiring layer must keep the G2 gate registered
	// fail-closed.
	RollbackArmed bool `json:"rollback_armed"`
	// DisableShadowGate suppresses the automatic G2 registration — the
	// documented no-scorer-plus-armed-rollback case. The wiring layer sets it
	// via ShadowGateMode's decision; the lifecycle only ever sees the
	// explicit instruction (gate absence is a wiring decision, never an
	// emergent property of nil-checking). ShadowGateSkipReason records why,
	// for the snapshot and startup log — the absence must be visible.
	DisableShadowGate    bool   `json:"disable_shadow_gate"`
	ShadowGateSkipReason string `json:"shadow_gate_skip_reason,omitempty"`
	// Gates holds verify-gate-specific settings.
	Gates GateConfig `json:"gates"`
}

// GateConfig groups verify-gate thresholds.
type GateConfig struct {
	// EvalMinScore is the minimum G3 (eval suite) score for a candidate to
	// pass. Set to 0 to disable the eval gate.
	EvalMinScore float64 `json:"eval_min_score"`
	// RequireManualApproval, when true, holds candidates in SHADOW until an
	// external API call explicitly approves them (P2-4). Submit returns
	// immediately — the CANDIDATE is held, never the caller's goroutine.
	RequireManualApproval bool `json:"require_manual_approval"`
}

// defaultWatchInterval is the rollback watch-loop period when
// LifecycleConfig.WatchInterval is unset.
const defaultWatchInterval = 30 * time.Second

// defaultBlacklistGenerations is the re-nomination ban window (in
// generations) applied to a rolled-back candidate when
// LifecycleConfig.BlacklistGenerations is unset.
const defaultBlacklistGenerations = 3

// defaultResidencyTicks is the default minimum-active duration, expressed in
// watch-loop ticks: a promoted strategy must survive at least three rollback
// windows before it may be replaced.
const defaultResidencyTicks = 3

// gateMinActiveDuration is the promote-throttle's pseudo-gate name, used for
// the gate-reject metric so throttled submissions are observable on the same
// counter as real gate rejections.
const gateMinActiveDuration = "min_active_duration"

// blacklistGenerations returns the effective ban window.
func (c LifecycleConfig) blacklistGenerations() int {
	if c.BlacklistGenerations > 0 {
		return c.BlacklistGenerations
	}
	return defaultBlacklistGenerations
}

// minActiveDuration returns the effective residency period. Zero falls back
// to 3 × watchInterval (the caller passes the already-defaulted interval).
func (c LifecycleConfig) minActiveDuration(watchInterval time.Duration) time.Duration {
	if c.MinActiveDuration > 0 {
		return c.MinActiveDuration
	}
	if watchInterval <= 0 {
		watchInterval = defaultWatchInterval
	}
	return defaultResidencyTicks * watchInterval
}

// DefaultLifecycleConfig returns sensible defaults matching the design doc.
func DefaultLifecycleConfig() LifecycleConfig {
	return LifecycleConfig{
		Enabled:               true,
		FitnessWindow:         50,
		MinSamplesBeforeJudge: 10,
		ColdStartScore:        0.5,
		Weights:               DefaultFitnessWeights(),
		WatchInterval:         defaultWatchInterval,
		BlacklistGenerations:  defaultBlacklistGenerations,
		Gates: GateConfig{
			EvalMinScore: 0.7,
		},
	}
}

// CompileInfoProvider supplies compile provenance for the introspection
// chain (C5.2). The wiring layer (cmd/ares) adapts the planprojection.
// CompileCoordinator into this interface so /api/evolution/lifecycle can
// answer "which generation, which gate, which compile" without ares_evolution
// importing planprojection (which would create a circular dependency).
//
// When not wired, the compile fields in LifecycleState stay zero-valued.
type CompileInfoProvider interface {
	// CompileID returns the most recent compile's unique identifier.
	CompileID() string
	// DAGVersion returns the live DAG's mutation counter at the last compile.
	DAGVersion() uint64
	// CompileCount returns the total number of compiles since startup.
	CompileCount() uint64
}

// lifecycleSnapshot was renamed to LifecycleState: the type name clashed with
// the LifecycleSnapshot METHOD (required by introspect.LifecycleSnapshotProvider),
// which read like two different things sharing one name.
// LifecycleState is a point-in-time copy of the lifecycle state for
// the HTTP /evolution/lifecycle endpoint (P2-2).
type LifecycleState struct {
	ActiveID        string  `json:"active_id"`
	PreviousID      string  `json:"previous_id,omitempty"`
	ShadowID        string  `json:"shadow_id,omitempty"`
	State           string  `json:"state"`
	WindowScore     float64 `json:"window_score"`
	WindowCount     int     `json:"window_count"`
	Generation      int     `json:"generation"`
	LastDecision    string  `json:"last_decision,omitempty"`
	PendingApproval bool    `json:"pending_approval,omitempty"`
	// HeldID / HeldGeneration identify the candidate awaiting manual
	// approval, so an operator sees WHICH generation they are approving
	// before calling /api/evolution/approve. Zero when nothing is held.
	HeldID         string `json:"held_id,omitempty"`
	HeldGeneration int    `json:"held_generation,omitempty"`
	// Gates lists the names of the verify gates actually registered, so an
	// operator sees at a glance which verification pipeline is live
	// (evolution loop closure E2/E6).
	Gates []string `json:"gates,omitempty"`
	// ShadowGateSkipReason is non-empty when the G2 shadow gate was
	// deliberately NOT registered (no independent scorer + rollback armed):
	// the absence is a decision and must be visible, not emergent.
	ShadowGateSkipReason string `json:"shadow_gate_skipped_reason,omitempty"`
	// ActiveSince is when the currently active strategy was promoted by this
	// lifecycle (zero for an externally deployed / seed baseline).
	ActiveSince time.Time `json:"active_since,omitempty"`
	// MinActiveDuration is the effective residency period between promotes.
	MinActiveDuration time.Duration `json:"min_active_duration,omitempty"`
	// RollbackArmed reports whether the automatic rollback watch loop may
	// trigger (the post-deployment safety net).
	RollbackArmed bool `json:"rollback_armed"`

	// C5.2: compile provenance for the attribution chain. The triplet
	// (Generation, Gates, CompileID) answers "which generation, which gate,
	// which compile" — the introspection acceptance contract. Zero values
	// when no CompileInfoProvider is wired.
	CompileID    string `json:"compile_id,omitempty"`
	DAGVersion   uint64 `json:"dag_version"`
	CompileCount uint64 `json:"compile_count"`
}

// StrategyLifecycle is the sole orchestrator that can change the active
// strategy. It owns the candidate state machine, the verify gates, and the
// rollback watch loop.
type StrategyLifecycle struct {
	asm     *ActiveStrategyManager
	agg     *RuntimeFitnessAggregator
	shadow  *ShadowEvaluator
	sampler *ShadowSampler
	metrics *observability.PrometheusMetrics
	evStore evidence.Store

	cfg LifecycleConfig

	mu sync.Mutex
	// state holds the current candidate's lifecycle state.
	state CandidateState
	// currentCandidate is the strategy currently being evaluated or deployed.
	currentCandidate *mutation.Strategy
	// generation is the GA generation that produced the current candidate.
	generation int
	// blacklist holds strategy IDs that were rolled back, mapped to the
	// generation at which the ban LIFTS (banUntil = rollBackGen + N, §9).
	// Entries are pruned once the submitted generation passes banUntil.
	blacklist map[string]int // strategyID → generation when the ban lifts
	// cancel stops the watch loop.
	cancel context.CancelFunc
	// done is closed when the watch loop exits; Stop waits on it so a
	// shutdown sequence cannot race a late rollback decision (K3: no
	// fire-and-forget goroutines — Start/Stop is a managed pair).
	done chan struct{}
	// lastDecision is the reason for the most recent promote/rollback.
	lastDecision string

	// heldCandidate is the strategy currently held in SHADOW awaiting an
	// external Approve() call (P2-4, RequireManualApproval=true). Submit
	// stores it and RETURNS immediately — the candidate is held, not the
	// caller's goroutine: the ticker/adapter path must never block on human
	// latency. Approve() promotes it; new Submits are rejected while a hold
	// is pending. Exposed to operators via Snapshot.HeldID/HeldGeneration
	// so an approver can judge the candidate's freshness before deciding.
	heldCandidate *mutation.Strategy
	// heldGeneration is the GA generation that produced heldCandidate.
	heldGeneration int
	// pendingApproval mirrors heldCandidate != nil for cheap Snapshot reads.
	pendingApproval bool
	// lastWindowAt is the newest evidence timestamp seen by the previous
	// watch tick. RecordScore fires only when the window ADVANCES — judged
	// by this TIMESTAMP, not by the record count: each source's count
	// saturates at WindowSize (50), and under steady-state churn the count
	// stays flat forever ("one in, one out"), which would silently kill the
	// rollback feed if judged by count (12h-soak certainty, not an edge
	// case). The timestamp is reset on promote so the new strategy's first
	// window records immediately.
	lastWindowAt time.Time
	// activeSince is when the CURRENT strategy was promoted by a GATED
	// Submit/Approve. It drives the promote throttle (MinActiveDuration) and
	// the active-duration gauge. It is deliberately NOT set by the one-shot
	// seed deploy: the seed is the §9 baseline, not a judged promote, and
	// starting the residency clock there would keep the first real candidate
	// waiting for a window that has no evidence source yet.
	activeSince time.Time
	// shadowGateSkipReason is non-empty when the wiring layer decided (via
	// WithShadowGateDisabled) NOT to register the G2 shadow gate. Surfaced by
	// Snapshot so the absence is visible.
	shadowGateSkipReason string
	// seeded marks that the lifecycle has performed (or observed) its one
	// seed deployment. After it flips, NO candidate may skip the gate
	// pipeline — even if the ASM later reports no active strategy (reset or
	// emptied store), which would otherwise re-open the gate-free path.
	seeded bool

	// compileInfo supplies compile provenance for the C5.2 attribution chain.
	// When wired, LifecycleSnapshot exposes (compile_id, dag_version,
	// compile_count) so /api/evolution/lifecycle can answer "which compile
	// produced the current task set". Nil when not wired (zero-valued fields).
	compileInfo CompileInfoProvider

	// gates holds the ordered verify gates.
	gates []VerifyGate
}

// LifecycleOption configures a StrategyLifecycle.
type LifecycleOption func(*StrategyLifecycle)

// WithLifecycleGates sets the ordered verify gates (G1..G4). When set, they
// replace the default gate set. Gates are evaluated in order: the first
// failure short-circuits the pipeline.
func WithLifecycleGates(gates ...VerifyGate) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.gates = append(l.gates, gates...)
	}
}

// WithLifecycleShadowEvaluator attaches a ShadowEvaluator for the G2 gate.
// When set, the lifecycle registers a shadow verify gate AHEAD of any
// explicitly supplied gates (G2 runs before G3 eval), and the evaluator's
// accumulated comparisons are enforced fail-closed: enough samples with a
// win rate at or above the configured threshold → pass; below, or no data
// yet → reject (design doc §3.1; the data feeder — DreamCycle today, a
// task-level sampler per P0-9 — owns StartShadow/RecordResult; the gate is
// read-only).
func WithLifecycleShadowEvaluator(se *ShadowEvaluator) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.shadow = se
	}
}

// WithLifecycleShadowSampler attaches the P0-9 task-level shadow feeder. When
// set (and an independent scorer is wired on the evaluator), Submit primes the
// sampler before running the gates so the G2 shadow gate has comparison
// evidence to judge in default configs where DreamCycle is disabled.
func WithLifecycleShadowSampler(s *ShadowSampler) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.sampler = s
	}
}

// ShadowSampler returns the wired task-level shadow feeder, or nil. The serve
// layer uses it to attach the real-execution A/B feeder (closure plan Step 4
// / N-1), which needs the serve-time cognition stack and is therefore
// constructed after the evolution system.
func (l *StrategyLifecycle) ShadowSampler() *ShadowSampler {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sampler
}

// WithShadowGateDisabled suppresses the automatic G2 registration for the
// documented no-scorer-plus-armed-rollback case (evolution loop closure E2).
// It is deliberately explicit: the gate's absence must be a decision at the
// wiring layer (ShadowGateMode's three-branch invariant), never an emergent
// property of nil-checking. The reason is stored and reported by the snapshot
// and the startup log — an absent gate must be visible to be auditable.
func WithShadowGateDisabled(reason string) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.shadowGateSkipReason = reason
	}
}

// WithLifecycleMetrics attaches Prometheus metrics for promote/rollback
// counters (P2-1).
func WithLifecycleMetrics(m *observability.PrometheusMetrics) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.metrics = m
	}
}

// WithCompileInfoProvider wires the C5.2 compile provenance source so the
// LifecycleSnapshot map carries (compile_id, dag_version, compile_count)
// alongside the generation and gates. This closes the attribution chain:
// /api/evolution/lifecycle can answer "which generation, which gate, which
// compile" in a single endpoint call.
//
// The provider is typically a *planprojection.CompileCoordinator adapted
// into the CompileInfoProvider interface by the cmd/ares wiring layer (the
// adaptation breaks what would be a circular import).
func WithCompileInfoProvider(provider CompileInfoProvider) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.compileInfo = provider
	}
}

// SetCompileInfoProvider wires the C5.2 compile provenance source at runtime.
// This is the post-construction wiring path: the CompileCoordinator is created
// after the lifecycle (it needs the live DAG which is built after bootstrap),
// so the provider must be injected after both are constructed. Called from
// the serve wiring layer (cmd/ares) once the compile coordinator is available.
//
// Thread-safe: the compileInfo field is an interface reference (a pointer-sized
// word) set once after construction and never read concurrently with a write
// (the Snapshot method runs only after wiring is complete).
func (l *StrategyLifecycle) SetCompileInfoProvider(provider CompileInfoProvider) {
	if l == nil || provider == nil {
		return
	}
	l.mu.Lock()
	l.compileInfo = provider
	l.mu.Unlock()
}

// WithLifecycleEvidenceStore attaches the shared evidence store so the
// lifecycle can read runtime fitness evidence and write promote/rollback
// events. It also injects the store into the RuntimeFitnessAggregator so
// its Window queries return real evidence instead of always returning
// ok=false (the aggregator may have been created with a nil store at
// NewWiredEvolutionSystem time because the shared store is not yet known).
func WithLifecycleEvidenceStore(store evidence.Store) LifecycleOption {
	return func(l *StrategyLifecycle) {
		l.evStore = store
		if l.agg != nil {
			l.agg.SetStore(store)
		}
	}
}

// NewStrategyLifecycle creates the sole strategy orchestrator. It wraps the
// ActiveStrategyManager (which owns Deploy/Rollback) so the lifecycle is
// the only caller of those methods (B1 fix).
func NewStrategyLifecycle(
	asm *ActiveStrategyManager,
	agg *RuntimeFitnessAggregator,
	cfg LifecycleConfig,
	opts ...LifecycleOption,
) *StrategyLifecycle {
	l := &StrategyLifecycle{
		asm:       asm,
		agg:       agg,
		cfg:       cfg,
		blacklist: make(map[string]int),
		state:     StateActive, // start in active state (no candidate pending)
	}
	for _, opt := range opts {
		opt(l)
	}
	// G2: when a ShadowEvaluator is wired, register the shadow verify gate
	// ahead of any explicitly supplied gates so the pipeline order is
	// G2 shadow → G3 eval → ... (B3 fix: previously the evaluator was
	// assigned to l.shadow but never read by the promote pipeline).
	// Exception (E2): WithShadowGateDisabled suppresses the registration for
	// the no-scorer-plus-armed-rollback case — the gate would otherwise
	// reject every candidate forever, while canary + automatic rollback
	// carries the promotion risk.
	if l.shadow != nil && l.shadowGateSkipReason == "" {
		l.gates = append([]VerifyGate{shadowVerifyGate{l}}, l.gates...)
	}
	return l
}

// shadowVerifyGate adapts the lifecycle's ShadowEvaluator into the G2 verify
// gate. It is deliberately read-only: ShouldDeploy consults the comparisons
// that the data feeder — DreamCycle's shadow flow, or the P0-9 task-level
// ShadowSampler when DreamCycle is disabled — recorded via
// StartShadow/RecordResult. The gate never calls StartShadow itself — that
// would reset accumulated comparisons on every Submit and destroy the
// evidence it is supposed to judge.
//
// SEMANTICS (review blocking item 1, resolved in favor of fail-closed): with
// zero comparisons the gate REJECTS, mirroring design doc §3.1 ("fewer than
// MinSamples samples → the candidate stays in SHADOW and is NOT deployed").
// Passing candidates without any shadow evidence
// made the whole verify pipeline a no-op in default configs (DreamCycle is
// disabled, so nothing feeds comparisons) — the previous "skip" branch
// silently reduced Submit to unconditional promote. The fail-closed branch is
// still reachable when the P0-9 sampler is wired but has NO independent scorer
// (default bootstrap: LLM scoring off): the sampler deliberately produces zero
// comparisons rather than fabricate evidence.
type shadowVerifyGate struct{ l *StrategyLifecycle }

func (g shadowVerifyGate) Name() string { return "shadow" }

func (g shadowVerifyGate) Check(_ context.Context, _ *mutation.Strategy, _ *mutation.Strategy) (bool, float64, string) {
	se := g.l.shadow
	if se == nil {
		// Unreachable when registered via NewStrategyLifecycle (the gate is
		// only appended when l.shadow != nil); kept nil-safe anyway.
		return true, 0, "shadow evaluator not wired, skipping"
	}
	ok, report := se.ShouldDeploy()
	// P2-1: publish the win rate from THIS gate, not only from DreamCycle's
	// shadow flow. Bootstrap runs with EnableDreamCycle=false and the
	// scheduler drives popAdapter.Run → lifecycle.Submit, so the DreamCycle
	// write point never executes in production and the gauge would stay
	// permanently zero. This is the gate the promote decision actually goes
	// through, so it is the authoritative source for the gauge.
	if g.l.metrics != nil && report != nil {
		g.l.metrics.SetEvolutionShadowWinRate(report.WinRate)
	}
	if report == nil {
		// FAIL-CLOSED: no shadow evidence at all — no comparison was ever
		// gathered. The gate cannot vouch for the candidate, so it does not
		// pass. See the type comment — this branch is the difference between a
		// verify pipeline and a rubber stamp.
		return false, 0, "no shadow comparisons recorded — fail-closed (no independent scorer wired)"
	}
	if report.TotalComparisons == 0 {
		// FAIL-CLOSED with a distinction (P0-3/P2): comparisons WERE gathered
		// but every one was an exact tie (e.g. cold-start prior-vs-prior on an
		// empty/sparse evidence store). Report the tie count so the operator
		// can tell "no evidence" (nil above) from "gathered but uninformative".
		return false, 0, fmt.Sprintf(
			"shadow evidence is all ties (%d comparisons, 0 decisive) — fail-closed: no decisive evidence the candidate is better",
			report.TieCount,
		)
	}
	if ok {
		return true, report.WinRate,
			fmt.Sprintf("shadow win rate %.2f over %d comparisons meets threshold", report.WinRate, report.TotalComparisons)
	}
	return false, report.WinRate,
		fmt.Sprintf("shadow win rate %.2f over %d comparisons below threshold (insufficient samples counts as fail)", report.WinRate, report.TotalComparisons)
}

// Start launches the rollback watch loop. It is idempotent. The loop runs
// until ctx is cancelled or Stop is called; Stop waits for the loop goroutine
// to exit so the lifecycle never leaks or races a late rollback (K3: managed
// goroutine pair, no fire-and-forget).
func (l *StrategyLifecycle) Start(ctx context.Context) {
	if l == nil || !l.cfg.Enabled {
		return
	}
	l.mu.Lock()
	if l.cancel != nil {
		l.mu.Unlock()
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})
	done := l.done
	l.mu.Unlock()

	go func() {
		// K3: production background goroutines must not die silently or take
		// the process down on a bug — recover, log, and exit cleanly.
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(context.Background(), "watch loop panicked",
					"method", "watch", "error", fmt.Errorf("panic: %v", r))
			}
			close(done)
		}()
		l.watch(watchCtx)
	}()
}

// Stop cancels the watch loop and waits for it to exit.
func (l *StrategyLifecycle) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.cancel = nil
	l.done = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Submit is the single entry point for GA to propose a new strategy. It
// replaces the old deployBestStrategy unconditional Deploy call (B2 fix).
// The candidate goes through the verify-gate pipeline before being
// promoted to ACTIVE. If any gate fails, the candidate is discarded and the
// active strategy remains unchanged.
//
// Two special cases:
//
//   - Seed deploy: when NO strategy is active yet there is nothing to
//     shadow-compare against, so the first candidate is promoted without
//     gates (it becomes the baseline that §9 relies on as "previous"). Every
//     subsequent candidate must earn promotion through the gates.
//   - Manual approval (P2-4): when RequireManualApproval is set, the
//     candidate is HELD in SHADOW and Submit RETURNS immediately — the
//     candidate waits, never the caller's goroutine (the ticker/adapter
//     path must not block on human latency).
func (l *StrategyLifecycle) Submit(ctx context.Context, candidate *mutation.Strategy, generation int) {
	if l == nil || !l.cfg.Enabled || candidate == nil {
		return
	}

	// Seed deploy: no active strategy → nothing to verify against. Promote
	// unconditionally so the baseline exists (design doc §9: the seed
	// baseline is always available as `previous`).
	//
	// The exemption is a ONE-SHOT flag, not derived from asm.Current()==nil:
	// if the ASM were ever reset (or its store emptied) mid-flight, a
	// Current()==nil test would let the next candidate skip ALL gates again.
	// Once seeded, every candidate must earn promotion (review fix #5).
	// Note: an ASM that ALREADY holds an externally deployed strategy is
	// "born seeded" — the first Submit runs the gates against it.
	hasActive := l.asm != nil && l.asm.Current() != nil

	l.mu.Lock()
	if !l.seeded {
		l.seeded = true
		if !hasActive {
			l.mu.Unlock()
			log.InfoContext(ctx, "no active strategy, promoting seed baseline without gates",
				"method", "lifecycle.Submit",
				"strategy_id", candidate.ID,
				"generation", generation,
			)
			l.promote(ctx, candidate, false)
			return
		}
	}

	// Promote throttle (E2): a promoted strategy must stay active for
	// MinActiveDuration before another candidate may replace it. Without it
	// the GA ticker rotates strategies faster than the rollback window
	// accumulates evidence — degradation becomes undetectable in principle.
	// Checked BEFORE the gate chain so a throttled candidate never burns gate
	// evaluations; the rejection is observable on the shared gate-reject
	// counter under the min_active_duration gate name.
	if wait := l.residencyRemainingLocked(time.Now()); wait > 0 {
		l.mu.Unlock()
		reason := fmt.Sprintf("min active duration not elapsed (%s remaining)", wait.Truncate(time.Second))
		log.InfoContext(ctx, "candidate rejected: promote throttle", "method", "lifecycle.Submit",
			"strategy_id", candidate.ID,
			"generation", generation,
			"reason", reason,
		)
		l.recordGateReject(gateMinActiveDuration, reason)
		return
	}

	// A candidate is already awaiting manual approval: reject new
	// submissions until an operator decides (replacing the held candidate
	// silently would defeat the gate).
	if l.pendingApproval {
		l.mu.Unlock()
		log.InfoContext(ctx, "manual approval pending, rejecting new candidate", "method", "lifecycle.Submit",
			"strategy_id", candidate.ID,
			"generation", generation,
			"held_id", l.heldCandidateIDLocked(),
		)
		return
	}

	// Check blacklist: candidates rolled back within the ban window
	// (rollBackGen + N generations) are banned from re-nomination (§9
	// rollback-oscillation damping). Entries are pruned once the submitted
	// generation reaches the ban-lift generation.
	for id, banUntil := range l.blacklist {
		if banUntil <= generation {
			delete(l.blacklist, id)
		}
	}
	if banUntil, blacklisted := l.blacklist[candidate.ID]; blacklisted && generation < banUntil {
		l.mu.Unlock()
		log.InfoContext(ctx, "candidate is blacklisted, skipping", "method", "lifecycle.Submit",
			"strategy_id", candidate.ID,
			"generation", generation,
			"ban_until_generation", banUntil,
		)
		return
	}
	l.state = StateCandidate
	l.currentCandidate = candidate
	l.generation = generation
	l.mu.Unlock()

	log.InfoContext(ctx, "candidate submitted", "method", "lifecycle.Submit",
		"strategy_id", candidate.ID,
		"generation", generation,
		"score", candidate.Score,
	)

	active := l.asm.Current()

	// P0-9: prime the task-level shadow feeder (when wired) so the G2 gate
	// has candidate-vs-active comparison evidence to judge. Must run AFTER
	// the candidate record is set and BEFORE the gates. No-op when no
	// sampler is wired or no independent scorer exists (stays fail-closed).
	if l.sampler != nil {
		l.sampler.Prime(ctx, candidate, active)
	}

	// Run the verify-gate pipeline.
	for _, gate := range l.gates {
		pass, score, reason := gate.Check(ctx, candidate, active)
		if !pass {
			log.InfoContext(ctx, "gate rejected candidate", "method", "lifecycle.Submit",
				"gate", gate.Name(),
				"strategy_id", candidate.ID,
				"score", score,
				"reason", reason,
			)
			l.recordGateReject(gate.Name(), reason)
			l.mu.Lock()
			l.state = StateActive
			l.currentCandidate = nil
			l.mu.Unlock()
			return
		}
		log.DebugContext(ctx, "gate passed", "method", "lifecycle.Submit",
			"gate", gate.Name(),
			"strategy_id", candidate.ID,
			"score", score,
		)
	}

	// P2-4: when manual approval is required, HOLD the candidate in SHADOW
	// and return immediately. The candidate sits pending until Approve()
	// promotes it (or a later Submit replaces it after approval/rejection).
	// Blocking here would stall the whole evolution heartbeat: the call
	// chain is bootstrap ticker → scheduler.Tick → adapter.Run → Submit,
	// and a human response can take hours.
	if l.cfg.Gates.RequireManualApproval {
		l.mu.Lock()
		l.state = StateShadow
		l.pendingApproval = true
		l.heldCandidate = candidate
		l.heldGeneration = generation
		l.mu.Unlock()
		log.InfoContext(ctx, "candidate held for manual approval", "method", "lifecycle.Submit",
			"strategy_id", candidate.ID,
			"generation", generation,
		)
		return
	}

	// All gates passed (and no hold requested): promote to ACTIVE.
	l.promote(ctx, candidate, true)
}

// residencyRemainingLocked reports how much of the current strategy's minimum
// active duration is still outstanding (0 = the next candidate may be judged).
// Caller holds l.mu. A zero activeSince (externally deployed or seed baseline)
// never throttles: there is no judged promote to protect yet.
func (l *StrategyLifecycle) residencyRemainingLocked(now time.Time) time.Duration {
	if l.activeSince.IsZero() {
		return 0
	}
	d := l.cfg.minActiveDuration(l.cfg.WatchInterval)
	if d <= 0 {
		return 0
	}
	if elapsed := now.Sub(l.activeSince); elapsed >= d {
		return 0
	} else {
		return d - elapsed
	}
}

// heldCandidateIDLocked returns the held candidate's ID; caller holds l.mu.
func (l *StrategyLifecycle) heldCandidateIDLocked() string {
	if l.heldCandidate == nil {
		return ""
	}
	return l.heldCandidate.ID
}

// Approve promotes the candidate held in SHADOW by RequireManualApproval
// (P2-4). It is a no-op when no candidate is pending.
//
// Concurrency (review fix #2): "take and clear" happen in ONE critical
// section, so exactly one caller of N concurrent approvals receives the
// candidate and promotes it — the losers return with cand == nil. The
// previous two-phase (read → unlock → promote) let two concurrent
// POST /api/evolution/approve calls both promote the same strategy, which
// made ActiveStrategyManager set previous = current = that strategy:
// subsequent rollbacks would "succeed" while restoring the strategy to
// itself, and degradation could never be undone. The HTTP handler's 409
// pre-check stays purely as a friendlier early error, not a correctness
// device. Approve carries no request context, so the promote runs under a
// bounded background context.
func (l *StrategyLifecycle) Approve() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cand := l.heldCandidate
	l.heldCandidate = nil
	l.pendingApproval = false
	l.mu.Unlock()
	if cand == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l.promote(ctx, cand, true)
}

// promote deploys the candidate as the new active strategy and resets the
// rollback window (B1 fix: previous is preserved by ActiveStrategyManager.Deploy).
// startResidency controls whether this promote starts the MinActiveDuration
// clock: gated Submit/Approve promotions do, the one-shot seed deploy does not
// (it is the §9 baseline, not a judged promote).
func (l *StrategyLifecycle) promote(ctx context.Context, candidate *mutation.Strategy, startResidency bool) {
	if l == nil || l.asm == nil {
		return
	}
	if err := l.asm.Deploy(ctx, candidate); err != nil {
		log.WarnContext(ctx, "deploy failed, keeping current active", "method", "lifecycle.promote",
			"strategy_id", candidate.ID,
			"error", err,
		)
		l.mu.Lock()
		l.state = StateActive
		l.currentCandidate = nil
		l.lastDecision = fmt.Sprintf("deploy_failed: %s", err)
		l.lastWindowAt = time.Time{}
		l.mu.Unlock()
		// Deploy can internally roll the ASM back to `previous` when the
		// post-evolve guardrail stops it — the ACTIVE strategy may have
		// changed even though this promote failed. Reset the rollback
		// window so the (possibly new) active strategy is not judged on the
		// previous strategy's stale scores (same reasoning as the promote
		// path above; conservative direction).
		l.asm.RollbackPolicy().Reset()
		if l.metrics != nil {
			l.metrics.RecordEvolutionPromote("deploy_failed")
		}
		return
	}
	l.mu.Lock()
	l.state = StateActive
	l.currentCandidate = candidate
	l.lastDecision = "promoted"
	// §8 general item 5: reset the rollback window on EVERY promote, not
	// only on rollback. The old strategy's low scores are still in
	// scoreHistory right after a promote; without the reset the new strategy
	// could be judged as a sudden drop on its very first watch tick using
	// the PREVIOUS strategy's evidence. The decorrelation timestamp resets
	// with it so the new strategy records on its first tick.
	l.lastWindowAt = time.Time{}
	if startResidency {
		l.activeSince = time.Now()
	}
	l.mu.Unlock()

	l.asm.RollbackPolicy().Reset()

	if l.metrics != nil {
		l.metrics.RecordEvolutionPromote("success")
		l.metrics.RecordEvolutionDeploy("promoted")
	}
	l.writeDecisionEvidence(ctx, "promote", candidate.ID, candidate.Score, "")
	log.InfoContext(ctx, "strategy promoted to active", "method", "lifecycle.promote",
		"strategy_id", candidate.ID,
		"score", candidate.Score,
	)
}

// watch is the background loop that feeds runtime samples into the
// RollbackPolicy and triggers Rollback when degradation is detected (B1 fix).
// The rollback window itself is reset on every promote (see promote) so the
// new strategy is judged from a clean baseline.
func (l *StrategyLifecycle) watch(ctx context.Context) {
	interval := l.cfg.WatchInterval
	if interval <= 0 {
		interval = defaultWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.recordWatchGauges()
			l.evaluateAndMaybeRollback(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// recordWatchGauges publishes the per-tick observability gauges (E6): how
// long the current strategy has been active. Purely observational — it must
// never influence the rollback decision.
func (l *StrategyLifecycle) recordWatchGauges() {
	if l.metrics == nil {
		return
	}
	l.mu.Lock()
	since := l.activeSince
	l.mu.Unlock()
	if since.IsZero() {
		return
	}
	l.metrics.SetEvolutionActiveDuration(time.Since(since).Seconds())
}

// evaluateAndMaybeRollback queries the aggregator for the current window
// fitness, feeds it into RollbackPolicy.RecordScore, and triggers Rollback
// when degradation is detected.
func (l *StrategyLifecycle) evaluateAndMaybeRollback(ctx context.Context) {
	if l.agg == nil || l.asm == nil {
		return
	}
	// Rollback disarm (E2): the YAML rollback.enabled=false path removes the
	// post-deployment safety net by explicit operator decision. The watch
	// loop then records nothing and never triggers — promotion risk was
	// accepted up front (and the shadow-gate invariant re-arms G2 fail-closed
	// for exactly this configuration).
	if !l.cfg.RollbackArmed {
		return
	}

	active := l.asm.Current()
	if active == nil {
		return
	}

	res := l.agg.Window(ctx, active.ID)
	if !res.Ok || res.Count == 0 {
		return
	}
	// E6: window-sample gauge split by strategy — attribution health is
	// visible at a glance. If E1's stamping ever breaks, all samples pile up
	// under one strategy_id label value instead of distributing.
	if l.metrics != nil {
		l.metrics.SetEvolutionWindowSamples(active.ID, "strategy", res.Count)
	}
	l.mu.Lock()
	// §8 general item 6 (decorrelation): record a score ONLY when the
	// evidence window advanced since the previous tick. Re-averaging the
	// same batch of evidence every tick would make the RollbackPolicy
	// window a set of highly self-correlated copies of one snapshot —
	// sudden drops get smoothed away and the gradual-decline detector
	// fires on noise.
	//
	// The advance signal is the window's NEWEST evidence timestamp, NOT
	// the record count: each source's count saturates at WindowSize and
	// then stays flat under steady-state churn ("one in, one out") — a
	// count-based check silently stops feeding RollbackPolicy forever
	// once every source fills up (no error, no warning; /api/evolution/
	// lifecycle would even show a healthy window_count). The timestamp is
	// reset to zero on promote so the new strategy records on its first
	// tick.
	if !res.LastAt.After(l.lastWindowAt) {
		l.mu.Unlock()
		return
	}
	l.lastWindowAt = res.LastAt
	gen := l.generation
	l.mu.Unlock()

	// Clamp to [0,1] before feeding RollbackPolicy (B1 fix: dimensional
	// consistency — RollbackPolicy threshold is 0.15 on a [0,1] scale).
	score := clamp01(res.Mean)

	l.asm.RecordScore(gen, score)

	// Evaluate degradation.
	decision := l.asm.RollbackPolicy().Evaluate()
	if decision == nil || !decision.ShouldRollback {
		return
	}

	// Trigger rollback.
	prev, err := l.asm.Rollback(ctx)
	if err != nil {
		if errors.Is(err, ErrNoPreviousStrategy) {
			// Expected in the fail-closed default config: only the seed
			// deploy has happened, so previous is still nil. Log at Info
			// (with the expectation stated) instead of Warn — a long soak
			// would otherwise flood the log with a non-malfunction.
			log.InfoContext(ctx, "rollback unavailable: no previous strategy yet (expected before the second promote)",
				"method", "lifecycle.watch",
				"active_id", active.ID,
			)
			if l.metrics != nil {
				l.metrics.RecordEvolutionRollback("no_previous")
			}
			return
		}
		log.WarnContext(ctx, "rollback failed", "method", "lifecycle.watch",
			"active_id", active.ID,
			"error", err,
		)
		if l.metrics != nil {
			l.metrics.RecordEvolutionRollback("failed")
			l.metrics.RecordEvolutionDeploy("rollback_failed")
		}
		return
	}

	// Blacklist the degraded candidate for N generations (§9 oscillation
	// damping): banUntil = current generation + N. The next Submit prunes
	// the entry once its generation reaches banUntil.
	l.mu.Lock()
	if l.currentCandidate != nil {
		l.blacklist[l.currentCandidate.ID] = gen + l.cfg.blacklistGenerations()
	}
	rolledBackID := active.ID
	rolledBackScore := active.Score
	l.state = StateActive
	l.currentCandidate = nil
	l.lastDecision = fmt.Sprintf("rollback: %s", decision.Reason)
	// The restored strategy becomes active again: restart its residency clock
	// so the promote throttle protects its fresh evidence window too.
	l.activeSince = time.Now()
	l.mu.Unlock()

	// Reset the rollback window so the new (previous) strategy gets a
	// clean baseline.
	l.asm.RollbackPolicy().Reset()

	if l.metrics != nil {
		l.metrics.RecordEvolutionRollback("degradation")
		l.metrics.RecordEvolutionDeploy("rollback")
	}
	// P2-3 (closed by E3): write the rollback decision into the evidence
	// store. The consumer is the knowledge graph's EvolutionProvider
	// (internal/knowledge/provider/evolution/provider.go, decision-trail
	// segment) via adapter.FromDecisionEvidence. active.ID is the strategy
	// that was rolled back.
	l.writeDecisionEvidence(ctx, "rollback", rolledBackID, rolledBackScore, decision.Reason)
	log.InfoContext(ctx, "strategy rolled back", "method", "lifecycle.watch",
		"active_id", active.ID,
		"restored_id", prev.ID,
		"reason", decision.Reason,
		"degradation", decision.Degradation,
		"threshold", decision.Threshold,
	)
}

// Snapshot returns a point-in-time copy of the lifecycle state for
// observability (P2-2 HTTP endpoint).
//
// The aggregator Window query (evidence-store I/O) runs OUTSIDE l.mu: the
// mutex protects the state machine fields, and holding it across store I/O
// would stall Submit/Approve/Stop while the HTTP endpoint reads. The agg
// pointer itself is immutable after construction, so reading it without the
// lock is safe.
func (l *StrategyLifecycle) Snapshot() LifecycleState {
	if l == nil {
		return LifecycleState{State: "disabled"}
	}
	l.mu.Lock()
	snap := LifecycleState{
		State:           l.state.String(),
		Generation:      l.generation,
		LastDecision:    l.lastDecision,
		PendingApproval: l.pendingApproval,
		// E2/E6: gate configuration + promote-throttle posture, so an
		// operator can tell from ONE endpoint call which verification mode
		// is live and whether the rollback net is armed.
		Gates:                l.gateNamesLocked(),
		ShadowGateSkipReason: l.shadowGateSkipReason,
		ActiveSince:          l.activeSince,
		MinActiveDuration:    l.cfg.minActiveDuration(l.cfg.WatchInterval),
		RollbackArmed:        l.cfg.RollbackArmed,
	}
	if l.asm != nil {
		if cur := l.asm.Current(); cur != nil {
			snap.ActiveID = cur.ID
		}
		if prev := l.asm.Previous(); prev != nil {
			snap.PreviousID = prev.ID
		}
	}
	if l.currentCandidate != nil {
		snap.ShadowID = l.currentCandidate.ID
	}
	if l.heldCandidate != nil {
		snap.HeldID = l.heldCandidate.ID
		snap.HeldGeneration = l.heldGeneration
	}
	// Capture compileInfo under the lock for consistent reads. The
	// provider itself is thread-safe, so its methods can be called outside
	// the lock — but the field reference must be read consistently.
	compileInfo := l.compileInfo
	l.mu.Unlock()

	if l.agg != nil {
		// §8 general item 8: the Window query is evidence-store I/O on the
		// HTTP snapshot path — always bounded so a slow store cannot hang
		// the endpoint. On timeout the fields stay zero (no fabricated
		// score).
		wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer wcancel()
		res := l.agg.Window(wctx, snap.ActiveID)
		snap.WindowScore = res.Mean
		snap.WindowCount = res.Count
	}
	// C5.2: compile provenance for the attribution chain. When not wired,
	// the fields stay zero-valued.
	if compileInfo != nil {
		snap.CompileID = compileInfo.CompileID()
		snap.DAGVersion = compileInfo.DAGVersion()
		snap.CompileCount = compileInfo.CompileCount()
	}
	return snap
}

// LifecycleSnapshot returns the lifecycle state as a JSON-friendly map
// for the introspect ControlServer /api/evolution/lifecycle endpoint
// (P2-2). It satisfies the introspect.LifecycleSnapshotProvider interface
// without creating an import cycle (introspect does not import ares_evolution).
//
// Naming note: the METHOD keeps the introspect interface's name; the state
// struct it returns was renamed LifecycleSnapshot → LifecycleState so the
// type and the method no longer share one identifier.
func (l *StrategyLifecycle) LifecycleSnapshot() map[string]any {
	snap := l.Snapshot()
	m := map[string]any{
		"active_id":    snap.ActiveID,
		"state":        snap.State,
		"window_score": snap.WindowScore,
		"window_count": snap.WindowCount,
		"generation":   snap.Generation,
	}
	if snap.PreviousID != "" {
		m["previous_id"] = snap.PreviousID
	}
	if snap.ShadowID != "" {
		m["shadow_id"] = snap.ShadowID
	}
	if snap.LastDecision != "" {
		m["last_decision"] = snap.LastDecision
	}
	if snap.PendingApproval {
		m["pending_approval"] = true
		m["held_id"] = snap.HeldID
		m["held_generation"] = snap.HeldGeneration
	}
	// E2/E6: gate pipeline visibility + promote-throttle posture. Rendered
	// as seconds so the JSON stays human-readable across language bindings.
	if len(snap.Gates) > 0 {
		m["gates"] = snap.Gates
	}
	if snap.ShadowGateSkipReason != "" {
		m["shadow_gate_skipped_reason"] = snap.ShadowGateSkipReason
	}
	if !snap.ActiveSince.IsZero() {
		m["active_since"] = snap.ActiveSince.Format(time.RFC3339)
	}
	if snap.MinActiveDuration > 0 {
		m["min_active_duration"] = snap.MinActiveDuration.Seconds()
	}
	m["rollback_armed"] = snap.RollbackArmed
	// C5.2: compile provenance for the attribution chain. The triplet
	// (generation, gates, compile_id) answers "which generation, which
	// gate, which compile" in a single endpoint call.
	m["dag_version"] = snap.DAGVersion
	m["compile_count"] = snap.CompileCount
	if snap.CompileID != "" {
		m["compile_id"] = snap.CompileID
	}
	return m
}

// gateNamesLocked returns the registered gate names in pipeline order.
// Caller holds l.mu.
func (l *StrategyLifecycle) gateNamesLocked() []string {
	if len(l.gates) == 0 {
		return nil
	}
	names := make([]string, 0, len(l.gates))
	for _, g := range l.gates {
		names = append(names, g.Name())
	}
	return names
}

// recordGateReject increments the gate-reject metric (P2-1) and records
// the decision trail (C3.3: every promote/reject must leave a trace with
// {generation, gate, reason, win_rate}).
func (l *StrategyLifecycle) recordGateReject(gateName, reason string) {
	// C3.3: record the rejection in the decision trail. The generation and
	// win_rate are best-effort: the lifecycle may not know the gate's score
	// at this call site (the gate's Check already returned), so we record
	// what we have.
	l.mu.Lock()
	gen := l.generation
	l.mu.Unlock()

	l.writeDecisionEvidence(context.Background(), "reject",
		"", 0, fmt.Sprintf("gate=%s gen=%d reason=%s", gateName, gen, reason))

	if l.metrics == nil {
		return
	}
	l.metrics.RecordEvolutionGateReject(gateName)
	// Also fire the legacy guardrail counter for backward-compatible
	// dashboards that still watch ARES_evolution_guardrail_total. The code
	// label is a FIXED constant: interpolating the gate name would give the
	// legacy counter unbounded label cardinality (gate names are
	// caller-supplied via VerifyGate.Name()).
	l.metrics.RecordEvolutionGuardrail("gate_reject")
}

// writeDecisionEvidence records promote/rollback decision events with
// source="lifecycle" so the knowledge graph's EvolutionProvider (P2-3,
// closed by E3) can consume the decision trail: the provider's Stream emits
// them as ObjectDecision objects via adapter.FromDecisionEvidence, filtered
// by Source=="lifecycle" plus the payload "action" field.
//
// The source is deliberately NOT "strategy" and the score is deliberately
// NOT normalized: GA scores live on a 0–100 scale, while every fitness
// consumer (RuntimeFitnessAggregator, recentFitnessSummary) filters
// KindFitness values to [0,1]. Writing a 0–100 GA score under the
// "strategy" fitness source would (a) be silently dropped by that filter
// and (b) mix one-off decision events into the runtime fitness window
// semantics. A dedicated source keeps the decision trail queryable without
// polluting either dimension.
//
// TODO(tech-debt): decision records share KindFitness with runtime fitness
// samples, distinguished only by Source. They should separate into a
// dedicated KindDecision in 0.4.x — AFTER confirming Window's sources table
// (fitness_aggregator.go: only strategy/workflow/scheduler/recovery) is
// unaffected. Today "lifecycle" is not in that table, so decision records
// never enter rollback math either way; the separation is a semantic
// cleanup, not a correctness fix.
func (l *StrategyLifecycle) writeDecisionEvidence(ctx context.Context, action, strategyID string, score float64, reason string) {
	if l.evStore == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"action":              action,
		"value":               score,
		evidenceKeyStrategyID: strategyID,
		"reason":              reason,
		"timestamp":           time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = l.evStore.Append(ctx, evidence.Evidence{
		// Full-date format: the PG store uses ON CONFLICT (id) DO NOTHING,
		// so a time-only suffix would silently drop decision events from
		// different days colliding on the same clock reading.
		ID:        "strategy_decision_" + action + "_" + strategyID + "_" + time.Now().Format("20060102150405.000000"),
		Source:    "lifecycle",
		Kind:      evidence.KindFitness,
		Payload:   payload,
		Timestamp: time.Now(),
	})
}

// clamp01 clamps a float64 to the [0,1] range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
