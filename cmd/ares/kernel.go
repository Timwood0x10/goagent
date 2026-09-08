// kernel — merged CLI source: kernel.go, kernel_adopt.go, kernel_bridge.go,
// kernel_dispatcher.go, kernel_loop.go, scheduler_compat.go,
// runtime_bridge.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/peer"
	"github.com/Timwood0x10/ares/internal/agentsyscall"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/kernel"
	"github.com/Timwood0x10/ares/internal/runtime"
)

// kernelHandle carries the assembled kernel from agent construction to the
// serve wiring.
//
// The Kernel pillars (ares-runtime.md §13) are assembled here:
//   - fabric:   Scheduler pillar (taskfabric: Create/Schedule/Acquire/RunQuantum)
//   - agents:   Lifecycle pillar (agentfabric: spawn/suspend/resume/retire/kill)
//   - recovery: Lifecycle recovery surface (aresrecovery: lease-expiry requeue /
//     checkpoint resume / agent restart)
//   - dual/flag: IPC pillar (agentipc: single-track Task Fabric dispatch +
//     execution policy; the legacy leader track was removed)
type kernelHandle struct {
	dual *agentipc.DualTrackDispatcher
	flag *agentipc.PolicyFlag

	fabric    *taskfabric.Fabric
	agents    *agentfabric.Fabric
	recovery  *aresrecovery.Recovery
	executors map[string]CapabilityExecutor
	// scheduler is the running kernelScheduler. Retained so
	// wireKernelLifecycle can attach the governance provider once the agent
	// fabric exists (the scheduler may start before the lifecycle wiring).
	scheduler *kernelScheduler
	// intro serves the runtime introspection panel (monitoring.md). Wired in
	// createAndServeAgents when the full kernel exists; nil on partial paths.
	intro *introspect.Handler
	// tracker is the shared per-agent load/confidence/priority source for the
	// scheduler and the fabric dispatch path. It is created at startup and
	// retained so agent priorities can be injected into it (OS-thread-style
	// thread priority).
	tracker *loadTracker
	// peerRegistry is the direct peer-to-peer messaging discovery surface
	// (primitive 2). Retained so the registry built at serve time stays
	// reachable for agent messaging / capability discovery instead of being
	// discarded (the peer registry return value was previously dropped).
	peerRegistry *peer.Registry
	// syscalls is the agentsyscall.Kernel backing spawn_agent/create_task/
	// ask_agent. Retained so the collaboration IPC bridge (built later in
	// setupPeerRegistry) can inject ipc.Send into ask_agent.
	// Nil on partial paths without syscalls.
	syscalls *agentsyscall.Kernel
	// compileCoord is the projection coordinator. It projects the live
	// MutableDAG into taskfabric PlanSteps and records compile provenance
	// for introspection. Nil when no live DAG is wired.
	compileCoord *planprojection.CompileCoordinator
	// sessionReg is the per-session L2 graph registry. Non-nil only
	// when the DAG execution gate is open; submitPeerTask admits sessions
	// through it. Nil = legacy path, session payloads stay envelope-only.
	sessionReg *agentfabric.SessionRegistry
	// pluginBus is the runtime plugin ecosystem hooked to the scheduler's
	// quantum boundary (runtime_bridge.go). Nil when the scheduler is absent.
	pluginBus *runtime.PluginBus
	// schedulerStop / schedulerDone drive the scheduler drain loop's managed
	// teardown: Stop cancels the loop context, Wait joins the goroutine.
	// Nil on partial paths — the adopt adapter skips those hooks.
	schedulerStop context.CancelFunc
	schedulerDone chan struct{}
	// recoveryStop / recoveryDone do the same for the kernel recovery loop.
	recoveryStop context.CancelFunc
	recoveryDone chan struct{}
	flipped      bool
}

// System Runtime registry names of the six kernel pillars. The
// dependency edges below turn these names into the shutdown order
// pluginbus/recovery → scheduler → dispatcher → fabrics → eventstore.
const (
	sysCompScheduler   = "scheduler"
	sysCompTaskFabric  = "taskfabric"
	sysCompAgentFabric = "agentfabric"
	sysCompRecovery    = "recovery"
	sysCompDispatcher  = "dispatcher"
	sysCompPluginBus   = "pluginbus"
)

// adoptReadyPollInterval is the polling cadence of the scheduler readiness
// gate; adoptReadyPollBudget bounds the total wait.
const (
	adoptReadyPollInterval = 50 * time.Millisecond
	adoptReadyPollBudget   = 2 * time.Second
)

// kernelComponent adapts one kernel pillar to the System Runtime Component
// contract. Identity + dependency metadata drive the registry ordering;
// optional ready/stop/wait hooks let Adopt verify real readiness (the
// scheduler must be draining to count as Ready) and let Shutdown drive real
// teardown (cancel + wait the loop's goroutine). Nil hooks are safe
// no-ops, so passive pillars (the fabrics, the dispatcher — pure in-memory
// state machines with no goroutine of their own) still join the graph and
// the shutdown order.
type kernelComponent struct {
	name    string
	deps    []string
	mode    kernel.Mode
	readyFn func(ctx context.Context) error
	stopFn  func(ctx context.Context) error
	waitFn  func() error
}

// Name returns the stable component identifier.
func (a *kernelComponent) Name() string { return a.name }

// Dependencies returns the names of components that must exist (and not be
// Failed) before this one is adopted; they also decide the shutdown order.
func (a *kernelComponent) Dependencies() []string { return a.deps }

// Ready delegates to the optional readiness hook; nil means Ready by
// construction.
func (a *kernelComponent) Ready(ctx context.Context) error {
	if a.readyFn == nil {
		return nil
	}
	return a.readyFn(ctx)
}

// Stop delegates to the optional teardown hook; nil is a no-op.
func (a *kernelComponent) Stop(ctx context.Context) error {
	if a.stopFn == nil {
		return nil
	}
	return a.stopFn(ctx)
}

// Wait delegates to the optional wait hook; nil is a no-op.
func (a *kernelComponent) Wait() error {
	if a.waitFn == nil {
		return nil
	}
	return a.waitFn()
}

// schedulerReady verifies the drain loop is actually running: it polls
// Scheduler.Running with a bounded budget so the natural delay between
// `go sched.Run(...)` and adoption does not produce a false Degraded, while
// a genuinely dead loop still reports a readable reason instead of Ready.
func schedulerReady(sched *kernelScheduler) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if sched.Running() {
			return nil
		}
		deadline := time.Now().Add(adoptReadyPollBudget)
		ticker := time.NewTicker(adoptReadyPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("scheduler drain loop not running (readiness check aborted: %v)", ctx.Err())
			case <-ticker.C:
				if sched.Running() {
					return nil
				}
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("scheduler drain loop not running after %s", adoptReadyPollBudget)
			}
		}
	}
}

// adopt registers the six kernel pillars with the System Runtime orchestrator:
// the kernel pillars are assembled LATER than
// the Bootstrap components, so they join the orchestrator through
// Orchestrator.Adopt instead of the startup-time Register. The adopt path
// owns the component names, the dependency edges (which decide the
// reverse-topological shutdown order), the stop/wait hooks, and the unified
// background-loop entry (runBackground).
//
// Nil pillars are skipped (present semantics, mirroring
// registerSystemComponent): partial kernels (SDK-adjacent paths, tests) keep
// working. A non-nil error from Adopt fails the serve startup loudly — a
// kernel pillar that cannot join the managed graph would otherwise recreate
// the "false Ready" blind spot.
func (k *kernelHandle) adopt(ctx context.Context, orch *kernel.Orchestrator) error {
	if k == nil {
		return nil
	}
	if orch == nil {
		log.Printf("serve: system runtime not wired; kernel components not adopted (unmanaged lifecycle)")
		return nil
	}

	// eventstore is the Bootstrap-registered dependency edge for both
	// fabrics; it decides that the fabrics stop BEFORE the store.
	eventstore := ares_bootstrap.SysCompEventStore

	components := []*kernelComponent{
		{
			// Passive state machine — no goroutine, nothing to stop. Its
			// presence in the graph orders the dispatcher/scheduler before
			// the event store at shutdown.
			name: sysCompTaskFabric,
			deps: []string{eventstore},
		},
		{
			// Passive population registry — same passive-stop semantics.
			name: sysCompAgentFabric,
			deps: []string{eventstore},
		},
		{
			// Dispatch is synchronous through the fabrics; stopping the
			// fabrics first (reverse topo) already halts new dispatch.
			name: sysCompDispatcher,
			deps: []string{sysCompTaskFabric, sysCompAgentFabric},
		},
		{
			// The scheduler owns the drain loop goroutine: Stop cancels its
			// context, Wait joins it. Degraded mode: a loop that is not
			// running reports Degraded + reason, never a false Ready.
			name: sysCompScheduler,
			deps: []string{sysCompTaskFabric, sysCompAgentFabric, sysCompDispatcher},
			mode: kernel.ModeDegraded,
			readyFn: func(ctx context.Context) error {
				if k.scheduler == nil {
					return nil
				}
				return schedulerReady(k.scheduler)(ctx)
			},
			stopFn: func(ctx context.Context) error {
				if k.schedulerStop != nil {
					k.schedulerStop()
				}
				return nil
			},
			waitFn: func() error {
				if k.schedulerDone != nil {
					<-k.schedulerDone
				}
				return nil
			},
		},
		{
			// Recovery owns the requeue/restart loop goroutine.
			name: sysCompRecovery,
			deps: []string{sysCompTaskFabric, sysCompAgentFabric},
			stopFn: func(ctx context.Context) error {
				if k.recoveryStop != nil {
					k.recoveryStop()
				}
				return nil
			},
			waitFn: func() error {
				if k.recoveryDone != nil {
					<-k.recoveryDone
				}
				return nil
			},
		},
		{
			// PluginBus has a real Stop (plugin reverse-order teardown).
			name: sysCompPluginBus,
			deps: []string{sysCompScheduler},
			stopFn: func(ctx context.Context) error {
				if k.pluginBus == nil {
					return nil
				}
				return k.pluginBus.Stop(ctx)
			},
		},
	}

	for _, c := range components {
		if !k.componentPresent(c.name) {
			continue
		}
		mode := c.mode
		if mode == 0 {
			mode = kernel.ModeRequired
		}
		if err := orch.Adopt(ctx, c, mode); err != nil {
			return fmt.Errorf("serve: adopt kernel component %q: %w", c.name, err)
		}
	}
	log.Printf("serve: kernel components adopted into system runtime (scheduler/taskfabric/agentfabric/recovery/dispatcher/pluginbus)")
	return nil
}

// componentPresent reports whether the named pillar exists on this kernel
// handle (present semantics: nil pillars are skipped, not an error).
func (k *kernelHandle) componentPresent(name string) bool {
	switch name {
	case sysCompTaskFabric:
		return k.fabric != nil
	case sysCompAgentFabric:
		return k.agents != nil
	case sysCompDispatcher:
		return k.dual != nil
	case sysCompScheduler:
		return k.scheduler != nil
	case sysCompRecovery:
		return k.recovery != nil
	case sysCompPluginBus:
		return k.pluginBus != nil
	default:
		return false
	}
}

// runBackground starts a managed background loop (no bare `go` on the
// serve path). With a wired System Runtime the loop joins the orchestrator's
// errgroup under the given name — a panic marks the component Failed and is
// recorded on the event sink — otherwise it falls back to the Bootstrap
// errgroup (same recover guarantees, no component marking).
//
// The fn receives the effective loop context: the orchestrator's managed
// root context on the adopted path, the caller's ctx on the fallback path.
// comp must be non-nil (every serve-path caller holds the Bootstrap
// container); a nil comp skips the loop loudly instead of leaking an
// unmanaged goroutine.
func runBackground(ctx context.Context, comp *ares_bootstrap.Components, name string, fn func(ctx context.Context) error) {
	if comp == nil {
		log.Printf("serve: background loop %q skipped (no component container)", name)
		return
	}
	if comp.SystemRuntime != nil {
		comp.SystemRuntime.GoBackground(name, fn)
		return
	}
	comp.GoBackground(ctx, name, fn)
}

// Kernel assembly entry (leader path removed).
//
// wireKernelDispatcher assembles the single-track Task Fabric dispatch kernel:
// the legacy leader track has been deleted, so the dispatcher always routes
// through kernelFabricDispatcher — scoring first, then (after
// enableKernelExecution attaches the executor) real
// Create→Schedule→Acquire→RunQuantum execution. The PolicyFlag starts at
// PolicyTaskFabric with shadow mode OFF (there is no legacy track left to
// compare against).
//
// Returns:
//   - *agentipc.DualTrackDispatcher: the assembled kernel dispatcher.
//   - *agentipc.PolicyFlag: the execution policy flag.
//
// TODO(tech-debt): agentipc has no retry/dead-letter semantics (the legacy ahp
// DLQProcessor was removed with the leader-sub protocol). Wire IPC retry or a
// dead-letter path when multi-agent messaging scales.
func wireKernelDispatcher(
	subAgents []subAgentCapability,
) (*agentipc.DualTrackDispatcher, *agentipc.PolicyFlag) {
	flag := agentipc.NewPolicyFlag(agentipc.PolicyTaskFabric)
	newPath := &kernelFabricDispatcher{candidates: subAgents}
	// nil legacy track: the leader path is removed, so the "dual" track is
	// single-track from the start and shadow mode is off.
	return agentipc.NewDualTrackDispatcher(flag, nil, newPath, false), flag
}

// enableKernelExecution switches the kernel's Task Fabric path from scoring
// (shadow) to real execution: it attaches the submitting executor (Create with
// DAG edges — the kernelScheduler owns Schedule→Acquire→RunQuantum) to the
// dispatcher. Callers invoke this at startup (peer mode) in the same critical
// section as the flag set to PolicyTaskFabric.
//
// Args:
//   - kernel: the dispatcher assembled by wireKernelDispatcher.
//   - fabric: the Task Fabric that executes tasks.
func enableKernelExecution(
	kernel *agentipc.DualTrackDispatcher,
	fabric *taskfabric.Fabric,
) {
	// Turn shadow off first: with the new path about to become live, running
	// the previous path in shadow would re-dispatch every task (double
	// execution).
	kernel.SetShadow(false)
	// Replace the scoring-only path with the submitting one. IMPORTANT: the
	// dispatch only SUBMITS the task to the fabric (Create); the kernelScheduler
	// is the single executor (Schedule→Acquire→RunQuantum on every READY task).
	// Keeping the full execution in the dispatch path as well caused a
	// double-path race: both the dispatch and the scheduler tried to acquire the
	// same task, surfacing as "task not ready for acquire" in serve logs.
	exec := &kernelFabricDispatcher{
		candidates: kernelNewPathCandidates(kernel),
		executeFn: func(ctx context.Context, task *models.Task) error {
			return submitFabricTask(ctx, fabric, task)
		},
	}
	kernel.SetNewPath(exec)
}

// kernelNewPathCandidates extracts the candidate list from the kernel's new
// path so enableKernelExecution can rebuild it with an executor attached.
func kernelNewPathCandidates(kernel *agentipc.DualTrackDispatcher) []subAgentCapability {
	if fp, ok := kernel.NewPath().(*kernelFabricDispatcher); ok {
		return fp.candidates
	}
	return nil
}

// submitFabricTask SUBMITS a task to the Task Fabric (Create with DAG edges)
// WITHOUT executing it. Execution is the kernelScheduler's sole job: its
// drain runs Schedule→Acquire→RunQuantum on every READY task. The leader
// dispatch path must NOT also schedule the task — doing so created a
// double-path race where both the leader dispatch (executeFabricTask) and the
// kernelScheduler tried to acquire the same task, surfacing as
// "task not ready for acquire" in serve logs.
//
// Args:
//   - ctx: task lifetime (unused; kept for signature symmetry).
//   - fabric: the Task Fabric that owns the task.
//   - task: the task to submit.
//
// Returns:
//   - error: fabric create error (ErrTaskExists is tolerated).
func submitFabricTask(
	ctx context.Context,
	fabric *taskfabric.Fabric,
	task *models.Task,
) error {
	if fabric == nil {
		return taskfabric.ErrTaskNotFound
	}
	// Single execution path. The dispatcher only materializes
	// L2-routable tasks — anything else would starve with no candidate
	// executor. Fail fast instead of creating an unrunnable task.
	if !agentfabric.IsL2Capability(string(task.AgentType)) {
		return fmt.Errorf("kernel bridge: agent type %q is not L2-routable (want ares/plan, ares/answer, ares/root, or tool/<name>)", task.AgentType)
	}
	var deps []string
	if task.Context != nil {
		deps = append([]string(nil), task.Context.Dependencies...)
	}
	if err := fabric.Create(&taskfabric.Task{
		ID:           task.TaskID,
		Capability:   string(task.AgentType),
		Dependencies: deps,
		Priority:     task.Priority,
		// Origin stays "" — kernel-bridge submissions are root tasks
		// (user/submitter-originated), not agent-created. Agent-created
		// tasks carry their caller via Task.Origin (create_task syscall).
		// RetryPolicy.MaxRetries counts TOTAL attempts, not retries-after-the-first
		// (taskfabric.CanRetry: Attempts < MaxRetries). MaxRetries: 1 therefore
		// grants ZERO retries — a transient failure finalizes FAILED immediately
		// (once a review bug). 2 = first attempt + one retry.
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
		// Carry the submission-time metadata in the Checkpoint slot so the
		// scheduler's toModelTask can restore it for the executor (LLM path
		// needs the profile; the outcome recorder needs UsedExperienceID).
		// The envelope is the versioned protocol (*CheckpointEnvelope);
		// a genuine progress checkpoint replaces it once a quantum runs
		// (RunQuantum yield).
		Checkpoint: &taskfabric.CheckpointEnvelope{
			UserProfile:      task.UserProfile,
			Payload:          task.Payload,
			UsedExperienceID: task.UsedExperienceID,
		},
	}); err != nil && err != taskfabric.ErrTaskExists {
		return fmt.Errorf("kernel fabric create: %w", err)
	}
	return nil
}

// subAgentCapability is the minimal capability surface the new-path scorer
// needs for one agent. Caps is the full declared capability set (flat
// peers); Type is the primary capability (first of Caps, or the legacy single
// Type for sub-structure configs).
type subAgentCapability struct {
	ID   string
	Type string
	Caps []string
	Load float64
}

// kernelFabricDispatcher is the kernel's Task Fabric dispatch path. Its D()
// behavior depends on whether an executeFn is attached (enableKernelExecution):
//
//   - scoring mode (no executeFn): scores the task against the candidate
//     agents with the Kernel's capability-aware formula (taskfabric.Score/Pick)
//     and reports the would-be outcome. It never creates, acquires or executes
//     — a task is never double-run.
//   - execution mode (executeFn attached): runs the real Task Fabric path
//     (Create → Schedule → Acquire → RunQuantum).
type kernelFabricDispatcher struct {
	candidates []subAgentCapability
	executeFn  func(ctx context.Context, task *models.Task) error // nil = scoring only
}

// D routes the task through the kernel's Task Fabric path: scoring (no
// executeFn) or real execution (executeFn attached).
func (d *kernelFabricDispatcher) D(ctx context.Context, agentID, taskID string, payload any) error {
	task, err := taskFromPayload(taskID, payload)
	if err != nil {
		return fmt.Errorf("kernel fabric dispatch: %w", err)
	}
	if d.executeFn != nil {
		return d.executeFn(ctx, task)
	}
	if len(d.candidates) == 0 {
		return nil
	}
	cands := make([]taskfabric.Candidate, 0, len(d.candidates))
	for _, c := range d.candidates {
		caps := c.Caps
		if len(caps) == 0 {
			caps = []string{c.Type}
		}
		cands = append(cands, taskfabric.Candidate{
			AgentID:      c.ID,
			Capabilities: caps,
			Load:         c.Load,
			Confidence:   1.0, // shadow: no experience store wired here
		})
	}
	if winner := taskfabric.Pick(string(task.AgentType), cands); winner == nil {
		return taskfabric.ErrNoCapableCandidate
	}
	return nil
}

// taskFromPayload builds a models.Task from the agentipc dispatch arguments.
// The payload is a map carrying the task's AgentType (capability), its DAG
// dependencies (Task Fabric gate) and any opaque user
// data; absent metadata falls back to a default type.
func taskFromPayload(taskID string, payload any) (*models.Task, error) {
	if taskID == "" {
		return nil, errors.New("task id required")
	}
	task := models.NewTask(taskID, models.AgentTypeTop, nil)
	if m, ok := payload.(map[string]any); ok {
		if at, ok := m["agent_type"].(string); ok && at != "" {
			task.AgentType = models.AgentType(at)
		}
		// UserProfile arrives as the same-process struct reference (the
		// kernel dispatcher passes it through untouched) — OR as a plain
		// map after a JSON round-trip (web serve → HTTP → decode). Both are
		// restored so the executor never sees profile==nil and degrades to
		// executeByType — the serve no-op chain.
		if up, ok := m["user_profile"].(*models.UserProfile); ok && up != nil {
			task.UserProfile = up
		} else if raw, ok := m["user_profile"].(map[string]any); ok {
			if buf, err := json.Marshal(raw); err == nil {
				var up models.UserProfile
				if err := json.Unmarshal(buf, &up); err == nil {
					task.UserProfile = &up
				}
			}
		}
		// Dependencies arrive as []string when the payload passes through the
		// kernel dispatcher directly and as []any after a JSON round-trip —
		// accept both so the DAG gate is never silently dropped.
		switch deps := m["dependencies"].(type) {
		case []string:
			task.Context.Dependencies = append(task.Context.Dependencies, deps...)
		case []any:
			for _, dep := range deps {
				if s, ok := dep.(string); ok && s != "" {
					task.Context.Dependencies = append(task.Context.Dependencies, s)
				}
			}
		}
		task.Payload = m
	}
	return task, nil
}

// recoverySweepInterval is how often the event-driven recovery loop also
// sweeps TTL-based lease expiry (lease expiry is detected by a sweep, not by
// an event, so a periodic safety net is required alongside the event channel).
// It is the default when kernel.recovery_sweep_interval is not configured.
const recoverySweepInterval = time.Second

// recoverySweepTimeout bounds one recovery sweep. A hung store must not block
// the recovery loop's event consumption nor pile up sweeps; the sweep
// runs async with this timeout so a slow store at worst drops triggers.
const recoverySweepTimeout = 30 * time.Second

// quotaApplyInterval is how often the evolution-aware quota manager pushes
// the current evolution resource budget into the Agent Fabric.
// The GA evolution ticker runs on a 5-minute cadence, so a 1-minute apply
// loop keeps a deployed budget effective within a reasonable window without
// burning CPU. It is the default when kernel.quota_apply_interval is unset.
const quotaApplyInterval = time.Minute

// evolutionApplyInterval is how often the evolution population adapter
// applies the agent population policy (runtime adaptation). Mirrors
// the quota cadence — 1 minute keeps a deployed policy effective within a
// reasonable window. It is the default when
// kernel.evolution_apply_interval is unset.
const evolutionApplyInterval = time.Minute

// evolutionApplyTimeout bounds one population policy application. A hung
// policy store must not stall the loop, so every Apply runs under this
// timeout.
const evolutionApplyTimeout = 30 * time.Second

// quotaApplyTimeout bounds one quota policy application. A hung policy store
// must not stall the quota loop, so every Apply runs under this timeout.
const quotaApplyTimeout = 30 * time.Second

// kernelLoopConfig carries the tunable intervals/timeouts for the kernel
// background loops (quota, recovery, dispatch). Zero durations fall back to
// the package defaults, so an absent kernel loop config section keeps prior
// behavior (zero-value usable).
type kernelLoopConfig struct {
	// LeaseTTL is the scheduler task-lease duration (0 = scheduler default).
	LeaseTTL time.Duration
	// QuotaApplyInterval is how often the quota loop re-applies the budget.
	QuotaApplyInterval time.Duration
	// QuotaApplyTimeout bounds each quota Apply call.
	QuotaApplyTimeout time.Duration
	// EvolutionApplyInterval is how often the evolution population loop runs.
	EvolutionApplyInterval time.Duration
	// EvolutionApplyTimeout bounds each evolution Apply call.
	EvolutionApplyTimeout time.Duration
	// RecoverySweepInterval is how often the recovery loop sweeps leases.
	RecoverySweepInterval time.Duration
	// RecoverySweepTimeout bounds each recovery sweep.
	RecoverySweepTimeout time.Duration
	// LoopMaxIterations caps the kernel loop clock's round count (0 =
	// unlimited). Past the budget the round clock stops advancing; the
	// scheduler's task flow is never gated by it.
	LoopMaxIterations int
	// LoopRoundQuanta is how many quanta constitute one loop round (>=1;
	// 0/absent falls back to 1).
	LoopRoundQuanta int
	// RecoveryKick carries task IDs the scheduler released at the stale-winner
	// boundary: the winner died with no capable replacement, so the task
	// is back in READY but has no execution body. The recovery loop binds a
	// replacement for each nominated task.
	//
	// A nominated task cannot be found by the expired-lease sweep — Release
	// clears the lease, and CheckExpiredLeases only requeues tasks that still
	// hold an expired one. That is exactly why the ID travels with the signal
	// instead of being a bare wake-up.
	//
	// Nil (the zero value) makes the select case inert, preserving the same
	// behavior for every call site that does not wire it.
	RecoveryKick <-chan string
}

// recoveryKickBuffer bounds the stale-winner nomination channel. Each
// entry is a distinct task needing a replacement body, so the buffer is sized
// for a burst of concurrent deaths (one drain runs at most 32 quanta, see
// Scheduler.drain's sanity cap) rather than the single slot a bare wake-up
// signal would need. The producer drops on full: a dropped nomination degrades
// to the pre-existing behavior for that task (it waits in READY for an executor),
// never to a blocked drain goroutine.
const recoveryKickBuffer = 32

// newRecoveryKick builds the stale-winner nomination pair: a bounded
// channel to hand to kernelLoopConfig.RecoveryKick, and a non-blocking hint
// function to hand to Scheduler.WithRecoveryHint.
//
// The hint is called from a drain goroutine on the scheduling hot path, so it
// must never block.
//
// Returns:
//   - <-chan string: the receive side for kernelLoopConfig.RecoveryKick.
//   - func(string): the non-blocking hint for Scheduler.WithRecoveryHint.
func newRecoveryKick() (<-chan string, func(taskID string)) {
	ch := make(chan string, recoveryKickBuffer)
	return ch, func(taskID string) {
		if taskID == "" {
			return
		}
		select {
		case ch <- taskID:
		default:
			// Buffer full: drop rather than block the drain. The task stays
			// READY and is picked up as soon as any capable executor appears.
			log.Printf("kernel recovery loop: nomination buffer full, dropping %q", taskID)
		}
	}
}

// withDefaults fills any zero-valued knob with the package default so a
// partially-configured (or zero) kernelLoopConfig never drives a loop with a
// zero ticker or a zero timeout.
func (c kernelLoopConfig) withDefaults() kernelLoopConfig {
	if c.QuotaApplyInterval <= 0 {
		c.QuotaApplyInterval = quotaApplyInterval
	}
	if c.QuotaApplyTimeout <= 0 {
		c.QuotaApplyTimeout = quotaApplyTimeout
	}
	if c.EvolutionApplyInterval <= 0 {
		c.EvolutionApplyInterval = evolutionApplyInterval
	}
	if c.EvolutionApplyTimeout <= 0 {
		c.EvolutionApplyTimeout = evolutionApplyTimeout
	}
	if c.RecoverySweepInterval <= 0 {
		c.RecoverySweepInterval = recoverySweepInterval
	}
	if c.RecoverySweepTimeout <= 0 {
		c.RecoverySweepTimeout = recoverySweepTimeout
	}
	if c.LoopRoundQuanta <= 0 {
		c.LoopRoundQuanta = 1
	}
	return c
}

// parseKernelLoopConfig reads the kernel loop knobs from the serve config.
// Empty or invalid duration strings log a warning and fall back to the
// package default, so a bad config never disables a safety timeout.
func parseKernelLoopConfig(cfg *ares_config.Config) kernelLoopConfig {
	parse := func(raw string, fallback time.Duration) time.Duration {
		if raw == "" {
			return fallback
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			log.Printf("kernel: invalid duration %q, using default %s: %v", raw, fallback, err)
			return fallback
		}
		return d
	}
	leaseTTL := time.Duration(0)
	if raw := cfg.Kernel.LeaseTTL; raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			leaseTTL = d
		} else {
			log.Printf("kernel: invalid lease_ttl %q, using scheduler default", raw)
		}
	}
	return kernelLoopConfig{
		LeaseTTL:               leaseTTL,
		QuotaApplyInterval:     parse(cfg.Kernel.QuotaApplyInterval, quotaApplyInterval),
		QuotaApplyTimeout:      parse(cfg.Kernel.QuotaApplyTimeout, quotaApplyTimeout),
		EvolutionApplyInterval: parse(cfg.Kernel.EvolutionApplyInterval, evolutionApplyInterval),
		EvolutionApplyTimeout:  parse(cfg.Kernel.EvolutionApplyTimeout, evolutionApplyTimeout),
		RecoverySweepInterval:  parse(cfg.Kernel.RecoverySweepInterval, recoverySweepInterval),
		RecoverySweepTimeout:   parse(cfg.Kernel.RecoverySweepTimeout, recoverySweepTimeout),
		LoopMaxIterations:      cfg.Kernel.LoopMaxIterations,
		LoopRoundQuanta:        cfg.Kernel.LoopRoundQuanta,
	}.withDefaults()
}

// runKernelQuotaLoop periodically applies the evolution resource policy to
// the Agent Fabric's budget. It applies once at startup so an
// already-deployed policy is effective immediately, then re-applies on a
// fixed interval — Apply is idempotent (replaces the budget in place), so a
// nil/no-op policy leaves the configured kernel resources untouched.
//
// Args:
//   - ctx: stops the loop.
//   - mgr: the quota manager (nil disables the loop).
//   - cfg: loop knobs; zero values fall back to the package defaults.
func runKernelQuotaLoop(ctx context.Context, mgr *aresrecovery.EvolutionAwareQuotaManager, cfg kernelLoopConfig) {
	if mgr == nil {
		return
	}
	cfg = cfg.withDefaults()
	apply := func(phase string) {
		// A hung policy store must not stall the loop: bound every Apply
		// with a timeout and recover from any panic so the ticker keeps
		// running.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("kernel: quota apply (%s) panic: %v", phase, r)
			}
		}()
		applyCtx, cancel := context.WithTimeout(ctx, cfg.QuotaApplyTimeout)
		defer cancel()
		if err := mgr.Apply(applyCtx); err != nil {
			log.Printf("kernel: quota apply (%s): %v", phase, err)
		}
	}
	apply("startup")
	ticker := time.NewTicker(cfg.QuotaApplyInterval)
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

// runKernelRecoveryLoop is the Kernel-level event-driven recovery loop. It
// reacts to task
// lifecycle events (TaskExpired / TaskFailed / TaskAcquired / TaskYielded) on
// the shared EventStore and, on each, runs the recovery chain
// (RequeueExpiredLeases → checkpoint resume → agent restart). A slow periodic
// sweep complements the event channel because TTL-based lease expiry is only
// observable by sweeping.
//
// Recovery闭环: when a factory + registerExecutor + hasCapableExecutor are
// wired (peer mode), the sweep goes beyond requeue-only. For each task that
// actually expired this sweep, if no registered executor can resume it, a
// replacement executor is created and bound to exactly that task
// (RegisterExecutorForTask), so the recovered task is driven by a real
// executor — not a phantom, and never a hijacker of other tasks. Bound
// executors are unregistered by the scheduler once the task reaches a terminal
// state. When the factory is nil (leader path, chaos/sandbox), the loop is
// requeue-only: existing registered executors resume the READY tasks from
// their preserved checkpoints via toModelTask.
//
// Each sweep runs ASYNC with a per-sweep timeout: a slow or hung store
// must neither block the loop's event consumption nor pile up sweeps. A
// buffered semaphore (capacity 1) drops a sweep trigger while the previous
// one is still running. The sweep goroutine is bounded by sweepCtx (derived
// from the loop ctx, so a shutdown cancels it) and releases the semaphore on
// exit (managed worker with a stop signal).
//
// cfg.RecoveryKick is the scheduler's stale-winner trigger. The scheduler
// signals it when a leased task's winner died with no capable replacement —
// the task is released to READY and this loop spawns the replacement body
// immediately, instead of the task waiting out a full lease TTL. A nil channel
// (the zero value) makes the select case inert, exactly like a nil event
// channel, so every existing call site keeps its previous behavior.
//
// Args:
//   - ctx: stops the loop.
//   - store: the EventStore to subscribe from (nil disables the event channel;
//     the periodic sweep still runs).
//   - recovery: the Recovery subsystem (nil disables the loop).
//   - cfg: loop knobs; zero values fall back to the package defaults.
//   - registerExecutor: registers a replacement executor bound to a specific
//     recovered task. nil = requeue-only mode.
//   - executorFactory: creates a CapabilityExecutor for a replacement agentID
//     and capability. nil = requeue-only mode.
//   - hasCapableExecutor: reports whether a registered executor can already
//     resume the given task, in which case no replacement is spawned.
func runKernelRecoveryLoop(
	ctx context.Context,
	store ares_events.EventStore,
	recovery *aresrecovery.Recovery,
	cfg kernelLoopConfig,
	registerExecutor func(taskID, agentID string, executor CapabilityExecutor),
	executorFactory func(agentID, capability string) CapabilityExecutor,
	hasCapableExecutor func(taskID string) bool,
) {
	if recovery == nil {
		return
	}
	cfg = cfg.withDefaults()
	var events <-chan *ares_events.Event
	if store != nil {
		ch, err := store.Subscribe(ctx, ares_events.EventFilter{
			Types: []ares_events.EventType{
				ares_events.EventTaskExpired,
				ares_events.EventTaskFailed,
				ares_events.EventTaskAcquired,
				ares_events.EventTaskYielded,
			},
		})
		if err == nil {
			events = ch
		} else {
			log.Printf("kernel recovery loop: subscribe failed, periodic sweep only: %v", err)
		}
	}
	ticker := time.NewTicker(cfg.RecoverySweepInterval)
	defer ticker.Stop()
	// bindReplacements gives each task in ids an execution body when no
	// registered executor can already resume it. Shared by the expired-lease
	// sweep and the stale-winner nomination path, which differ only in how
	// the task list is obtained: the sweep discovers tasks whose lease just
	// expired, the nomination path is told a specific task by the scheduler.
	//
	// No-op in requeue-only mode (leader path, chaos/sandbox, tests that pass
	// nil callbacks): the scheduler resumes the READY task with an existing
	// executor from its preserved checkpoint via toModelTask.
	bindReplacements := func(ids []string) {
		if registerExecutor == nil || executorFactory == nil || hasCapableExecutor == nil {
			return
		}
		for _, taskID := range ids {
			if hasCapableExecutor(taskID) {
				continue // an existing executor resumes this task
			}
			tasks := recovery.RecoveryTasksFor([]string{taskID})
			if len(tasks) == 0 {
				continue
			}
			rt := tasks[0]

			// with matching capability left a cognitive snapshot, revive
			// THAT identity in place — same id, restored cognition,
			// continuous provenance — instead of spawning a generic
			// replacement. RestartAgent enforces the maxRestarts budget
			// and returns ErrRecoveryExhausted past it, in which case we
			// fall through to the generic replacement below.
			if snapID, snap, found := recovery.RevivableSnapshot(rt.Capability); found {
				if revived, err := recovery.RestartAgent(ctx, snapID, snap.Cognitive, snap.Capabilities); err == nil {
					exec := executorFactory(revived.Identity, rt.Capability)
					if exec != nil {
						registerExecutor(taskID, revived.Identity, exec)
						log.Printf("kernel recovery loop: revived %q in place (cognition restored) for task %q", revived.Identity, taskID)
						continue
					}
				} else {
					log.Printf("kernel recovery loop: in-place revival of %q unavailable (%v); using replacement", snapID, err)
				}
			}

			replacementID := fmt.Sprintf("recovery-%s-%d", taskID, time.Now().UnixNano())
			executor := executorFactory(replacementID, rt.Capability)
			if executor == nil {
				log.Printf("kernel recovery loop: executor factory returned nil for %s (%s)", replacementID, rt.Capability)
				continue
			}
			registerExecutor(taskID, replacementID, executor)
			log.Printf("kernel recovery loop: replacement executor %q bound to task %q", replacementID, taskID)
		}
	}
	// sem (capacity 1) guards against overlapping sweeps: a sweep that is
	// still running (e.g. a stalled store) holds the single slot, so further
	// triggers are dropped until it finishes. Bounded — at most one sweep
	// goroutine exists beyond a hung one.
	sem := make(chan struct{}, 1)
	sweep := func() {
		select {
		case sem <- struct{}{}:
		default:
			return // previous sweep still running; drop this trigger
		}
		go func() {
			defer func() { <-sem }()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("kernel recovery loop: panic in recovery sweep: %v", r)
				}
			}()
			sweepCtx, cancel := context.WithTimeout(ctx, cfg.RecoverySweepTimeout)
			defer cancel()
			// Honor the sweep timeout even though the requeue scan is
			// in-memory: a cancelled/past-deadline sweep runs no scan at all
			// (the next trigger retries).
			select {
			case <-sweepCtx.Done():
				return
			default:
			}
			// Recovery闭环: requeue the tasks whose lease expired THIS
			// sweep (not all READY tasks — a brand-new task is never a
			// recovery candidate), then give each one an execution body.
			requeued := recovery.RequeueExpiredLeases()
			if len(requeued) == 0 {
				return
			}
			log.Printf("kernel recovery loop: requeued %d expired task(s)", len(requeued))
			bindReplacements(requeued)
		}()
	}
	// bindNominated handles one stale-winner nomination. It shares the
	// sweep's semaphore so a nomination can never run concurrently with a
	// sweep — both mutate the executor registry for the same task set, and
	// RestartAgent's restart budget must not be spent twice for one death.
	//
	// Unlike sweep it does NOT requeue: the scheduler already released the
	// task to READY, and Release cleared the lease, so CheckExpiredLeases
	// would never find it. The nomination carries the task ID for exactly this
	// reason.
	//
	// It WAITS for the semaphore instead of dropping on contention. Dropping
	// looked symmetric with sweep's drop-on-full, but the two are not
	// symmetric: a dropped sweep is retried by the next tick, whereas a
	// dropped nomination is lost forever — the released task holds no lease,
	// so no later sweep will rediscover it, and it sits in READY with no
	// execution body. Measured as a 1-in-30 residual failure of
	// TestE2E_GrandLoop_RealSchedulerChaosRecovery.
	//
	// Waiting is bounded: RecoveryKick is a capacity-32 channel and the loop
	// consumes one entry at a time, so at most a handful of these goroutines
	// exist, each parked on a semaphore released by an in-memory scan.
	bindNominated := func(taskID string) {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("kernel recovery loop: panic binding nominated task %q: %v", taskID, r)
				}
			}()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			bindReplacements([]string{taskID})
		}()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		case _, ok := <-events:
			if !ok {
				return
			}
			sweep()
		case taskID, ok := <-cfg.RecoveryKick:
			// The scheduler released a leased task whose winner died with
			// no capable replacement. Bind a replacement body now so the task
			// resumes within one drain instead of stalling in READY.
			if !ok {
				return
			}
			bindNominated(taskID)
		}
	}
}

// parseKernelPollInterval parses the YAML kernel.poll_interval duration,
// returning 0 when unset/invalid so the scheduler keeps its 500ms default.
//
// Args:
//
//	raw - the raw YAML duration string (may be empty).
//
// Returns:
//
//	time.Duration - the parsed interval, or 0 when empty/invalid.
func parseKernelPollInterval(raw string) time.Duration {
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("kernel: invalid poll_interval %q, using scheduler default", raw)
		return 0
	}
	return d
}

// This file is the cmd/ares compatibility layer over the shared
// internal/kernel package (合并 SDK 和
// kernel 两条路径 — the scheduler logic lives in one importable package, both
// cmd/ares and sdk drive the same engine). cmd/ares keeps its historical
// names so no caller (kernel wiring, peer mode, tests) changes.

// CapabilityExecutor is the scheduler's executor contract, aliased from the
// shared package so the whole cmd/ares codebase and the kernel loops use the
// identical interface.
type CapabilityExecutor = kernel.CapabilityExecutor

// kernelScheduler aliases the shared Scheduler, preserving cmd/ares's
// historical naming throughout kernel.go / agent.go / serve.go and their
// tests.
type kernelScheduler = kernel.Scheduler

// loadTracker aliases the shared per-agent load/confidence tracker.
type loadTracker = kernel.LoadTracker

// NewKernelScheduler creates the shared scheduler over a fabric. It mirrors
// the historical cmd/ares constructor signature; the implementation lives in
// kernel.New.
func NewKernelScheduler(fabric *taskfabric.Fabric, executors map[string]CapabilityExecutor, tracker *loadTracker) *kernelScheduler {
	return kernel.New(fabric, executors, tracker)
}

// newLoadTracker creates a shared tracker.
func newLoadTracker() *loadTracker {
	return kernel.NewLoadTracker()
}

// pluginBusHook adapts runtime.PluginBus to the kernel.QuantumHook
// contract, so the runtime plugin ecosystem (observer/checkpoint/tool/...)
// participates in the Agent OS scheduling loop without the kernel depending on
// the runtime package (the adapter lives in the cmd assembly layer — the only
// place allowed to import both).
//
// Mapping: the bus speaks workflow Step/StepResult; a scheduling quantum is
// projected as a single-step workflow whose ID is the fabric task id.
//
// The hook ALSO drives the kernel loop clock: every LoopRoundQuanta
// quanta closes one loop round through the registered LoopPlugin — the "beat"
// of the evolution clock. Concurrency contract (QuantumHook may be invoked
// from concurrent drains):
//   - the boundary decision uses the atomic.AddInt64 RETURN value, never
//     Add-then-Load — Add-then-Load lets two drains read the same counter
//     value (double-fire) or skip past a multiple (lost round);
//   - the round BUDGET is likewise derived from that return value, not from
//     the stop flag: a flag is read-then-set, so concurrent boundary callers
//     all observe "not stopped" before any of them latches it and each would
//     settle its own over-budget round. Deriving the round number from the
//     caller's own unique count makes the budget order-independent.
//   - the stop flag is its own atomic.Bool, kept purely as a fast path so an
//     exhausted clock stops touching the plugin on every later quantum.
type pluginBusHook struct {
	bus *runtime.PluginBus
	// loop is the registered round-clock plugin; nil disables the beat.
	loop *runtime.LoopPlugin
	// roundQuanta / maxRounds are the loop clock knobs (from kernelLoopConfig).
	roundQuanta int64
	maxRounds   int64
	// quantumCount counts quanta observed by this hook; the AddInt64 return
	// value is the authoritative boundary test input.
	quantumCount atomic.Int64
	// loopStop is a fast path only: once the budget is exhausted later quanta
	// skip the plugin entirely. It is NOT the budget enforcement mechanism —
	// see driveLoopRound (the budget is derived from quantumCount).
	loopStop atomic.Bool
}

// newPluginBusHook wraps a started PluginBus as a scheduler QuantumHook.
// loop may be nil (no loop clock). roundQuanta is normalized HERE (<=1 → 1)
// rather than trusting the caller: the boundary arithmetic divides by it, so
// the invariant belongs to the type, not to every construction site.
func newPluginBusHook(bus *runtime.PluginBus, loop *runtime.LoopPlugin, loopCfg kernelLoopConfig) *pluginBusHook {
	roundQuanta := int64(loopCfg.LoopRoundQuanta)
	if roundQuanta <= 0 {
		roundQuanta = 1
	}
	maxRounds := int64(loopCfg.LoopMaxIterations)
	if maxRounds < 0 {
		maxRounds = 0 // negative is meaningless; treat as unlimited
	}
	return &pluginBusHook{
		bus:         bus,
		loop:        loop,
		roundQuanta: roundQuanta,
		maxRounds:   maxRounds,
	}
}

// BeforeQuantum implements kernel.QuantumHook: projects the quantum
// onto the bus as a before-step hook invocation.
func (h *pluginBusHook) BeforeQuantum(ctx context.Context, taskID, agentID string) error {
	return h.bus.BeforeStep(ctx, taskID, &runtime.Step{
		ID:        taskID,
		Name:      taskID,
		AgentType: agentID,
		Status:    runtime.StepStatusRunning,
		StartedAt: time.Now(),
	})
}

// AfterQuantum implements kernel.QuantumHook: projects the quantum
// outcome onto the bus as an after-step hook invocation, then advances the
// loop clock. Both paths are observational — the hook never blocks or fails
// the scheduler.
func (h *pluginBusHook) AfterQuantum(ctx context.Context, taskID, agentID string, qerr error) {
	res := &runtime.StepResult{
		StepID:   taskID,
		Name:     taskID,
		Duration: 0,
		Metadata: map[string]string{"agent_id": agentID},
	}
	if qerr != nil {
		res.Status = runtime.StepStatusFailed
		res.Error = qerr.Error()
	} else {
		res.Status = runtime.StepStatusCompleted
	}
	_ = h.bus.AfterStep(ctx, taskID, res) // observational; bus already logs hook failures
	h.driveLoopRound(ctx)
}

// driveLoopRound advances the kernel loop clock when this quantum closes a
// round boundary.
//
// Judgment order (settle-then-gate — the gate must come AFTER the settle,
// otherwise a MaxIterations budget would swallow the FINAL round's
// OnRoundEnd: asking ShouldExecuteRound(round+1) before settling round
// `round` returns false at exactly the boundary where the last round needs
// its end-of-round bookkeeping):
//
//  1. settle the finished round: OnRoundEnd(round, executionID)
//  2. gate the next round: ShouldExecuteRound(round+1, vars) — false latches
//     loopStop and stops all further round advancement.
//
// executionID is the ROUND's identity, not the boundary task's taskID: one
// round spans LoopRoundQuanta quanta over multiple different tasks, so the
// task that happens to land on the boundary would flush only its own
// execution context while every other task of the round is silently skipped.
//
// Budget enforcement is derived from the caller's own `count`, NOT from
// loopStop. loopStop is read-then-set, so N concurrent boundary callers can
// all observe "not stopped" before any of them latches it, and each would
// then settle a round beyond the budget (observed: max_iterations=1 settling
// 3 rounds under concurrent drains). Because `count` is unique and monotonic
// per quantum, `round = count/roundQuanta` is unique per caller, so testing
// `round > maxRounds` is order-independent and exact.
func (h *pluginBusHook) driveLoopRound(ctx context.Context) {
	if h.loop == nil || h.loopStop.Load() {
		return
	}
	// The AddInt64 return value — not a subsequent Load — is the boundary
	// test: it is unique per quantum even under concurrent drains, so each
	// multiple of roundQuanta maps to exactly one caller.
	count := h.quantumCount.Add(1)
	if h.roundQuanta <= 0 || count%h.roundQuanta != 0 {
		return
	}
	round := count / h.roundQuanta
	// Over-budget rounds are dropped before any settling. `round` comes from
	// this caller's unique count, so this holds regardless of interleaving.
	// Note round == maxRounds still settles: settle-then-gate means the final
	// round keeps its end-of-round bookkeeping.
	if h.maxRounds > 0 && round > h.maxRounds {
		h.loopStop.Store(true)
		return
	}
	executionID := fmt.Sprintf("kernel-round-%d", round)
	// OnRoundEnd is best-effort by contract (each subsystem failure only
	// logs) and guarded internally — a panic here would be a plugin bug, so
	// recover to keep the observational contract ("hook never kills the
	// scheduler") airtight.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("kernel loop: round-end processing panicked (recovered): %v", r)
		}
	}()
	h.loop.OnRoundEnd(ctx, int(round), executionID)

	vars := map[string]any{"round": int(round)}
	if !h.loop.ShouldExecuteRound(int(round)+1, vars) {
		h.loopStop.Store(true)
		log.Printf("kernel loop: round budget exhausted (max_iterations=%d) after round %d; "+
			"round clock stopped (scheduler task flow unaffected)", h.maxRounds, round)
	}
}

// startPluginBus assembles the runtime plugin ecosystem and attaches it to the
// kernel scheduler's quantum boundary (Agent OS closure: the plugins observe
// every Schedule→Acquire→RunQuantum without the kernel importing the runtime).
//
// Registration order is load-bearing: PluginBus.Register REJECTS plugins
// after Start (ErrBusAlreadyStarted), and PluginBus.Start is what hands each
// plugin its EventBus reference — LoopPlugin.OnRoundEnd service discovery
// (`p.bus.(*PluginBus)`) only works when the plugin was registered BEFORE
// Start. Registering after Start fails twice over: the Register error is
// downgraded to a log line, and the plugin never receives a bus, so every
// round-end action becomes a silent no-op while the beat keeps ticking.
//
// This wires the ROUND CLOCK (LoopPlugin beat). The downstream
// capability plugins LoopPlugin discovers on round end (CapCheckpoint flush,
// CapMemory advise, CapEvolution record) are a separate wiring item —
// until they are registered the clock beats and the actions are no-ops,
// which the falsifiable tests cover by proving a registered fake
// CapCheckpoint Flusher IS flushed on every round boundary.
//
// Args:
//
//	ctx     - lifetime of the serve process; cancelling stops the bus.
//	store   - the shared event store the bus mirrors events into (may be nil).
//	sched   - the kernel scheduler to hook; may be nil (no-op).
//	loopCfg - kernel loop knobs (round quanta / max iterations).
//
// Returns:
//
//	*runtime.PluginBus - the started bus (nil when nothing to wire).
func startPluginBus(ctx context.Context, store ares_events.EventStore, sched *kernel.Scheduler, loopCfg kernelLoopConfig) *runtime.PluginBus {
	if sched == nil {
		return nil
	}
	bus := runtime.NewPluginBus()
	_ = store // the bus subscribes via Subscribe(); store passthrough not needed

	// Register BEFORE Start (see doc comment: Register-after-Start is a
	// guaranteed silent no-op).
	loop := runtime.NewLoopPlugin("kernel-loop", runtime.LoopConfig{
		MaxIterations: loopCfg.LoopMaxIterations,
		// UntilCondition stays nil: the kernel round clock does no variable
		// assertion — the round budget is the only stop condition.
	})
	if err := bus.Register(loop); err != nil {
		// Downgrade to log + continue scheduling: a registration metadata
		// problem must never block the kernel.
		log.Printf("peer mode: loop plugin registration skipped (scheduling continues without the round clock): %v", err)
		loop = nil
	}
	if err := bus.Start(ctx); err != nil {
		log.Printf("peer mode: plugin bus start failed (scheduling continues without plugins): %v", err)
		return nil
	}
	sched.WithQuantumHook(newPluginBusHook(bus, loop, loopCfg))
	log.Printf("peer mode: plugin bus wired to kernel quantum boundary (loop clock: quanta/round=%d max_iterations=%d)",
		loopCfg.LoopRoundQuanta, loopCfg.LoopMaxIterations)
	return bus
}
