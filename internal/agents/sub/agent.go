package sub

import (
	"context"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/outputguard"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/errors"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	resources "github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// Event payload keys
const (
	KeyAgentID = "agent_id"
	KeyTaskID  = "task_id"
	KeyError   = "error"
	KeyStatus  = "status"
)

// Agent represents the Sub Agent interface. Agents are execution units only
// (ares-runtime: agents are not orchestrated, they are scheduled): the
// Kernel owns dispatch and drives each task quantum-by-quantum via
// ExecuteStep (taskfabric.RunQuantum). There is deliberately no self-dispatch
// entry point here — an agent never subscribes to events and runs tasks on
// its own.
type Agent interface {
	base.Agent
	// Execute runs a task to completion and returns its result (used by the
	// message-driven path).
	Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error)
	// ExecuteStep runs one execution quantum.
	// Done=false carries a resumable checkpoint: the task is SUSPENDED with
	// the checkpoint preserved and a later quantum resumes from it.
	ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error)
}

// StepOutcome is the result of one execution quantum. Done=false carries a resumable checkpoint so the caller (the
// kernel scheduler's RunQuantum step) can yield and resume the task in a later
// quantum; Done=true carries the finalized task result.
type StepOutcome struct {
	Done       bool
	Result     *models.TaskResult
	Checkpoint any
}

// stepExecutor is the optional quantum-capable contract. The interface lives
// at the consumer (sub.Agent); executors that predate quantum
// execution simply do not implement it and subAgent falls back to one-shot
// Execute. (The only implementation left is the cognition-backed
// adapter — the ReAct tool loop is deleted.)
type stepExecutor interface {
	ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error)
}

// TaskExecutor executes tasks.
type TaskExecutor interface {
	Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error)
	// RegisterFallback registers a type-specific handler used when the LLM
	// is unavailable or execution fails.
	RegisterFallback(agentType models.AgentType, handler FallbackHandler)
}

// FallbackHandler computes a degraded result when normal execution is
// unavailable. (Kept from the retired tool-loop executor: part of
// the TaskExecutor contract.)
type FallbackHandler func(ctx context.Context, task *models.Task) ([]*models.RecommendItem, string, error)

// ChatClient is the minimal LLM chat surface an executor needs (interface
// at the consumer). The optional params map carries per-call
// overrides (temperature, max_tokens, top_k) from the active evolution
// strategy. (Relocated from the retired tool-loop executor; the
// contract is unchanged.)
type ChatClient interface {
	Chat(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool, params map[string]any) (*llmcore.GenerateResponse, error)
}

// ToolBinder binds tools to the agent.
type ToolBinder interface {
	BindTool(name string, toolFunc func(ctx context.Context, args map[string]any) (any, error))
	CallTool(ctx context.Context, name string, args map[string]any) (any, error)
	ListTools() []string
	IsToolIdempotent(name string) bool
	ListIdempotentTools() []string
	GetToolSchemas() []resources.ToolSchema
	BridgeFromRegistry(registry *resources.Registry)
	WithPlannerBridge(bridge interface {
		Execute(ctx context.Context, toolName string, params map[string]any, userRequest string) (resources.Result, error)
	})
}

// Compile-time check: subAgent must satisfy base.StatefulAgent.
var _ base.StatefulAgent = (*subAgent)(nil)

// SubAgentOption configures a subAgent instance.
type SubAgentOption func(*subAgent)

// WithEventStore sets the event store for event sourcing.
func WithEventStore(store ares_events.EventStore) SubAgentOption {
	return func(a *subAgent) {
		a.eventStore = store
	}
}

// subAgent implements a Sub Agent.
type subAgent struct {
	mu         sync.RWMutex
	id         string
	agentType  models.AgentType
	status     models.AgentStatus
	config     *SubAgentConfig
	executor   TaskExecutor
	eventStore ares_events.EventStore

	// Lifecycle management
	stopCh   chan struct{}  // Signals goroutines to stop.
	streamWg sync.WaitGroup // Tracks active ProcessStream goroutines.
}

// SubAgentConfig holds configuration for SubAgent.
type SubAgentConfig struct {
	base.Config
}

// New creates a new SubAgent instance.
//
// TODO(tech-debt): the msgQueue parameter (and the SendMessage/
// ReceiveMessage surface it backed) was removed as dead: production peers
// were always constructed with a nil queue, so peer direct messaging never
// delivered — only the kernel-session collaboration topics are live.
// The handler and hbMon parameters (and the messageHandler /
// heartbeatSender files they backed) were removed the same way: the
// handler's Handle had zero call sites (protocol-ACK stubs only) and the
// heartbeat monitor was always constructed with nil in production.
// WithActionLog and the agents/actionlog package went with them: the store
// had zero production constructors, so the audit path never executed.
func New(
	id string,
	agentType models.AgentType,
	executor TaskExecutor,
	cfg *SubAgentConfig,
	opts ...SubAgentOption,
) Agent {
	if cfg == nil {
		cfg = DefaultSubAgentConfig(agentType)
	}
	cfg.ID = id
	cfg.Type = agentType

	a := &subAgent{
		id:        id,
		agentType: agentType,
		status:    models.AgentStatusOffline,
		config:    cfg,
		executor:  executor,
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// DefaultSubAgentConfig returns default configuration.
func DefaultSubAgentConfig(agentType models.AgentType) *SubAgentConfig {
	return &SubAgentConfig{
		Config: *base.DefaultConfig(agentType),
	}
}

// ID returns the unique identifier.
func (a *subAgent) ID() string {
	return a.id
}

// Type returns the agent type.
func (a *subAgent) Type() models.AgentType {
	return a.agentType
}

// Status returns the current status.
func (a *subAgent) Status() models.AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *subAgent) setStatus(status models.AgentStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = status
}

// Start starts the sub agent.
func (a *subAgent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.status != models.AgentStatusOffline {
		a.mu.Unlock()
		return errors.ErrAgentAlreadyStarted
	}
	a.status = models.AgentStatusStarting
	a.stopCh = make(chan struct{})
	a.mu.Unlock()

	a.setStatus(models.AgentStatusReady)

	// Wire event store to executor for tool/LLM call ares_events.
	if a.eventStore != nil {
		if setter, ok := a.executor.(interface {
			SetEventStore(ares_events.EventStore, string)
		}); ok {
			setter.SetEventStore(a.eventStore, a.id)
		}
	}

	a.emitEvent(ctx, ares_events.EventAgentStarted, map[string]any{
		KeyAgentID: a.id,
		"type":     string(a.agentType),
	})

	return nil
}

// Stop stops the sub agent and waits for active stream goroutines.
func (a *subAgent) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.status == models.AgentStatusOffline {
		a.mu.Unlock()
		return errors.ErrAgentNotRunning
	}
	if a.status == models.AgentStatusStopping {
		// A concurrent Stop already owns the shutdown; treat the stop as
		// done rather than error — the caller's goal (agent stopped) is
		// being achieved, and erroring would race the winner for no gain.
		a.mu.Unlock()
		return nil
	}
	a.status = models.AgentStatusStopping
	// Detach the channel under the lock so exactly one Stop closes it;
	// a second closer would panic ("close of closed channel") and take
	// the process down with it.
	stopCh := a.stopCh
	a.stopCh = nil
	a.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	// Wait for stream goroutines, but honour ctx cancellation so a stuck
	// goroutine cannot block Stop forever.
	waitDone := make(chan struct{})
	go func() {
		a.streamWg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-ctx.Done():
		log.Warn("sub agent stop: stream wait timed out", KeyAgentID, a.id, "error", ctx.Err())
	}

	a.emitEvent(ctx, ares_events.EventAgentStopped, map[string]any{
		KeyAgentID: a.id,
	})

	a.setStatus(models.AgentStatusOffline)
	return nil
}

// Process handles input and returns result.
func (a *subAgent) Process(ctx context.Context, input any) (any, error) {
	// Check status under lock to avoid TOCTOU between read and auto-Start.
	a.mu.Lock()
	status := a.status
	if status == models.AgentStatusOffline {
		// Temporarily release lock for Start (which acquires its own lock).
		// If Start fails or another goroutine already started us, handle gracefully.
		a.mu.Unlock()
		if err := a.Start(ctx); err != nil && err != errors.ErrAgentAlreadyStarted {
			return nil, err
		}
		a.mu.Lock()
		status = a.status
	}
	if status != models.AgentStatusReady {
		a.mu.Unlock()
		return nil, errors.ErrAgentNotReady
	}
	a.status = models.AgentStatusBusy
	a.mu.Unlock()

	a.streamWg.Add(1)
	defer a.streamWg.Done()
	defer a.setStatus(models.AgentStatusReady)

	task, ok := input.(*models.Task)
	if !ok {
		return nil, errors.ErrInvalidInput
	}

	if a.executor == nil {
		return nil, errors.ErrInvalidState
	}

	return a.executor.Execute(ctx, task)
}

// Heartbeat is the base.Heartbeater surface. It is a no-op since the
// heartbeatSender/monitor wiring was removed as dead (production always
// constructed the agent with a nil monitor); liveness is judged by IsAlive.
func (a *subAgent) Heartbeat(ctx context.Context) error {
	return nil
}

// IsAlive checks if the agent is alive.
func (a *subAgent) IsAlive() bool {
	return a.Status() == models.AgentStatusReady || a.Status() == models.AgentStatusBusy
}

// Execute executes a task to completion and returns its result.
func (a *subAgent) Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error) {
	if task == nil {
		return nil, errors.ErrInvalidInput
	}
	if a.executor == nil {
		return nil, errors.ErrNilPointer
	}

	a.emitEvent(ctx, ares_events.EventTaskCreated, map[string]any{
		KeyTaskID:  task.TaskID,
		KeyAgentID: a.id,
	})

	result, err := a.executor.Execute(ctx, task)
	return a.finalizeErr(ctx, task, result, err)
}

// ExecuteStep runs one execution quantum and returns its outcome. The
// per-task lifecycle events mirror Execute, split across quanta: TaskCreated
// fires on the first quantum (no resume checkpoint yet), TaskCompleted/Failed
// on the final one, so a multi-quantum task is announced once and finalized
// once. Executors without quantum support fall back to one full run in a
// single quantum.
func (a *subAgent) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	if a.executor == nil {
		return nil, errors.ErrNilPointer
	}
	if task == nil {
		return nil, errors.ErrInvalidInput
	}
	if task.Payload == nil || task.Payload["checkpoint"] == nil {
		a.emitEvent(ctx, ares_events.EventTaskCreated, map[string]any{
			KeyTaskID:  task.TaskID,
			KeyAgentID: a.id,
		})
	}

	se, ok := a.executor.(stepExecutor)
	if !ok {
		// Legacy executor without quantum support: the whole run is one quantum.
		res, err := a.executor.Execute(ctx, task)
		if _, finalErr := a.finalizeErr(ctx, task, res, err); finalErr != nil {
			return nil, finalErr
		}
		return &StepOutcome{Done: true, Result: res}, nil
	}

	out, err := se.ExecuteStep(ctx, task)
	if err != nil {
		_, finalErr := a.finalizeErr(ctx, task, nil, err)
		return nil, finalErr
	}
	if !out.Done {
		// Yield: no boundary event yet — the task is still in flight.
		return out, nil
	}
	_, guardErr := a.finalizeErr(ctx, task, out.Result, nil)
	return out, guardErr
}

// finalizeErr applies the sub-agent boundary to a finished task outcome: the
// output guard (primitive 6), completion/failure events and the action log.
// It is the shared tail of Execute and ExecuteStep so both paths finalize
// identically. Returns the (possibly rejected) result and any boundary error.
func (a *subAgent) finalizeErr(ctx context.Context, task *models.Task, result *models.TaskResult, execErr error) (*models.TaskResult, error) {
	if execErr != nil {
		a.emitEvent(ctx, ares_events.EventTaskFailed, map[string]any{
			KeyTaskID:                            task.TaskID,
			KeyAgentID:                           a.id,
			KeyError:                             execErr.Error(),
			ares_events.EventKeyTask:             taskEventText(task),
			ares_events.EventKeyResult:           execErr.Error(),
			ares_events.EventKeyTenantID:         distillTenantID(),
			ares_events.EventKeyUsedExperienceID: task.UsedExperienceID,
			ares_events.EventKeyStrategyID:       task.StrategyID,
		})
		return nil, execErr
	}

	// Output guard (primitive 6): reject structurally inconsistent results at
	// the boundary — a success carrying an error, or a failure with no detail —
	// so contradictory state never propagates into aggregation/distillation.
	if guardErr := outputguard.NewGuard().ValidateResult(result); guardErr != nil {
		a.emitEvent(ctx, ares_events.EventTaskFailed, map[string]any{
			KeyTaskID:                            task.TaskID,
			KeyAgentID:                           a.id,
			KeyError:                             guardErr.Error(),
			ares_events.EventKeyTask:             taskEventText(task),
			ares_events.EventKeyResult:           guardErr.Error(),
			ares_events.EventKeyTenantID:         distillTenantID(),
			ares_events.EventKeyUsedExperienceID: task.UsedExperienceID,
			ares_events.EventKeyStrategyID:       task.StrategyID,
		})
		return result, fmt.Errorf("sub agent %s output guard rejected result: %w", a.id, guardErr)
	}

	a.emitEvent(ctx, ares_events.EventTaskCompleted, map[string]any{
		KeyTaskID:                            task.TaskID,
		KeyAgentID:                           a.id,
		ares_events.EventKeyTask:             taskEventText(task),
		ares_events.EventKeyResult:           resultEventText(result),
		ares_events.EventKeyTenantID:         distillTenantID(),
		ares_events.EventKeyUsedExperienceID: task.UsedExperienceID,
		ares_events.EventKeyStrategyID:       task.StrategyID,
	})

	return result, nil
}

// ProcessStream handles input and returns a stream of ares_events.
//
// The method owns only admission control (status gating, auto-start,
// validation) and goroutine lifecycle (panic boundary, WaitGroup, channel
// close); the per-task event sequence lives in runTaskAndEmit. Keeping the
// two apart bounds each function's branching: the previous single-body form
// mixed TOCTOU handling, recovery and five selects in one scope, which made
// changes to either concern riskier than the content justified.
func (a *subAgent) ProcessStream(ctx context.Context, input any) (<-chan base.AgentEvent, error) {
	// Atomically check status under lock to avoid TOCTOU with auto-Start.
	a.mu.Lock()
	status := a.status
	if status == models.AgentStatusOffline {
		a.mu.Unlock()
		if err := a.Start(ctx); err != nil && err != errors.ErrAgentAlreadyStarted {
			return nil, err
		}
		a.mu.Lock()
		status = a.status
	}
	if status != models.AgentStatusReady {
		a.mu.Unlock()
		return nil, errors.ErrAgentNotReady
	}
	a.status = models.AgentStatusBusy
	a.mu.Unlock()

	task, ok := input.(*models.Task)
	if !ok {
		a.setStatus(models.AgentStatusReady)
		return nil, errors.ErrInvalidInput
	}

	if a.executor == nil {
		a.setStatus(models.AgentStatusReady)
		return nil, errors.ErrInvalidState
	}

	ch := make(chan base.AgentEvent, 64)

	a.mu.RLock()
	stopCh := a.stopCh
	a.mu.RUnlock()

	a.streamWg.Add(1)
	go func() {
		defer close(ch)
		defer a.streamWg.Done()
		// Reset to Ready only when the task goroutine finishes — NOT when
		// the outer function returns the channel. The previous outer defer
		// fired immediately, breaking Busy/Ready admission control.
		defer a.setStatus(models.AgentStatusReady)
		defer func() {
			if r := recover(); r != nil {
				// Capture panic to prevent process crash.
				// Emit failure event and send error on channel so consumers don't hang.
				panicErr := fmt.Errorf("sub agent %s panic: %v", a.id, r)
				a.emitEvent(ctx, ares_events.EventSubAgentFailed, map[string]any{
					KeyAgentID: a.id,
					KeyTaskID:  task.TaskID,
					KeyError:   panicErr.Error(),
				})
				log.Error("sub agent panic recovered", KeyAgentID, a.id, "panic", r)
				select {
				case ch <- base.AgentEvent{Type: base.EventComplete, Source: a.id, Err: panicErr}:
				case <-ctx.Done():
				case <-stopCh:
				}
			}
		}()

		a.runTaskAndEmit(ctx, task, ch, stopCh)
	}()

	return ch, nil
}

// runTaskAndEmit executes one task inside the ProcessStream goroutine and
// emits its full lifecycle: Busy→Ready around the executor call, TaskStart /
// TaskCreated before it, then TaskFailed or TaskCompleted + completion events
// after. Every channel send races ctx.Done() and stopCh so a cancelled
// consumer never deadlocks the agent goroutine.
func (a *subAgent) runTaskAndEmit(
	ctx context.Context,
	task *models.Task,
	ch chan<- base.AgentEvent,
	stopCh <-chan struct{},
) {
	a.setStatus(models.AgentStatusBusy)
	defer a.setStatus(models.AgentStatusReady)

	// Send task start event
	select {
	case ch <- base.AgentEvent{Type: base.EventTaskStart, Source: a.id, Data: task}:
	case <-ctx.Done():
		return
	case <-stopCh:
		return
	}

	a.emitEvent(ctx, ares_events.EventTaskCreated, map[string]any{
		KeyTaskID:  task.TaskID,
		KeyAgentID: a.id,
	})

	// Execute task
	result, err := a.executor.Execute(ctx, task)
	if err != nil {
		a.emitEvent(ctx, ares_events.EventTaskFailed, map[string]any{
			KeyTaskID:                            task.TaskID,
			KeyAgentID:                           a.id,
			KeyError:                             err.Error(),
			ares_events.EventKeyTask:             taskEventText(task),
			ares_events.EventKeyResult:           err.Error(),
			ares_events.EventKeyTenantID:         distillTenantID(),
			ares_events.EventKeyUsedExperienceID: task.UsedExperienceID,
			ares_events.EventKeyStrategyID:       task.StrategyID,
		})

		select {
		case ch <- base.AgentEvent{Type: base.EventComplete, Source: a.id, Err: err}:
		case <-ctx.Done():
		case <-stopCh:
		}
		return
	}

	a.emitEvent(ctx, ares_events.EventTaskCompleted, map[string]any{
		KeyTaskID:                            task.TaskID,
		KeyAgentID:                           a.id,
		ares_events.EventKeyTask:             taskEventText(task),
		ares_events.EventKeyResult:           resultEventText(result),
		ares_events.EventKeyTenantID:         distillTenantID(),
		ares_events.EventKeyUsedExperienceID: task.UsedExperienceID,
		ares_events.EventKeyStrategyID:       task.StrategyID,
	})

	// Send task complete event
	select {
	case ch <- base.AgentEvent{Type: base.EventTaskComplete, Source: a.id, Data: result}:
	case <-ctx.Done():
		return
	case <-stopCh:
		return
	}

	// Send final result
	select {
	case ch <- base.AgentEvent{Type: base.EventComplete, Source: a.id, Data: result}:
	case <-ctx.Done():
	case <-stopCh:
	}
}

// RestoreState restores the sub-agent's state from persisted data.
// Implements base.StatefulAgent for resurrection support.
func (a *subAgent) RestoreState(state map[string]any) error {
	if state == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Sub-agents are simpler than leaders — just restore status if needed.
	if status, ok := state["status"].(string); ok && status != "" {
		a.status = models.AgentStatus(status)
	}
	return nil
}

// ReplayEvents replays ares_events to reconstruct sub-agent state.
// Implements base.StatefulAgent for resurrection support.
func (a *subAgent) ReplayEvents(evts []*ares_events.Event) error {
	if len(evts) == 0 {
		return nil
	}
	// Sub-agents track task completion for operational recovery.
	for _, ev := range evts {
		if ev == nil {
			continue
		}
		if ev.Type == ares_events.EventTaskCompleted {
			log.Debug("sub-agent replayed task completion",
				KeyAgentID, a.id,
				KeyTaskID, ev.Payload[KeyTaskID],
			)
		}
	}
	return nil
}

// Snapshot returns a serializable snapshot of the sub-agent's state.
// Implements base.StatefulAgent for resurrection support.
func (a *subAgent) Snapshot() (map[string]any, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return map[string]any{
		KeyAgentID: a.id,
		KeyStatus:  string(a.status),
	}, nil
}

// emitEvent appends a single event using the canonical ares_events.Emit.
func (a *subAgent) emitEvent(ctx context.Context, eventType ares_events.EventType, payload map[string]any) {
	if ares_events.Emit(ctx, a.eventStore, a.id, eventType, "sub", payload) {
		log.Debug("event emitted", KeyAgentID, a.id, "type", eventType)
	}
}
