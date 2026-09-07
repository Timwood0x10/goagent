package taskfabric

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// defaultPlanInterval is the round-completion poll interval. The kernel
// scheduler itself polls the fabric on every tick, so a plan loop that polls
// the round's tasks at a similar cadence adds no new synchronization
// mechanism.
const defaultPlanInterval = 50 * time.Millisecond

// planRoundSeparator marks the round segment of a loop-compiled task ID
// ("<planID>#r<round>#<stepID>"). Extracted as a constant because both the
// ID builder and the loop's own log/error strings embed it.
const planRoundSeparator = "#r"

// RoundOutcome summarizes one completed round of a PlanLoop: every task of
// the round has reached a terminal state. It is the value handed to
// UntilCondition and PlanReplanFunc — the boundary where the workflow layer
// decides whether the DAG should run again (and with which steps).
type RoundOutcome struct {
	// Round is the 1-based number of the finished round.
	Round int
	// PlanID is the plan identity the round belongs to.
	PlanID string
	// Status maps step ID → terminal task state. A task that vanished from
	// the fabric (externally deleted) is reported as StateFailed: a plan
	// task disappearing is interference, and treating it as a failure is
	// the conservative reading — the round's work provably did not finish.
	Status map[string]TaskState
	// Output maps step ID → the step's own execution output, i.e. the
	// quantum's StepCheckpoint decoded from the task's checkpoint envelope.
	// A step is absent when it produced nothing. The submission-time input
	// payload (PlanStep.Payload) is deliberately NOT surfaced here: it is
	// already known to whoever built the step, and mixing it in would make
	// "did this round produce anything" undecidable for Replan.
	Output map[string]any
	// Failed lists step IDs whose task ended in StateFailed, in round step
	// order.
	Failed []string
}

// Succeeded reports whether every task of the round completed. It is the
// declarative UntilCondition used when a caller (or the LLM via create_plan)
// asks for "iterate until a clean round" without writing a Go predicate.
func (o RoundOutcome) Succeeded() bool { return len(o.Failed) == 0 }

// PlanReplanFunc derives the steps for the next round from the previous
// round's outcome. This is the "incremental replanning" hook (repair plan
// GAP-2 / appendix C M4): a round may tighten, expand or re-payload the DAG
// based on what the last round produced. Returning an empty batch is an
// error — a round always runs at least one step.
type PlanReplanFunc func(prev RoundOutcome) ([]PlanStep, error)

// PlanLoopSpec describes a DAG-level round loop over the task fabric. The
// steps form the round-invariant base DAG; each round re-compiles them under
// round-namespaced task IDs and executes through the normal kernel pipeline
// (Schedule → Acquire → RunQuantum). Nothing here dispatches work to agents:
// the loop only decides WHEN the next round of tasks is compiled, which is
// task-level lifecycle, not leader-sub orchestration (repair plan §0.2).
type PlanLoopSpec struct {
	// PlanID names the plan. It becomes the task-ID namespace
	// "<planID>#r<round>#<stepID>", so it must be unique among live plans
	// (task IDs are globally unique in the fabric).
	PlanID string
	// Steps is the base DAG. Step IDs are round-local; they must be
	// non-empty and unique within the batch.
	Steps []PlanStep
	// MaxRounds is the hard cap on executed rounds (>= 1). The loop always
	// executes round 1; later rounds only start while round < MaxRounds.
	MaxRounds int
	// UntilCondition, when set, is evaluated after each round; returning
	// true stops the loop before the next round. When nil the loop runs
	// exactly MaxRounds rounds.
	UntilCondition func(RoundOutcome) bool
	// Replan, when set, derives the next round's steps from the previous
	// outcome (incremental replanning). When nil every round reuses Steps.
	Replan PlanReplanFunc
}

// roundTask binds a round-local step ID to the fabric task ID compiled for
// it. The loop tracks the pair (not just the ID) because Replan may change
// the step set between rounds: re-deriving step IDs from the base spec would
// then watch tasks that were never created, and the round could never be
// observed as finished.
type roundTask struct {
	stepID string
	taskID string
}

// PlanLoop drives one plan through bounded DAG-level rounds. It owns a single
// managed worker goroutine (ctx + Stop-cancellable, recover-guarded) that
// waits for each round's tasks to go terminal, evaluates the exit condition
// and compiles the next round. Execution itself stays entirely with the
// scheduler — the loop never touches leases or agents.
type PlanLoop struct {
	f        *Fabric
	spec     PlanLoopSpec
	interval time.Duration
	// done is closed when the driver exits. Created at construction (not at
	// Start) so Stop on a never-started loop returns instead of blocking on
	// a nil channel forever.
	done chan struct{}

	// mu guards cancel, round, pending, outcome, loopErr and started.
	mu      sync.Mutex
	cancel  context.CancelFunc
	round   int           // last compiled round (1-based)
	pending []roundTask   // tasks of the last compiled round
	outcome *RoundOutcome // last finished round, nil until round 1 ends
	loopErr error         // fatal loop error, nil when the loop ended normally
	started bool
}

// planLoopOptions carries the tunables set through PlanLoopOption.
type planLoopOptions struct {
	interval time.Duration
}

// PlanLoopOption configures a PlanLoop at construction time.
type PlanLoopOption func(*planLoopOptions)

// WithPlanInterval overrides the round-completion poll interval. Non-positive
// values fall back to the default rather than disabling polling (a zero
// ticker panics — same guard as the scheduler's interval knobs).
func WithPlanInterval(d time.Duration) PlanLoopOption {
	return func(o *planLoopOptions) {
		if d > 0 {
			o.interval = d
		}
	}
}

// PlanTaskID returns the fabric task ID for a plan step in a given round.
// Exported so callers (tests, dashboards) can correlate tasks back to plans
// without depending on the separator convention being re-derived elsewhere.
func PlanTaskID(planID string, round int, stepID string) string {
	return planID + planRoundSeparator + strconv.Itoa(round) + "#" + stepID
}

// NewPlanLoop validates spec and returns a loop ready to Start. A non-nil
// fabric is required — the loop reads task states and compiles rounds
// through it. Start does the actual work; construction only validates.
func NewPlanLoop(f *Fabric, spec PlanLoopSpec, opts ...PlanLoopOption) (*PlanLoop, error) {
	if f == nil {
		return nil, errors.New("taskfabric: plan loop requires a fabric")
	}
	if spec.PlanID == "" {
		return nil, errors.New("taskfabric: plan loop: plan id required")
	}
	if err := validatePlanSteps(spec.PlanID, spec.Steps); err != nil {
		return nil, err
	}
	if spec.MaxRounds < 1 {
		return nil, fmt.Errorf("taskfabric: plan loop %q: max rounds must be >= 1", spec.PlanID)
	}
	o := planLoopOptions{interval: defaultPlanInterval}
	for _, opt := range opts {
		opt(&o)
	}
	return &PlanLoop{
		f:        f,
		spec:     spec,
		interval: o.interval,
		done:     make(chan struct{}),
	}, nil
}

// validatePlanSteps rejects a batch that could never compile into a round:
// empty batches, missing step IDs and duplicates. It runs on the base DAG at
// construction and on every Replan result, so a bad replan is caught before
// it reaches the fabric.
func validatePlanSteps(planID string, steps []PlanStep) error {
	if len(steps) == 0 {
		return fmt.Errorf("taskfabric: plan loop %q: empty step batch", planID)
	}
	seen := make(map[string]bool, len(steps))
	for _, s := range steps {
		if s.ID == "" {
			return fmt.Errorf("taskfabric: plan loop %q: step id required", planID)
		}
		if seen[s.ID] {
			return fmt.Errorf("taskfabric: plan loop %q: duplicate step id %q", planID, s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// Start compiles round 1 synchronously (so a bad base DAG fails the caller
// immediately and atomically — CompilePlan is all-or-nothing) and then runs
// the round driver on one managed goroutine until an exit condition, the
// round cap, or ctx/Stop ends it.
//
// Args:
//   - ctx: the loop's lifetime (production: the serve lifetime passed via
//     agentsyscall.WithLoopLifetime). Cancelling ctx ends the driver; tasks
//     of an in-flight round are left to the scheduler and normal recovery —
//     the loop never aborts leases it does not own.
func (l *PlanLoop) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("taskfabric: plan loop: start requires a context")
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		cancel()
		return errors.New("taskfabric: plan loop: already started")
	}
	l.started = true
	l.cancel = cancel
	l.mu.Unlock()

	if err := l.compileRound(runCtx, 1, l.spec.Steps); err != nil {
		cancel()
		// Done must close on every terminated loop, including one that
		// never got a driver goroutine: callers wait on it unconditionally.
		close(l.done)
		return err
	}
	go l.run(runCtx)
	return nil
}

// Stop cancels the driver and waits for it to exit. Calling it on a
// never-started loop is a no-op. It must not be called from inside
// UntilCondition/Replan (the callbacks run on the driver goroutine Stop waits
// for — that would deadlock).
func (l *PlanLoop) Stop() {
	l.mu.Lock()
	cancel, started := l.cancel, l.started
	l.mu.Unlock()
	if !started {
		return
	}
	if cancel != nil {
		cancel()
	}
	<-l.done
}

// Done is closed when the driver exits (any end reason). A loop whose Start
// failed also has Done closed.
func (l *PlanLoop) Done() <-chan struct{} { return l.done }

// Round reports the last compiled round (1-based; 0 before Start).
func (l *PlanLoop) Round() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.round
}

// Err reports the fatal loop error, or nil when the loop ended by condition
// or round cap. A non-nil Err always means later rounds did NOT start.
func (l *PlanLoop) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loopErr
}

// LastOutcome returns the finished round's outcome, if any round completed.
func (l *PlanLoop) LastOutcome() (RoundOutcome, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.outcome == nil {
		return RoundOutcome{}, false
	}
	return *l.outcome, true
}

// run is the single driver goroutine: poll → round terminal → decide →
// compile next round. Panics from user callbacks (UntilCondition/Replan) are
// recovered and recorded as loop errors — a caller bug must not take down
// the process (code_rules §4.2), it must only end the loop.
func (l *PlanLoop) run(ctx context.Context) {
	defer close(l.done)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		outcome, finished := l.pollRound()
		if !finished {
			continue
		}
		l.mu.Lock()
		l.outcome = &outcome
		l.mu.Unlock()

		if stop, err := l.evalUntil(outcome); err != nil {
			l.fail(err)
			return
		} else if stop || outcome.Round >= l.spec.MaxRounds {
			return
		}
		next, err := l.evalReplan(outcome)
		if err != nil {
			l.fail(err)
			return
		}
		if err := l.compileRound(ctx, outcome.Round+1, next); err != nil {
			l.fail(err)
			return
		}
	}
}

// evalUntil invokes the user's UntilCondition with a recover boundary.
// A nil condition means "never stop early".
func (l *PlanLoop) evalUntil(o RoundOutcome) (stop bool, err error) {
	if l.spec.UntilCondition == nil {
		return false, nil
	}
	defer func() {
		if r := recover(); r != nil {
			stop, err = false, fmt.Errorf("taskfabric: plan loop %q: until condition panicked: %v", l.spec.PlanID, r)
		}
	}()
	return l.spec.UntilCondition(o), nil
}

// evalReplan invokes the user's Replan with a recover boundary and validates
// the derived batch the same way the base DAG was validated at construction
// (an empty, unnamed or duplicated batch can never go terminal).
func (l *PlanLoop) evalReplan(prev RoundOutcome) (steps []PlanStep, err error) {
	if l.spec.Replan == nil {
		return l.spec.Steps, nil
	}
	defer func() {
		if r := recover(); r != nil {
			steps, err = nil, fmt.Errorf("taskfabric: plan loop %q: replan panicked: %v", l.spec.PlanID, r)
		}
	}()
	steps, err = l.spec.Replan(prev)
	if err != nil {
		return nil, err
	}
	if err := validatePlanSteps(l.spec.PlanID, steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// compileRound rewrites the batch onto round-namespaced task IDs and hands it
// to CompilePlan (atomic, cycle-checked), then records the resulting task set
// as the round the driver waits on. Step Origin/Priority/retry budgets are
// honored verbatim; only IDs and dependency references are rewritten.
func (l *PlanLoop) compileRound(ctx context.Context, round int, steps []PlanStep) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("taskfabric: plan loop %q: round %d: %w", l.spec.PlanID, round, err)
	}
	named := make([]PlanStep, 0, len(steps))
	tasks := make([]roundTask, 0, len(steps))
	for _, s := range steps {
		ns := s
		ns.ID = PlanTaskID(l.spec.PlanID, round, s.ID)
		if len(s.DependsOn) > 0 {
			deps := make([]string, 0, len(s.DependsOn))
			for _, d := range s.DependsOn {
				deps = append(deps, PlanTaskID(l.spec.PlanID, round, d))
			}
			ns.DependsOn = deps
		}
		named = append(named, ns)
		tasks = append(tasks, roundTask{stepID: s.ID, taskID: ns.ID})
	}
	if _, err := l.f.CompilePlan(ctx, named); err != nil {
		return fmt.Errorf("taskfabric: plan loop %q: round %d: %w", l.spec.PlanID, round, err)
	}
	l.mu.Lock()
	l.round = round
	l.pending = tasks
	l.mu.Unlock()
	return nil
}

// pollRound reads the current round's tasks and, once all of them are
// terminal, returns the assembled outcome. Reading each task by ID (instead
// of snapshotting the whole fabric) keeps the poll proportional to the round
// size and out of the scheduler's way on every tick.
//
// A task missing from the fabric counts as terminal-FAILED: it was deleted
// behind the loop's back, so waiting for it would hang the plan forever.
func (l *PlanLoop) pollRound() (RoundOutcome, bool) {
	l.mu.Lock()
	round, pending := l.round, l.pending
	l.mu.Unlock()

	o := RoundOutcome{
		Round:  round,
		PlanID: l.spec.PlanID,
		Status: make(map[string]TaskState, len(pending)),
	}
	for _, rt := range pending {
		task, err := l.f.Task(rt.taskID)
		if err != nil {
			o.Status[rt.stepID] = StateFailed
			o.Failed = append(o.Failed, rt.stepID)
			continue
		}
		if task.State != StateCompleted && task.State != StateFailed {
			// READY, LEASED, RUNNING or SUSPENDED: the round is still live.
			return RoundOutcome{}, false
		}
		o.Status[rt.stepID] = task.State
		if task.State == StateFailed {
			o.Failed = append(o.Failed, rt.stepID)
		}
		l.collectOutput(&o, rt.stepID, task.Checkpoint)
	}
	return o, true
}

// collectOutput records the step's execution output (the quantum's
// StepCheckpoint) into the outcome. A checkpoint that only carries the
// submission payload — the pre-execution envelope CompilePlan writes for a
// step with PlanStep.Payload — yields nothing, so Output stays a truthful
// answer to "what did this round produce".
func (l *PlanLoop) collectOutput(o *RoundOutcome, stepID string, checkpoint any) {
	if checkpoint == nil {
		return
	}
	decoded, err := DecodeCheckpoint(checkpoint)
	if err != nil {
		// A future-schema envelope cannot be interpreted by this build. The
		// task's terminal state is already recorded and is what drives the
		// loop, so the undecodable output is reported as absent rather than
		// failing the whole plan on a forward-compatibility mismatch.
		return
	}
	if decoded.StepCheckpoint == nil {
		return
	}
	if o.Output == nil {
		o.Output = make(map[string]any, len(o.Status))
	}
	o.Output[stepID] = decoded.StepCheckpoint
}

// fail records a fatal loop error. Later rounds never start after this.
func (l *PlanLoop) fail(err error) {
	l.mu.Lock()
	l.loopErr = err
	l.mu.Unlock()
}
