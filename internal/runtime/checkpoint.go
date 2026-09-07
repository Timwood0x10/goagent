package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const currentSchemaVersion = 1

// CheckpointStore persists execution checkpoint data for crash recovery.
type CheckpointStore interface {
	// Save stores data under the given key.
	Save(ctx context.Context, key string, data []byte) error
	// Load retrieves data for the given key. Returns nil, nil if not found.
	Load(ctx context.Context, key string) ([]byte, error)
}

// DAGEdge captures a single edge in the DAG topology for round checkpoint.
type DAGEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ExperienceCheckpoint captures a workflow execution at a point in time,
// including both recovery data and signals for memory/evolution consumption.
type ExperienceCheckpoint struct {
	SchemaVersion    int                    `json:"schema_version"`
	ExecutionID      string                 `json:"execution_id"`
	WorkflowID       string                 `json:"workflow_id"`
	WorkflowVersion  string                 `json:"workflow_version,omitempty"`
	StateVersion     int64                  `json:"state_version"`
	Status           string                 `json:"status"`
	CurrentRound     int                    `json:"current_round"` // evolutionary loop round
	Error            string                 `json:"error,omitempty"`
	StepStates       []StepStateSnapshot    `json:"step_states"`
	Variables        map[string]interface{} `json:"variables,omitempty"`
	OutputStore      map[string]string      `json:"output_store,omitempty"`
	DAGNodes         []string               `json:"dag_nodes,omitempty"` // node IDs for DAG recovery
	DAGEdges         []DAGEdge              `json:"dag_edges,omitempty"` // edge topology for DAG recovery
	RouteHistory     []RouteEntry           `json:"route_history,omitempty"`
	ToolHistory      []ToolEntry            `json:"tool_history,omitempty"`
	MemoryHits       []MemoryEntry          `json:"memory_hits,omitempty"`
	InterruptHistory []InterruptEntry       `json:"interrupt_history,omitempty"`
	LoopHistory      []LoopEntry            `json:"loop_history,omitempty"`
	ErrorHistory     []ErrorEntry           `json:"error_history,omitempty"`
	ScoringSignals   []ScoringSignal        `json:"scoring_signals,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
}

// StepStateSnapshot captures the state of a single step.
type StepStateSnapshot struct {
	StepID    string     `json:"step_id"`
	Status    StepStatus `json:"status"`
	Output    string     `json:"output,omitempty"`
	Error     string     `json:"error,omitempty"`
	StartedAt time.Time  `json:"started_at,omitempty"`
}

// RouteEntry records a routing decision.
type RouteEntry struct {
	FromStepID string `json:"from_step_id"`
	ToStepID   string `json:"to_step_id"`
	Reason     string `json:"reason"`
}

// ToolEntry records a tool invocation.
type ToolEntry struct {
	StepID   string        `json:"step_id"`
	ToolName string        `json:"tool_name"`
	Input    string        `json:"input,omitempty"`
	Output   string        `json:"output,omitempty"`
	Duration time.Duration `json:"duration_ns"`
	Success  bool          `json:"success"`
	Error    string        `json:"error,omitempty"`
}

// MemoryEntry records a memory retrieval hit.
type MemoryEntry struct {
	StepID     string  `json:"step_id"`
	Similarity float64 `json:"similarity"`
	TaskID     string  `json:"task_id,omitempty"`
}

// InterruptEntry records a HITL interrupt.
type InterruptEntry struct {
	StepID   string `json:"step_id"`
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback,omitempty"`
}

// LoopEntry records a loop iteration.
type LoopEntry struct {
	Iteration  int    `json:"iteration"`
	ExitReason string `json:"exit_reason,omitempty"`
}

// ErrorEntry records an execution error.
type ErrorEntry struct {
	StepID  string `json:"step_id,omitempty"`
	Message string `json:"message"`
}

// ScoringSignal records a quality or fitness signal.
type ScoringSignal struct {
	Source string  `json:"source"`
	Score  float64 `json:"score"`
	Label  string  `json:"label,omitempty"`
}

// Flusher is implemented by plugins that can flush buffered state to durable
// storage. The engine calls Flush when an execution completes or fails.
type Flusher interface {
	Flush(ctx context.Context, executionID string) error
}

// CheckpointKey returns the storage key for a given execution ID.
func CheckpointKey(executionID string) string {
	return fmt.Sprintf("checkpoint/%s", executionID)
}

// CheckpointPlugin saves experience checkpoints at key lifecycle points
// (BeforeStep, AfterStep) and manages accumulated execution state.
// It implements RuntimePlugin and WorkflowHook.
//
// By default the plugin saves on every hook call. Set flushInterval > 0
// with WithFlushInterval to batch writes (e.g., every 5 steps) and call
// Flush explicitly when the execution completes.
type CheckpointPlugin struct {
	name          string
	store         CheckpointStore
	mu            sync.Mutex
	collector     *ExecutionCollector // optional; if set, merged before save
	bus           EventBus            // optional; if set, emits EventCheckpointSaved
	flushInterval int                 // 0 = save on every hook (default)
	stepCount     map[string]int      // executionID → hook call count
	// accumulated state across hook calls
	snapshots map[string]*ExperienceCheckpoint // executionID → checkpoint
}

// NewCheckpointPlugin creates a CheckpointPlugin with the given store.
func NewCheckpointPlugin(name string, store CheckpointStore) *CheckpointPlugin {
	if name == "" {
		name = "checkpoint"
	}
	return &CheckpointPlugin{
		name:      name,
		store:     store,
		snapshots: make(map[string]*ExperienceCheckpoint),
		stepCount: make(map[string]int),
	}
}

// SetRound sets the current round number on the checkpoint for the given
// execution. Thread-safe.
func (p *CheckpointPlugin) SetRound(executionID string, round int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ckpt, ok := p.snapshots[executionID]; ok {
		ckpt.CurrentRound = round
	}
}

// WithFlushInterval sets the number of hook calls between checkpoint saves.
// A value of 0 or 1 saves on every call (default). Use higher values (e.g., 5)
// to batch writes and reduce I/O; call Flush when the execution completes.
func (p *CheckpointPlugin) WithFlushInterval(n int) *CheckpointPlugin {
	if n < 0 {
		n = 0
	}
	p.flushInterval = n
	return p
}

// Name returns the plugin name.
func (p *CheckpointPlugin) Name() string {
	return p.name
}

// Capabilities returns the capabilities this plugin provides.
func (p *CheckpointPlugin) Capabilities() []Capability {
	return []Capability{CapCheckpoint}
}

// WithCollector sets an execution collector whose data is merged into
// checkpoints before saving. Thread-safe.
func (p *CheckpointPlugin) WithCollector(c *ExecutionCollector) *CheckpointPlugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.collector = c
	return p
}

// Start initializes the checkpoint plugin.
func (p *CheckpointPlugin) Start(_ context.Context, bus EventBus) error {
	p.bus = bus
	return nil
}

// Stop shuts down the checkpoint plugin.
func (p *CheckpointPlugin) Stop(_ context.Context) error {
	return nil
}

// BeforeStep creates or updates a checkpoint marking the step as running.
func (p *CheckpointPlugin) BeforeStep(ctx context.Context, executionID string, step *Step) error {
	if p.store == nil {
		return nil
	}

	p.mu.Lock()

	ckpt := p.snapshots[executionID]
	if ckpt == nil {
		ckpt = &ExperienceCheckpoint{
			SchemaVersion: currentSchemaVersion,
			ExecutionID:   executionID,
			Status:        "running",
			CreatedAt:     time.Now(),
		}
	}

	ckpt.StateVersion++
	ckpt.Status = "running"

	// Add or update the step state.
	found := false
	for i, ss := range ckpt.StepStates {
		if ss.StepID == step.ID {
			ckpt.StepStates[i].Status = StepStatusRunning
			ckpt.StepStates[i].StartedAt = step.StartedAt
			found = true
			break
		}
	}
	if !found {
		ckpt.StepStates = append(ckpt.StepStates, StepStateSnapshot{
			StepID:    step.ID,
			Status:    StepStatusRunning,
			StartedAt: step.StartedAt,
		})
	}

	p.snapshots[executionID] = ckpt
	p.stepCount[executionID]++
	shouldFlush := p.flushInterval <= 1 || p.stepCount[executionID]%p.flushInterval == 0
	if !shouldFlush {
		p.mu.Unlock()
		return nil
	}
	// saveLocked runs while still holding p.mu (it does not lock internally),
	// closing the unlock→relock window that previously let another goroutine
	// mutate the checkpoint mid-save (TOCTOU).
	err := p.saveLocked(ctx, executionID, ckpt)
	p.mu.Unlock()
	return err
}

// AfterStep updates the checkpoint with the completed step result.
func (p *CheckpointPlugin) AfterStep(ctx context.Context, executionID string, result *StepResult) error {
	if p.store == nil {
		return nil
	}

	p.mu.Lock()

	ckpt := p.snapshots[executionID]
	if ckpt == nil {
		ckpt = &ExperienceCheckpoint{
			SchemaVersion: currentSchemaVersion,
			ExecutionID:   executionID,
			CreatedAt:     time.Now(),
		}
		p.snapshots[executionID] = ckpt
	}

	ckpt.StateVersion++

	// Update or append step state.
	found := false
	for i, ss := range ckpt.StepStates {
		if ss.StepID == result.StepID {
			ckpt.StepStates[i].Status = result.Status
			ckpt.StepStates[i].Output = result.Output
			ckpt.StepStates[i].Error = result.Error
			found = true
			break
		}
	}
	if !found {
		ckpt.StepStates = append(ckpt.StepStates, StepStateSnapshot{
			StepID: result.StepID,
			Status: result.Status,
			Output: result.Output,
			Error:  result.Error,
		})
	}

	if result.Status == StepStatusFailed {
		ckpt.ErrorHistory = append(ckpt.ErrorHistory, ErrorEntry{
			StepID:  result.StepID,
			Message: result.Error,
		})
	}

	p.stepCount[executionID]++
	shouldFlush := p.flushInterval <= 1 || p.stepCount[executionID]%p.flushInterval == 0
	if !shouldFlush {
		p.mu.Unlock()
		return nil
	}
	// Same as BeforeStep: save while holding p.mu to avoid the TOCTOU window.
	err := p.saveLocked(ctx, executionID, ckpt)
	p.mu.Unlock()
	return err
}

// Snapshot returns a deep copy of the current checkpoint for an execution, or nil.
func (p *CheckpointPlugin) Snapshot(executionID string) *ExperienceCheckpoint {
	p.mu.Lock()
	defer p.mu.Unlock()

	ckpt, ok := p.snapshots[executionID]
	if !ok {
		return nil
	}
	cp := *ckpt
	// Deep copy slice and map fields to prevent callers from mutating internal state.
	cp.StepStates = make([]StepStateSnapshot, len(ckpt.StepStates))
	copy(cp.StepStates, ckpt.StepStates)
	if ckpt.Variables != nil {
		cp.Variables = make(map[string]interface{}, len(ckpt.Variables))
		for k, v := range ckpt.Variables {
			cp.Variables[k] = v
		}
	}
	if ckpt.OutputStore != nil {
		cp.OutputStore = make(map[string]string, len(ckpt.OutputStore))
		for k, v := range ckpt.OutputStore {
			cp.OutputStore[k] = v
		}
	}
	cp.RouteHistory = append([]RouteEntry(nil), ckpt.RouteHistory...)
	cp.ToolHistory = append([]ToolEntry(nil), ckpt.ToolHistory...)
	cp.MemoryHits = append([]MemoryEntry(nil), ckpt.MemoryHits...)
	cp.InterruptHistory = append([]InterruptEntry(nil), ckpt.InterruptHistory...)
	cp.LoopHistory = append([]LoopEntry(nil), ckpt.LoopHistory...)
	cp.ErrorHistory = append([]ErrorEntry(nil), ckpt.ErrorHistory...)
	cp.ScoringSignals = append([]ScoringSignal(nil), ckpt.ScoringSignals...)
	return &cp
}

// Flush forces an immediate save for the given execution, ignoring the
// flush interval. This should be called when an execution completes or
// fails to ensure the final checkpoint is persisted.
func (p *CheckpointPlugin) Flush(ctx context.Context, executionID string) error {
	if p.store == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ckpt := p.snapshots[executionID]
	if ckpt == nil {
		return nil
	}
	return p.saveLocked(ctx, executionID, ckpt)
}

// Cleanup removes the in-memory snapshot for the given execution to
// prevent unbounded map growth. Call this when the execution is fully
// terminated and no further Flush or hook calls will reference it.
func (p *CheckpointPlugin) Cleanup(executionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.snapshots, executionID)
	delete(p.stepCount, executionID)
}

func (p *CheckpointPlugin) saveLocked(ctx context.Context, executionID string, ckpt *ExperienceCheckpoint) error {
	// CreatedAt is set only once (in BeforeStep/AfterStep when the checkpoint
	// is first created); subsequent saves update the state version but not the
	// creation timestamp.
	if p.collector != nil {
		// Drain the collector so each record is merged exactly once.
		// Without this, repeated saves append duplicate route/tool/etc.
		// history entries to the checkpoint.
		p.collector.MergeInto(ckpt)
		p.collector.Reset()
	}
	data, err := json.Marshal(ckpt)
	if err != nil {
		return fmt.Errorf("checkpoint: marshal: %w", err)
	}
	if err := p.store.Save(ctx, CheckpointKey(executionID), data); err != nil {
		return fmt.Errorf("checkpoint: save: %w", err)
	}
	if p.bus != nil {
		p.bus.Emit(ctx, executionID, EventCheckpointSaved, "runtime", map[string]any{
			PayloadKeyExecutionID: executionID,
			"state_version":       ckpt.StateVersion,
		})
		log.Debug("checkpoint saved",
			"execution_id", executionID,
			"state_version", ckpt.StateVersion,
		)
	}
	return nil
}
