package evolution

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// contextKey is a custom type for context value keys to avoid collisions.
type contextKey string

// Context keys propagated from EventStore payloads into the evolution context.
const (
	contextKeyNameAgentID  = "agent_id"
	contextKeyNameTenantID = "tenant_id"
	contextKeyNameTraceID  = "trace_id"
)

// CallbackData holds data passed to callback handlers during evolution triggers.
type CallbackData struct {
	// AgentID is the identifier of the agent that triggered the event.
	AgentID string
}

// AdapterRunner defines the interface for running an evolution adapter.
// This allows the scheduler to work with any adapter implementation.
type AdapterRunner interface {
	// Run starts the adapter's event consumption loop.
	Run(ctx context.Context) error
}

// EvolutionTrigger defines when to trigger evolution cycles.
type EvolutionTrigger int

const (
	// TriggerOnIdle triggers evolution when the system is idle.
	TriggerOnIdle EvolutionTrigger = iota + 1

	// TriggerOnThreshold triggers evolution when diagnostic count exceeds threshold.
	TriggerOnThreshold

	// TriggerOnDemand triggers evolution only when explicitly requested.
	TriggerOnDemand
)

// String returns the string representation of EvolutionTrigger.
func (t EvolutionTrigger) String() string {
	switch t {
	case TriggerOnIdle:
		return "idle"
	case TriggerOnThreshold:
		return "threshold"
	case TriggerOnDemand:
		return "demand"
	default:
		return "unknown"
	}
}

// SchedulerOption configures the EvolutionScheduler.
type SchedulerOption func(*EvolutionScheduler)

// WithMinInterval sets the minimum interval between evolution cycles.
//
// Args:
//
//	d - the minimum duration between cycles.
//
// Returns:
//
//	SchedulerOption - the option function.
func WithMinInterval(d time.Duration) SchedulerOption {
	return func(s *EvolutionScheduler) {
		if d > 0 {
			s.minInterval = d
		}
	}
}

// WithTrigger sets the evolution trigger mode.
//
// Args:
//
//	trigger - the trigger mode to use.
//
// Returns:
//
//	SchedulerOption - the option function.
func WithTrigger(trigger EvolutionTrigger) SchedulerOption {
	return func(s *EvolutionScheduler) {
		s.trigger = trigger
	}
}

// WithSchedulerGuardrails attaches guardrails to the scheduler for pre-evolution checks.
//
// Args:
//
//	guardrails - the evolution guardrails instance (may be nil to disable).
//
// Returns:
//
//	SchedulerOption - the option function.
func WithSchedulerGuardrails(guardrails *EvolutionGuardrails) SchedulerOption {
	return func(s *EvolutionScheduler) {
		s.guardrails = guardrails
	}
}

// WithEnabled sets whether the scheduler is enabled.
//
// Args:
//
//	enabled - true to enable, false to disable.
//
// Returns:
//
//	SchedulerOption - the option function.
func WithEnabled(enabled bool) SchedulerOption {
	return func(s *EvolutionScheduler) {
		s.enabled.Store(enabled)
	}
}

// WithScoreProvider wires the C2.4 zero-LLM score provider. When set, each
// task.completed/failed event records the provider's aggregate score instead
// of the constant 1.0/0.0, so degradation detection reflects real execution
// quality (latency, retries, recovery) rather than just pass/fail.
//
// Args:
//
//	provider - the TaskScoreProvider to use (nil is a no-op).
//
// Returns:
//
//	SchedulerOption - the option function.
func WithScoreProvider(provider TaskScoreProvider) SchedulerOption {
	return func(s *EvolutionScheduler) {
		s.scoreProvider = provider
	}
}

// scoreWindowSize is the number of recent task scores to track for trend detection.
const scoreWindowSize = 50

// Task-outcome scores fed into the sliding window. The scheduler derives them
// directly from EventStore task events (completed → success, failed →
// failure), so degradation detection works on real production outcomes
// instead of requiring an external score feeder.
//
// Scores are normalized to [0,1] to be dimensionally consistent with
// RollbackPolicy thresholds (B1 fix: the threshold 0.15 is on a [0,1] scale).
//
// C2.4: when a ScoreProvider is wired, the scheduler uses the provider's
// aggregate score instead of the constant 1.0/0.0. The provider reads
// execution attribution (latency, retries, recovery) so the score window
// reflects real execution quality, not just pass/fail. The constants remain
// as the fallback when no provider is wired (backward compatible).
const (
	// taskScoreSuccess is the fallback score for a completed task (no provider).
	taskScoreSuccess = 1.0
	// taskScoreFailure is the fallback score for a failed task (no provider).
	taskScoreFailure = 0.0
)

// TaskScoreProvider supplies a deterministic [0,1] score for task outcomes.
// When wired into the EvolutionScheduler (C2.4), each task.completed or
// task.failed event records the provider's current aggregate score instead
// of the constant 1.0/0.0, so degradation detection reflects real execution
// quality (latency, retries, recovery) rather than just pass/fail.
//
// The provider must be thread-safe (the subscription loop calls it
// concurrently with the drain loop). Returns the neutral 0.5 when no
// attribution data is available yet.
type TaskScoreProvider interface {
	// TaskScore returns a [0,1] score for the given task outcome.
	TaskScore(success bool) float64
}

// degradationThreshold is the fraction of score drop that triggers evolution (15%).
// On a [0,1] scale this is 0.15 — dimensionally consistent with
// RollbackPolicy.DegradationThreshold (B1 fix).
const degradationThreshold = 0.15

// minScoreCountForReliability is the minimum number of scores required before
// the trend data is considered reliable enough for evolution decisions.
const minScoreCountForReliability = 20

// periodicEvolutionScoreThreshold is the score count threshold that triggers
// periodic exploration evolution even without detected degradation.
const periodicEvolutionScoreThreshold = 100

// EvolutionScheduler triggers evolution cycles based on agent lifecycle
// events. It subscribes to the shared EventStore (filtering on
// EventAgentStopped plus task outcome events) and decides when to run the
// adapter based on configurable trigger conditions. Task completed/failed
// events feed the score window via RecordScore, giving TriggerOnThreshold /
// TriggerOnIdle degradation detection a real production score source.
type EvolutionScheduler struct {
	// subscriber is the event store subscription source. Agent lifecycle
	// events (agent.started / agent.stopped) and task outcome events
	// (task.completed / task.failed) are emitted to the EventStore,
	// NOT to the ares_callbacks registry, so the scheduler must listen here.
	subscriber   EventStoreSubscriber
	adapter      AdapterRunner
	minInterval  time.Duration
	mu           sync.Mutex
	lastRun      time.Time
	trigger      EvolutionTrigger
	enabled      atomic.Bool
	evolveMu     sync.Mutex
	evolveCancel context.CancelFunc
	evolveEg     *errgroup.Group // stored for Shutdown to wait on

	// subMu guards subCancel and subEg for the subscription loop.
	subMu     sync.Mutex
	subCancel context.CancelFunc
	subEg     *errgroup.Group // stored for Shutdown to wait on

	dreamCycle *DreamCycle
	scores     []float64
	scoreMu    sync.Mutex
	guardrails *EvolutionGuardrails

	// scoreProvider is the C2.4 zero-LLM score source. When wired, each
	// task.completed/failed event records the provider's aggregate score
	// instead of the constant 1.0/0.0. Nil falls back to the constants
	// (backward compatible).
	scoreProvider TaskScoreProvider
}

// NewEvolutionScheduler creates a new scheduler with sensible defaults.
//
// Default configuration:
//   - minInterval: 5 minutes
//   - trigger: TriggerOnIdle
//   - enabled: false (must be explicitly enabled)
//
// Args:
//
//	subscriber - the event store subscriber for listening to agent lifecycle
//	   events (implements EventStoreSubscriber; typically an ares_events.EventStore).
//	adapter - the adapter runner to execute on evolution cycles (implements AdapterRunner).
//	opts - optional configuration functions.
//
// Returns:
//
//	*EvolutionScheduler - the configured scheduler instance.
func NewEvolutionScheduler(subscriber EventStoreSubscriber, adapter AdapterRunner, opts ...SchedulerOption) *EvolutionScheduler {
	s := &EvolutionScheduler{
		subscriber:  subscriber,
		adapter:     adapter,
		minInterval: 5 * time.Minute,
		lastRun:     time.Time{},
		trigger:     TriggerOnIdle,
	}
	// enabled defaults to false (atomic.Bool zero value).

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// RecordScore adds a task score to the sliding window for trend detection.
// Thread-safe. Keeps only the most recent scoreWindowSize scores.
//
// The score is clamped to [0,1]: the window feeds degradation detection on
// the same scale as RollbackPolicy.DegradationThreshold (0.15), so an
// out-of-range score would silently corrupt every trend computation
// (dimensional consistency contract, design doc §9).
//
// Args:
//
//	score - the task execution score, normalized to [0,1]. Out-of-range
//	   values are clamped, not rejected, so a mis-scaled caller degrades
//	   to "always best/always worst" instead of poisoning the window.
func (s *EvolutionScheduler) RecordScore(score float64) {
	s.scoreMu.Lock()
	defer s.scoreMu.Unlock()

	if score < taskScoreFailure {
		score = taskScoreFailure
	}
	if score > taskScoreSuccess {
		score = taskScoreSuccess
	}

	if len(s.scores) >= scoreWindowSize {
		n := make([]float64, scoreWindowSize-1)
		copy(n, s.scores[1:])
		s.scores = n
	}
	s.scores = append(s.scores, score)
}

// SetAdapter replaces the evolution adapter at runtime.
// Used by bootstrap to wire the GA population adapter after construction.
//
// Args:
//
//	adapter - the new adapter to use for evolution cycles.
func (s *EvolutionScheduler) SetAdapter(adapter AdapterRunner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapter = adapter
}

// OnAgentEnd handles agent completion events as a callback handler.
// It checks if an evolution cycle should be triggered and runs the adapter if so.
//
// Args:
//
//	ctx - operation context.
//	data - the callback data containing agent completion information.
func (s *EvolutionScheduler) OnAgentEnd(ctx context.Context, data CallbackData) {
	if !s.enabled.Load() {
		return
	}

	if s.adapter == nil {
		log.WarnContext(ctx, "[Evolution] Adapter is nil, skipping evolution")
		return
	}

	if !s.shouldEvolve(ctx, data) {
		return
	}

	if !s.checkGuardrails(ctx) {
		return
	}

	s.mu.Lock()
	triggerStr := s.trigger.String()
	s.mu.Unlock()

	log.InfoContext(ctx, "[Evolution] Starting evolution cycle",
		"agent_id", data.AgentID,
		"trigger", triggerStr)

	// Cancel any previously running evolution before starting a new one
	// to prevent concurrent evolution cycles and goroutine leaks.
	{
		s.evolveMu.Lock()
		if s.evolveCancel != nil {
			s.evolveCancel()
		}
		s.evolveMu.Unlock()
	}

	// Run the adapter asynchronously via errgroup with context for cancellation support.
	// lastRun is only updated after successful completion so that failures
	// do not incorrectly trigger the cooldown timer and suppress retries.
	egCtx, egCancel := context.WithCancel(ctx)
	eg, _ := errgroup.WithContext(egCtx)

	s.evolveMu.Lock()
	s.evolveCancel = egCancel
	s.evolveEg = eg
	s.evolveMu.Unlock()

	eg.Go(func() error {
		if err := s.adapter.Run(egCtx); err != nil {
			log.ErrorContext(ctx, "[Evolution] Evolution cycle failed",
				"agent_id", data.AgentID,
				"error", err)
			return err
		}
		// Update lastRun only after successful evolution.
		s.mu.Lock()
		s.lastRun = time.Now()
		s.mu.Unlock()
		return nil
	})

	// OnAgentEnd must return immediately (it's a callback handler), so the
	// errgroup is not waited on here. The errgroup is stored in s.evolveEg
	// so Shutdown() can wait for it. Any error from eg.Go is already logged
	// inside the goroutine above, and Shutdown() observes the aggregated
	// error via eg.Wait().
}

// Register subscribes the scheduler to the EventStore for agent lifecycle
// events. It listens for EventAgentStopped (the event that agents actually
// emit when they finish) so evolution cycles fire on real agent completion.
// The subscription runs in a managed goroutine until Shutdown cancels its
// context; the EventStore closes the channel on cancellation.
func (s *EvolutionScheduler) Register() {
	if s.subscriber == nil {
		log.Warn("[Evolution] Event store subscriber is nil, cannot register")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := s.subscriber.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventAgentStopped,
			ares_events.EventTaskCompleted,
			ares_events.EventTaskFailed,
		},
	})
	if err != nil {
		log.Warn("[Evolution] Failed to subscribe to agent stopped events", "error", err)
		cancel()
		return
	}

	s.subMu.Lock()
	s.subCancel = cancel
	s.subEg = new(errgroup.Group)
	s.subMu.Unlock()

	s.subEg.Go(func() error {
		defer log.Info("[Evolution] Scheduler subscription loop stopped")
		for evt := range ch {
			if evt == nil {
				continue
			}
			switch evt.Type {
			case ares_events.EventAgentStopped:
				s.OnAgentEnd(contextFromEvent(evt), CallbackData{AgentID: evt.StreamID})
			case ares_events.EventTaskCompleted:
				// C2.4: use the deterministic score provider when wired;
				// fall back to the constant 1.0 when not (backward compatible).
				s.RecordScore(s.taskScore(true))
			case ares_events.EventTaskFailed:
				// C2.4: use the deterministic score provider when wired;
				// fall back to the constant 0.0 when not (backward compatible).
				s.RecordScore(s.taskScore(false))
			}
		}
		return nil
	})

	log.Info("[Evolution] Scheduler registered for agent stopped events")
}

// contextFromEvent derives a context from an EventStore event, propagating
// any well-known metadata keys (agent_id, tenant_id, trace_id) so the
// evolution path keeps the request's correlation context instead of dropping
// it with context.Background().
//
// Args:
//
//	evt - the event to extract context from.
//
// Returns:
//
//	context.Context - a derived context carrying the event's metadata.
func contextFromEvent(evt *ares_events.Event) context.Context {
	ctx := context.Background()
	if evt == nil || evt.Payload == nil {
		return ctx
	}
	for _, k := range []string{contextKeyNameAgentID, contextKeyNameTenantID, contextKeyNameTraceID} {
		if v, ok := evt.Payload[k]; ok {
			ctx = context.WithValue(ctx, contextKey(k), v)
		}
	}
	return ctx
}

// shouldEvolve determines if an evolution cycle should be triggered.
// The decision is based on multiple heuristics:
//   - Minimum interval protection (minInterval must have elapsed since lastRun)
//   - Minimum task count threshold (enough data collected for meaningful decision)
//   - Score degradation detection (recent performance dropping significantly)
//
// Args:
//
//	ctx - operation context.
//	data - the callback data containing agent completion information.
//
// Returns:
//
//	true if evolution should run, false otherwise.
func (s *EvolutionScheduler) shouldEvolve(ctx context.Context, data CallbackData) bool {
	// Step 1: Check minimum interval protection.
	s.mu.Lock()
	lastRun := s.lastRun
	minInterval := s.minInterval
	trigger := s.trigger
	s.mu.Unlock()

	if !lastRun.IsZero() && time.Since(lastRun) < minInterval {
		log.DebugContext(ctx, "[Evolution] Skipping: minimum interval not elapsed",
			"last_run", lastRun.Format(time.RFC3339),
			"min_interval", minInterval)
		return false
	}

	// Step 2: Snapshot score state under a single lock to avoid TOCTOU.
	avg, recent, scoreCount := s.scoreSnapshot()

	// Step 3: Check trigger mode.
	switch trigger {
	case TriggerOnDemand:
		return false

	case TriggerOnThreshold:
		// Only avg must be positive: recent == 0 means every recent task
		// failed, which is the strongest degradation signal there is and
		// must not be excluded from the check.
		if avg <= 0 {
			return false
		}
		drop := (avg - recent) / avg
		if drop >= degradationThreshold {
			log.InfoContext(ctx, "[Evolution] Score degradation detected",
				"overall_avg", avg,
				"recent_avg", recent,
				"drop_pct", drop)
			return true
		}
		return false

	case TriggerOnIdle:
		if scoreCount < minScoreCountForReliability {
			return false
		}

		if avg > 0 {
			drop := (avg - recent) / avg
			if drop >= degradationThreshold {
				log.InfoContext(ctx, "[Evolution] Score degradation detected (idle)",
					"overall_avg", avg,
					"recent_avg", recent,
					"drop_pct", drop)
				return true
			}
		}

		if scoreCount >= periodicEvolutionScoreThreshold {
			log.DebugContext(ctx, "[Evolution] Periodic evolution triggered",
				"score_count", scoreCount)
			return true
		}
		return false

	default:
		return false
	}
}

// scoreSnapshot reads avg, recent avg, and score count atomically under a single lock.
func (s *EvolutionScheduler) scoreSnapshot() (avg, recent float64, count int) {
	s.scoreMu.Lock()
	defer s.scoreMu.Unlock()

	if len(s.scores) == 0 {
		return 0, 0, 0
	}

	var total float64
	for _, v := range s.scores {
		total += v
	}
	avg = total / float64(len(s.scores))

	window := 10
	if window > len(s.scores) {
		window = len(s.scores)
	}
	var recentTotal float64
	for _, v := range s.scores[len(s.scores)-window:] {
		recentTotal += v
	}
	recent = recentTotal / float64(window)

	count = len(s.scores)
	return
}

// populationSizer is an optional interface that adapters can implement to
// report the current population size for guardrail checks.
type populationSizer interface {
	PopulationSize() int
}

// populationInspector is the fuller optional interface: besides the size it
// reports the unevaluated count and the current generation.
//
// It exists because size alone cannot drive PreEvolveCheck. That check's ONLY
// ShouldStop condition is "more than half the population is unevaluated", and
// checkGuardrails used to pass a hardcoded unevaluatedCount of 0 — so the
// legacy scheduler's guardrail could never block anything, no matter how it was
// configured (B2). Wiring guardrails without this was wiring a switch with no
// wire behind it.
//
// Generation matters too: it is stamped on every emitted GuardrailEvent, and a
// hardcoded 0 made every event look like it came from the first generation.
type populationInspector interface {
	populationSizer
	PopulationUnevaluated() int
	PopulationGeneration() int
}

// checkGuardrails runs a pre-evolution guardrail check.
// Returns true if evolution should proceed, false if guardrails block it.
// Passes bestRecentScore from the score window for meaningful baseline comparison.
//
// Args:
//
//	ctx - operation context.
//
// Returns:
//
//	bool - true if evolution may proceed.
func (s *EvolutionScheduler) checkGuardrails(ctx context.Context) bool {
	if s.guardrails == nil {
		return true
	}
	// Use the most recent score as currentBest for baseline regression detection.
	avg, _, _ := s.scoreSnapshot()
	// Read the population shape when the adapter can report it. An adapter that
	// only implements populationSizer still yields unevaluated=0, which the
	// unevaluated-majority check reads as "fully evaluated" — the documented
	// degradation for adapters that cannot introspect their population.
	totalPop, unevaluated, generation := 0, 0, 0
	switch a := s.adapter.(type) {
	case populationInspector:
		totalPop = a.PopulationSize()
		unevaluated = a.PopulationUnevaluated()
		generation = a.PopulationGeneration()
	case populationSizer:
		totalPop = a.PopulationSize()
	}
	result := s.guardrails.PreEvolveCheck(ctx, avg, generation, totalPop, unevaluated)
	if result.ShouldStop {
		log.WarnContext(ctx, "[Evolution] Guardrails block evolution cycle",
			"events", len(result.Events),
			"generation", generation,
			"total_pop", totalPop,
			"unevaluated", unevaluated)
		return false
	}
	return true
}

// SetEnabled enables or disables the scheduler at runtime.
//
// Args:
//
//	enabled - true to enable, false to disable.
func (s *EvolutionScheduler) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// SetScoreProvider wires the C2.4 zero-LLM score provider at runtime. This
// is the post-construction wiring path: the EvolutionScheduler is built
// before the peer kernel's ExecutionAttribution exists, so the provider
// must be injected after both are constructed. Called from the serve
// wiring layer (cmd/ares) once the attribution source is available.
//
// Args:
//
//	provider - the TaskScoreProvider to use (nil reverts to constants).
func (s *EvolutionScheduler) SetScoreProvider(provider TaskScoreProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scoreProvider = provider
}

// IsEnabled returns whether the scheduler is currently enabled.
//
// Returns:
//
//	bool - true if enabled, false otherwise.
func (s *EvolutionScheduler) IsEnabled() bool {
	return s.enabled.Load()
}

// LastRunTime returns the timestamp of the last evolution cycle.
// Thread-safe: uses mutex to protect concurrent access.
//
// Returns:
//
//	time.Time - the last run time, or zero value if never run.
func (s *EvolutionScheduler) LastRunTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRun
}

// SetDreamCycle attaches a dream cycle orchestrator to the scheduler.
// When set, the scheduler delegates evolution execution to the dream cycle
// instead of directly running the adapter.
//
// Args:
//
//	dc - the dream cycle orchestrator (may be nil to detach).
func (s *EvolutionScheduler) SetDreamCycle(dc *DreamCycle) {
	s.dreamCycle = dc
}

// DreamCycle returns the attached dream cycle orchestrator, if any.
//
// Returns:
//
//	*DreamCycle - the dream cycle instance, or nil if not set.
func (s *EvolutionScheduler) DreamCycle() *DreamCycle {
	return s.dreamCycle
}

// Shutdown gracefully stops the scheduler and cancels all pending evolution goroutines
// and the event subscription loop. It should be called when the scheduler is no
// longer needed to prevent goroutine leaks.
func (s *EvolutionScheduler) Shutdown() {
	s.subMu.Lock()
	subCancel := s.subCancel
	subEg := s.subEg
	s.subMu.Unlock()
	if subCancel != nil {
		subCancel()
	}
	if subEg != nil {
		_ = subEg.Wait()
	}

	s.evolveMu.Lock()
	cancel := s.evolveCancel
	eg := s.evolveEg
	s.evolveMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if eg != nil {
		if err := eg.Wait(); err != nil {
			log.Warn("evolution scheduler: wait", "error", err)
		}
	}
}

// taskScore returns the score for a task outcome event (C2.4). When a
// TaskScoreProvider is wired, it delegates to the provider so the score
// reflects real execution quality (latency, retries, recovery). Without a
// provider, it falls back to the constant 1.0/0.0 (backward compatible).
//
// Args:
//
//	success - true for task.completed, false for task.failed.
//
// Returns:
//
//	float64 - the score in [0,1].
func (s *EvolutionScheduler) taskScore(success bool) float64 {
	if s.scoreProvider != nil {
		return s.scoreProvider.TaskScore(success)
	}
	if success {
		return taskScoreSuccess
	}
	return taskScoreFailure
}

// ShouldEvolve delegates to the internal shouldEvolve logic.
// This is the exported entry point for DreamCycle to check evolution conditions.
//
// Args:
//
//	ctx - operation context.
//	data - callback data from the triggering event.
//
// Returns:
//
//	bool - true if evolution should run.
func (s *EvolutionScheduler) ShouldEvolve(ctx context.Context, data CallbackData) bool {
	return s.shouldEvolve(ctx, data)
}

// TriggerMode returns the current trigger mode.
// Thread-safe: uses mutex to protect concurrent access.
//
// Returns:
//
//	EvolutionTrigger - the current trigger mode.
func (s *EvolutionScheduler) TriggerMode() EvolutionTrigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trigger
}

// Tick is the single heartbeat entry point for the evolution ticker. It
// replaces the bootstrap ticker's unconditional popAdapter.Run(ctx) call so
// evolution timing is unified: the same shouldEvolve + checkGuardrails +
// MinInterval throttling that drives event-triggered evolution also drives
// time-triggered evolution (B4 fix).
//
// Tick is safe to call from a time.Ticker goroutine. It runs the adapter
// synchronously — the caller controls timing via the ticker interval.
// When the scheduler is disabled or the adapter is nil, Tick is a no-op.
func (s *EvolutionScheduler) Tick(ctx context.Context) {
	if !s.enabled.Load() {
		return
	}
	if s.adapter == nil {
		return
	}
	// Use the same throttling logic as OnAgentEnd: minInterval protection
	// + guardrails. This ensures the ticker cannot bypass degradation
	// detection or guardrail checks (B4 fix).
	if !s.shouldEvolve(ctx, CallbackData{AgentID: "ticker"}) {
		return
	}
	if !s.checkGuardrails(ctx) {
		return
	}
	if err := s.adapter.Run(ctx); err != nil {
		log.WarnContext(ctx, "[Evolution] Tick-triggered evolution failed", "error", err)
		return
	}
	s.mu.Lock()
	s.lastRun = time.Now()
	s.mu.Unlock()
}
