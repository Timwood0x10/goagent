package aresrecovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// Recovery orchestrates the Kernel's failure-recovery paths. It is a
// SEPARATE responsibility from Chaos (which injects failures):
// Recovery proves the Runtime survives them. The subsystem wires the Task
// Fabric (durable tasks + lease expiry + checkpoints) to the Agent Fabric
// (disposable agents + cognitive state) so that an agent death is followed
// by task requeue + checkpoint resume + agent replacement.
type Recovery struct {
	tasks  *taskfabric.Fabric
	agents *agentfabric.Fabric
	// spawner is the optional evolution-aware spawn gate. When
	// set, every replacement spawn goes through it so the evolution policy
	// (Enabled / MaxConcurrent / PreferredCapabilities) shapes restarts and
	// checkpoint recovery too — "Evolution decides; Kernel enforces". Nil
	// keeps the plain fabric spawn.
	spawner *EvolutionAwareSpawner
	// cogFactory is the A1 execution-body factory injected into every
	// replacement spawn (replacement 走 A1 factory，
	// 可执行 — 消除 phantom). When set, spawnAgent fills
	// spec.CognitionFactory before spawning so the replacement agent is a
	// REAL cognitive process that the scheduler can execute, not an empty
	// shell. Nil keeps the caller's spec untouched (the production peer path
	// passes its own factory through runKernelRecoveryLoop's executorFactory).
	cogFactory agentfabric.CognitionFactory
	// policy is the restart policy (max attempts, backoff).
	policy RestartPolicy
	// restarts tracks how many times each agent id has been restarted. The
	// count is LIFETIME-CUMULATIVE per identity and is intentionally NEVER
	// reset by a successful revival: the budget exists to stop a broken agent
	// from cycling forever, so total deaths — not consecutive ones — consume
	// it (clarified 2026-08-22).
	mu       sync.Mutex
	restarts map[string]int
	now      func() time.Time
	// sleep suspends the calling goroutine for d, returning ctx.Err() when
	// ctx is cancelled first. It exists as a field so tests can replace the
	// real time.Sleep (same determinism pattern as the now clock above).
	sleep func(ctx context.Context, d time.Duration) error
}

// realSleeper sleeps for d unless ctx is cancelled first. The production
// default injected into Recovery.sleep.
func realSleeper(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RestartPolicy bounds agent restart attempts after a crash.
type RestartPolicy struct {
	// MaxRestarts is the total restart attempts allowed (0 = no restart).
	MaxRestarts int
	// Backoff is the initial delay before a restart attempt (doubles each
	// attempt, capped at MaxBackoff).
	Backoff time.Duration
	// MaxBackoff caps the backoff growth.
	MaxBackoff time.Duration
}

// DefaultRestartPolicy is a sane production default: 5 attempts, 1s backoff
// capped at 30s.
func DefaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		MaxRestarts: 5,
		Backoff:     1 * time.Second,
		MaxBackoff:  30 * time.Second,
	}
}

// ErrRecoveryExhausted is returned when the restart budget is exhausted.
var ErrRecoveryExhausted = errors.New("aresrecovery: restart budget exhausted")

// New wires the Recovery subsystem to the Task and Agent Fabrics.
func New(tasks *taskfabric.Fabric, agents *agentfabric.Fabric, policy RestartPolicy) *Recovery {
	if policy.MaxRestarts == 0 {
		policy = DefaultRestartPolicy()
	}
	if policy.Backoff == 0 {
		policy.Backoff = time.Second
	}
	if policy.MaxBackoff == 0 {
		policy.MaxBackoff = 30 * time.Second
	}
	return &Recovery{
		tasks:    tasks,
		agents:   agents,
		policy:   policy,
		restarts: make(map[string]int),
		now:      time.Now,
		sleep:    realSleeper,
	}
}

// WithClock injects a controllable clock for deterministic tests.
func (r *Recovery) WithClock(now func() time.Time) *Recovery {
	r.now = now
	return r
}

// WithSleeper injects a controllable sleeper for deterministic tests: the
// restart backoff is observable through the durations handed to it, without
// real time.Sleep. Returns the Recovery for chaining.
func (r *Recovery) WithSleeper(sleep func(ctx context.Context, d time.Duration) error) *Recovery {
	r.sleep = sleep
	return r
}

// WithSpawner injects the evolution-aware spawn gate. When set,
// every replacement spawn in RecoverTaskCheckpoint / RestartAgent is routed
// through it, so the evolution policy shapes restart and recovery spawns.
// Returns the Recovery for chaining.
func (r *Recovery) WithSpawner(s *EvolutionAwareSpawner) *Recovery {
	r.spawner = s
	return r
}

// WithCognitionFactory injects the A1 execution-body factory.
// Every replacement spawn produced by RecoverTaskCheckpoint /
// RestartAgent then carries the factory, so the replacement agent is
// executable (non-phantom). Returns the Recovery for chaining.
func (r *Recovery) WithCognitionFactory(f agentfabric.CognitionFactory) *Recovery {
	r.cogFactory = f
	return r
}

// spawnAgent creates a replacement agent, routing through the evolution
// spawner when wired, otherwise spawning directly on the fabric. Recovery
// spawns ALWAYS use the recovery path (SpawnForRecovery): they replace a
// dead/expired agent and must not be blocked by the population cap — a
// self-healing spawn rejected by MaxConcurrent would strand the task forever
// (recovery bypasses quota, not the Enabled gate).
func (r *Recovery) spawnAgent(ctx context.Context, spec agentfabric.SpawnSpec) (*agentfabric.Agent, error) {
	// Inject the A1 execution body so a replacement is a REAL cognitive
	// process (the scheduler can execute it), never a phantom shell.
	if r.cogFactory != nil && spec.CognitionFactory == nil {
		spec.CognitionFactory = r.cogFactory
	}
	if r.spawner != nil {
		return r.spawner.SpawnForRecovery(ctx, spec)
	}
	return r.agents.Spawn(ctx, spec)
}

// RequeueExpiredLeases sweeps the Task Fabric for expired leases and returns
// the ids of every task requeued to READY (lease expiry → requeue).
// A dead agent's lease expires; the task becomes acquirable again. This is
// the first recovery path. The returned ids are the ONLY tasks that were
// expired — the caller must NOT iterate all READY tasks (which would hijack
// brand-new tasks unrelated to any crash).
//
// Returns:
//   - []string: the ids of every requeued task (empty when none).
func (r *Recovery) RequeueExpiredLeases() []string {
	return r.tasks.CheckExpiredLeases()
}

// RecoverTaskCheckpoint resumes a task's preserved checkpoint with a new
// agent (checkpoint recovery). The task must be in a state where its
// checkpoint is preserved (SUSPENDED or READY after lease expiry). The
// Recovery subsystem:
//  1. Finds a replacement agent (spawns one if the original is gone).
//  2. Acquires the task for the new agent.
//  3. The new agent resumes from the checkpoint (its cognitive state is
//     installed from the task's preserved checkpoint).
//
// TEST/CHAOS-ONLY (this entry point installs checkpoints via
// agents.SetCognitiveState and acquires tasks itself, which is a separate
// mechanism from the production scheduler path (scheduler.executeWith-
// Candidates → taskfabric.DecodeCheckpoint → ToModelTask). Production
// recovery uses runKernelRecoveryLoop in cmd/ares. Callers: chaos
// simulations, sandbox tests, and recovery tests only.
//
// Args:
//   - ctx: for event sinks.
//   - taskID: the task to recover.
//   - replacementID: the new agent's id ("" = auto-spawn one).
//
// Returns:
//   - string: the replacement agent id.
//   - uint64: the new lease epoch (fencing token).
//   - error: taskfabric.ErrTaskNotFound / ErrRecoveryExhausted.
func (r *Recovery) RecoverTaskCheckpoint(ctx context.Context, taskID, replacementID string) (string, uint64, error) {
	t, err := r.tasks.Task(taskID)
	if err != nil {
		return "", 0, err
	}
	// The task must be acquirable (READY after lease expiry, or SUSPENDED with
	// a preserved checkpoint).
	if t.State != taskfabric.StateReady && t.State != taskfabric.StateSuspended {
		return "", 0, fmt.Errorf("aresrecovery: task %s not recoverable in state %s", taskID, t.State)
	}
	// Spawn or reuse the replacement agent.
	agentID := replacementID
	if agentID == "" {
		spawned, err := r.spawnAgent(ctx, agentfabric.SpawnSpec{
			Capabilities: []string{t.Capability},
		})
		if err != nil {
			return "", 0, fmt.Errorf("aresrecovery: spawn replacement: %w", err)
		}
		agentID = spawned.Identity
	}
	// Acquire the task for the replacement.
	epoch, err := r.tasks.Acquire(taskID, agentID, time.Minute)
	if err != nil {
		return "", 0, fmt.Errorf("aresrecovery: acquire %s for %s: %w", taskID, agentID, err)
	}
	// Install the preserved checkpoint as the new agent's cognitive state so
	// it resumes from where the dead agent left off. A failure to install the
	// checkpoint must not be silent: the task is acquired by a replacement
	// that cannot resume, so surface it for the recovery loop instead of
	// pretending recovery succeeded.
	if t.Checkpoint != nil {
		if err := r.agents.SetCognitiveState(agentID, agentfabric.CognitiveState{
			Checkpoint: t.Checkpoint,
			Context:    t.Checkpoint,
		}); err != nil {
			return "", 0, fmt.Errorf("aresrecovery: install checkpoint for %s: %w", agentID, err)
		}
	}
	return agentID, epoch, nil
}

// RestartAgent replaces a crashed agent with a new one that picks up the
// dead agent's cognitive checkpoint (agent restart). The original
// agent must be gone (killed). The new agent is spawned with the original's
// capabilities and cognitive state. The restart budget is checked; if
// exhausted, ErrRecoveryExhausted is returned. A backoff delay (Backoff
// doubled per prior attempt, capped at MaxBackoff) is slept before the
// replacement spawn, so consecutive crash-restart cycles cannot hammer the
// fabric; a cancelled ctx aborts the wait.
//
// Args:
//   - ctx: for event sinks.
//   - deadAgentID: the crashed agent's id.
//   - cognitive: the dead agent's preserved cognitive state.
//   - capabilities: the dead agent's declared capabilities.
//
// Returns:
//   - *agentfabric.Agent: the replacement agent.
//   - error: ErrRecoveryExhausted, or the ctx error when the ctx is
//     cancelled during the backoff sleep (no spawn happens).
func (r *Recovery) RestartAgent(ctx context.Context, deadAgentID string, cognitive agentfabric.CognitiveState, capabilities []string) (*agentfabric.Agent, error) {
	r.mu.Lock()
	// Lifetime-cumulative budget (see the restarts field note): successful
	// revivals do NOT reset the counter — only total deaths consume it.
	attempts := r.restarts[deadAgentID]
	if attempts >= r.policy.MaxRestarts {
		r.mu.Unlock()
		return nil, ErrRecoveryExhausted
	}
	r.mu.Unlock()
	// Crash-restart storm prevention: consecutive crash-restart cycles used
	// to hammer the fabric instantly (sleep-less restarts, the exact storm
	// the policy's Backoff was written to prevent). Sleep backoff doubled
	// per prior attempt, capped at MaxBackoff — the 0-th restart pays the
	// plain Backoff. The sleeper honors ctx, so a shutdown aborts the wait
	// instead of spawning a replacement into a dying process. Sleep BEFORE
	// charging the budget and spawning, and never under r.mu (holding the
	// lock while sleeping would freeze RestartCount/budget checks).
	delay := r.policy.Backoff << attempts
	if delay > r.policy.MaxBackoff || delay <= 0 {
		// Cap at MaxBackoff; the <=0 guard also catches the (unreachable
		// for sane policies) shift-overflow into non-positive values.
		delay = r.policy.MaxBackoff
	}
	if err := r.sleep(ctx, delay); err != nil {
		return nil, fmt.Errorf("aresrecovery: restart backoff for %s: %w", deadAgentID, err)
	}
	// Arbitration: when a death snapshot exists for THIS
	// identity, revive IN PLACE under the same id — provenance and the audit
	// trail stay continuous ("有状态认知复活"). Without a snapshot, fall back
	// to a freshly generated identity (plain replacement).
	spec := agentfabric.SpawnSpec{Capabilities: capabilities}
	if _, ok := r.agents.LastSnapshot(deadAgentID); ok {
		spec.Identity = deadAgentID
	}
	a, err := r.spawnAgent(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("aresrecovery: restart spawn for %s: %w", deadAgentID, err)
	}
	// Install the preserved cognitive state.
	if err := r.agents.Recover(ctx, a.Identity, cognitive); err != nil {
		return nil, fmt.Errorf("aresrecovery: restart recover for %s: %w", deadAgentID, err)
	}
	// Only charge the budget after a successful spawn+recover.
	r.mu.Lock()
	r.restarts[deadAgentID] = attempts + 1
	r.mu.Unlock()
	// The snapshot is now CONSUMED by this revival; keeping it would let a
	// much later death of the revived body restore stale cognition.
	r.agents.ClearSnapshot(deadAgentID)
	return a, nil
}

// RestartCount returns how many times an agent has been restarted.
func (r *Recovery) RestartCount(agentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.restarts[agentID]
}

// RevivableSnapshot exposes the agent fabric's death-snapshot matching the
// given capability (arbitration input). Found does not imply
// allowed — RestartAgent still enforces the restart budget.
func (r *Recovery) RevivableSnapshot(capability string) (string, agentfabric.AgentSnapshot, bool) {
	return r.agents.FindRevivableSnapshot(capability)
}

// RecoverFromAgentDeath is the full recovery chain (the acceptance path:
// "inject failure → kill agent → lease expires → Task READY → B acquire →
// checkpoint resume"). It sweeps expired leases, requeues tasks, and resumes
// each requeued task's checkpoint with a fresh replacement agent.
//
// TEST/CHAOS-ONLY (this entry point installs checkpoints via
// agents.SetCognitiveState and acquires tasks itself, which is a separate
// mechanism from the production path (runKernelRecoveryLoop → taskfabric
// DecodeCheckpoint → scheduler Schedule/Acquire). It must not be wired into
// the production serve path — production recovery uses runKernelRecoveryLoop
// in cmd/ares. Callers: chaos simulations, sandbox tests, and recovery
// tests only.
//
// Args:
//   - ctx: for event sinks.
//
// Returns:
//   - int: the number of tasks fully recovered (requeued + checkpoint resumed).
func (r *Recovery) RecoverFromAgentDeath(ctx context.Context) int {
	requeued := r.RequeueExpiredLeases()
	if len(requeued) == 0 {
		return 0
	}
	// Resume the checkpoint of exactly the tasks whose lease expired — not
	// every READY task. A brand-new or released task is not a crash-recovery
	// candidate and must not be grabbed by a replacement agent.
	recovered := 0
	for _, taskID := range requeued {
		if _, _, err := r.RecoverTaskCheckpoint(ctx, taskID, ""); err != nil {
			// A failure here means the task was requeued to READY but the
			// checkpoint resume failed (spawn/acquire/SetCognitiveState).
			// The task is still READY and will be picked up by the scheduler
			// on the next drain, but surface the failure instead of
			// silently dropping it.
			slog.Error("aresrecovery: recover task failed",
				slog.String("task_id", taskID),
				slog.Any("error", err))
			continue
		}
		recovered++
	}
	return recovered
}

// RecoveryTask is a snapshot of a task that was requeued from an expired
// lease. It carries the minimal information the recovery loop needs to
// spawn a replacement executor: the task ID and its required capability.
type RecoveryTask struct {
	// ID is the task identifier.
	ID string
	// Capability is the task's required capability (used to match a
	// replacement executor).
	Capability string
	// Checkpoint is the preserved checkpoint from the dead agent (nil when
	// the task had no checkpoint — a fresh start).
	Checkpoint any
}

// RecoveryTasksFor returns a RecoveryTask snapshot for each requeued task id
// from RequeueExpiredLeases. Only the requeued ids are accepted — the
// caller must pass exactly the ids returned by RequeueExpiredLeases, never
// ReadyTasks, so a brand-new READY task is never mistaken for a crash-
// recovery candidate.
//
// Args:
//   - taskIDs: the ids returned by RequeueExpiredLeases.
//
// Returns:
//   - []RecoveryTask: the tasks ready for recovery (empty when none).
func (r *Recovery) RecoveryTasksFor(taskIDs []string) []RecoveryTask {
	out := make([]RecoveryTask, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		t, err := r.tasks.Task(taskID)
		if err != nil {
			continue
		}
		out = append(out, RecoveryTask{
			ID:         t.ID,
			Capability: t.Capability,
			Checkpoint: t.Checkpoint,
		})
	}
	return out
}
