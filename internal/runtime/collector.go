package runtime

import (
	"sync"
	"time"
)

// ExecutionCollector collects execution data across hook calls for
// consumption by CheckpointPlugin, memory distill, and evolution scoring.
// All methods are thread-safe.
type ExecutionCollector struct {
	mu           sync.Mutex
	executionID  string
	routeHistory []RouteRecord
	toolHistory  []ToolRecord
	memoryHits   []MemoryHitRecord
	interruptLog []InterruptRecord
	errorLog     []ErrorRecord
}

// RouteRecord captures a routing decision.
type RouteRecord struct {
	StepID    string    `json:"step_id"`
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason"`
	Source    string    `json:"source"`
	Timestamp time.Time `json:"timestamp"`
}

// ToolRecord captures a tool invocation.
// Duration is stored as time.Duration (nanoseconds). For JSON serialization
// in milliseconds, use DurationMS() instead of the Duration field directly.
type ToolRecord struct {
	StepID   string        `json:"step_id"`
	ToolName string        `json:"tool_name"`
	Input    string        `json:"input"`
	Output   string        `json:"output"`
	Duration time.Duration `json:"duration_ns"`
	Success  bool          `json:"success"`
}

// MemoryHitRecord captures a memory retrieval hit.
type MemoryHitRecord struct {
	StepID    string   `json:"step_id"`
	Query     string   `json:"query"`
	HitCount  int      `json:"hit_count"`
	BestScore float64  `json:"best_score"`
	UsedIDs   []string `json:"used_ids,omitempty"`
}

// InterruptRecord captures a HITL interrupt action.
type InterruptRecord struct {
	StepID   string `json:"step_id"`
	Action   string `json:"action"`
	Feedback string `json:"feedback,omitempty"`
}

// ErrorRecord captures an execution error.
type ErrorRecord struct {
	StepID  string `json:"step_id,omitempty"`
	Message string `json:"message"`
}

// NewExecutionCollector creates a collector for the given execution.
func NewExecutionCollector(executionID string) *ExecutionCollector {
	return &ExecutionCollector{
		executionID:  executionID,
		routeHistory: make([]RouteRecord, 0),
		toolHistory:  make([]ToolRecord, 0),
		memoryHits:   make([]MemoryHitRecord, 0),
		interruptLog: make([]InterruptRecord, 0),
		errorLog:     make([]ErrorRecord, 0),
	}
}

// ExecutionID returns the execution ID this collector is associated with.
func (c *ExecutionCollector) ExecutionID() string {
	return c.executionID
}

// RecordRoute records a routing decision.
func (c *ExecutionCollector) RecordRoute(stepID, decision, reason, source string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routeHistory = append(c.routeHistory, RouteRecord{
		StepID:    stepID,
		Decision:  decision,
		Reason:    reason,
		Source:    source,
		Timestamp: time.Now(),
	})
}

// RecordTool records a tool invocation.
func (c *ExecutionCollector) RecordTool(stepID, toolName, input, output string, duration time.Duration, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolHistory = append(c.toolHistory, ToolRecord{
		StepID:   stepID,
		ToolName: toolName,
		Input:    input,
		Output:   output,
		Duration: duration,
		Success:  success,
	})
}

// RecordMemoryHit records a memory retrieval hit.
func (c *ExecutionCollector) RecordMemoryHit(stepID, query string, hitCount int, bestScore float64, usedIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, len(usedIDs))
	copy(ids, usedIDs)
	c.memoryHits = append(c.memoryHits, MemoryHitRecord{
		StepID:    stepID,
		Query:     query,
		HitCount:  hitCount,
		BestScore: bestScore,
		UsedIDs:   ids,
	})
}

// RecordInterrupt records a HITL interrupt action.
func (c *ExecutionCollector) RecordInterrupt(stepID, action, feedback string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interruptLog = append(c.interruptLog, InterruptRecord{
		StepID:   stepID,
		Action:   action,
		Feedback: feedback,
	})
}

// RecordError records an execution error.
func (c *ExecutionCollector) RecordError(stepID, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorLog = append(c.errorLog, ErrorRecord{
		StepID:  stepID,
		Message: message,
	})
}

// RouteHistory returns a copy of the route history.
func (c *ExecutionCollector) RouteHistory() []RouteRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := make([]RouteRecord, len(c.routeHistory))
	copy(r, c.routeHistory)
	return r
}

// ToolHistory returns a copy of the tool history.
func (c *ExecutionCollector) ToolHistory() []ToolRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := make([]ToolRecord, len(c.toolHistory))
	copy(r, c.toolHistory)
	return r
}

// MemoryHits returns a copy of the memory hit records.
func (c *ExecutionCollector) MemoryHits() []MemoryHitRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := make([]MemoryHitRecord, len(c.memoryHits))
	copy(r, c.memoryHits)
	return r
}

// InterruptLog returns a copy of the interrupt records.
func (c *ExecutionCollector) InterruptLog() []InterruptRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := make([]InterruptRecord, len(c.interruptLog))
	copy(r, c.interruptLog)
	return r
}

// ErrorLog returns a copy of the error records.
func (c *ExecutionCollector) ErrorLog() []ErrorRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := make([]ErrorRecord, len(c.errorLog))
	copy(r, c.errorLog)
	return r
}

// Export returns a deep copy of all collected data as a serializable map.
func (c *ExecutionCollector) Export() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	route := make([]RouteRecord, len(c.routeHistory))
	copy(route, c.routeHistory)
	tool := make([]ToolRecord, len(c.toolHistory))
	copy(tool, c.toolHistory)
	mem := make([]MemoryHitRecord, len(c.memoryHits))
	copy(mem, c.memoryHits)
	interrupt := make([]InterruptRecord, len(c.interruptLog))
	copy(interrupt, c.interruptLog)
	errs := make([]ErrorRecord, len(c.errorLog))
	copy(errs, c.errorLog)
	return map[string]any{
		PayloadKeyExecutionID: c.executionID,
		"route_history":       route,
		"tool_history":        tool,
		"memory_hits":         mem,
		"interrupt_log":       interrupt,
		"error_log":           errs,
	}
}

// Reset clears all collected data for the same execution ID.
func (c *ExecutionCollector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routeHistory = make([]RouteRecord, 0)
	c.toolHistory = make([]ToolRecord, 0)
	c.memoryHits = make([]MemoryHitRecord, 0)
	c.interruptLog = make([]InterruptRecord, 0)
	c.errorLog = make([]ErrorRecord, 0)
}

// Import restores collected data from an Export()-format map.
// Only non-nil keys present in the map are restored; other histories are
// left unchanged. This is used by the resume path to restore pre-checkpoint
// observability and evolution data.
func (c *ExecutionCollector) Import(data map[string]any) {
	if data == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if routes, ok := data["route_history"].([]RouteRecord); ok {
		c.routeHistory = append(c.routeHistory, routes...)
	}
	if tools, ok := data["tool_history"].([]ToolRecord); ok {
		c.toolHistory = append(c.toolHistory, tools...)
	}
	if mems, ok := data["memory_hits"].([]MemoryHitRecord); ok {
		c.memoryHits = append(c.memoryHits, mems...)
	}
	if interrupts, ok := data["interrupt_log"].([]InterruptRecord); ok {
		c.interruptLog = append(c.interruptLog, interrupts...)
	}
	if errs, ok := data["error_log"].([]ErrorRecord); ok {
		c.errorLog = append(c.errorLog, errs...)
	}
}

// MergeInto copies collector data into an ExperienceCheckpoint.
// This is called before the checkpoint is saved so that route, tool,
// memory, interrupt, and error data collected by plugins is included.
func (c *ExecutionCollector) MergeInto(ckpt *ExperienceCheckpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.routeHistory {
		ckpt.RouteHistory = append(ckpt.RouteHistory, RouteEntry{
			FromStepID: r.StepID,
			ToStepID:   r.Decision,
			Reason:     r.Reason,
		})
	}
	for _, t := range c.toolHistory {
		ckpt.ToolHistory = append(ckpt.ToolHistory, ToolEntry{
			StepID:   t.StepID,
			ToolName: t.ToolName,
			Input:    t.Input,
			Output:   t.Output,
			Duration: t.Duration,
			Success:  t.Success,
		})
	}
	for _, m := range c.memoryHits {
		ckpt.MemoryHits = append(ckpt.MemoryHits, MemoryEntry{
			StepID:     m.StepID,
			Similarity: m.BestScore,
			TaskID:     "",
		})
	}
	for _, i := range c.interruptLog {
		ckpt.InterruptHistory = append(ckpt.InterruptHistory, InterruptEntry{
			StepID:   i.StepID,
			Approved: i.Action == "approve",
			Feedback: i.Feedback,
		})
	}
	for _, e := range c.errorLog {
		ckpt.ErrorHistory = append(ckpt.ErrorHistory, ErrorEntry(e))
	}
}
