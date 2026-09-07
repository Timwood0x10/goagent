package context

import (
	"context"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
)

// TaskMemory stores task-specific context and distillation.
type TaskMemory struct {
	tasks          map[string]*TaskData
	mu             sync.RWMutex
	maxSize        int
	ttl            time.Duration
	cleanupStopCh  chan struct{}
	cleanupOnce    sync.Once
	cleanupWg      sync.WaitGroup
	cleanupRunning bool
}

// TaskData holds task information.
type TaskData struct {
	TaskID     string
	SessionID  string
	UserID     string
	Input      string
	Output     string
	Context    map[string]interface{}
	Steps      []StepRecord
	Results    []ResultRecord
	CreatedAt  time.Time
	AccessedAt time.Time
}

// StepRecord represents a task execution step.
type StepRecord struct {
	Name      string                 `json:"name"`
	Input     string                 `json:"input"`
	Output    string                 `json:"output"`
	Duration  time.Duration          `json:"duration"`
	Metadata  map[string]interface{} `json:"metadata"`
	Timestamp time.Time              `json:"timestamp"`
}

// ResultRecord represents a task result.
type ResultRecord struct {
	Type      string                 `json:"type"`
	Content   string                 `json:"content"`
	Score     float64                `json:"score"`
	Metadata  map[string]interface{} `json:"metadata"`
	Timestamp time.Time              `json:"timestamp"`
}

// NewTaskMemory creates a new TaskMemory.
func NewTaskMemory(maxSize int, ttl time.Duration) *TaskMemory {
	return &TaskMemory{
		tasks:         make(map[string]*TaskData),
		maxSize:       maxSize,
		ttl:           ttl,
		cleanupStopCh: make(chan struct{}),
	}
}

// Start starts the background cleanup goroutine.
// This should be called after creating TaskMemory to enable automatic TTL cleanup.
func (m *TaskMemory) Start(ctx context.Context) {
	m.mu.Lock()
	if m.cleanupRunning {
		m.mu.Unlock()
		return
	}
	m.cleanupRunning = true
	m.mu.Unlock()

	m.cleanupWg.Add(1)
	go m.cleanupLoop(ctx)
}

// Stop stops the background cleanup goroutine.
// This should be called during application shutdown.
func (m *TaskMemory) Stop() {
	m.cleanupOnce.Do(func() {
		close(m.cleanupStopCh)
	})
	m.cleanupWg.Wait()
}

// cleanupLoop runs periodic cleanup of expired tasks.
func (m *TaskMemory) cleanupLoop(ctx context.Context) {
	defer m.cleanupWg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run initial cleanup
	m.cleanupExpired()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.cleanupStopCh:
			return
		case <-ticker.C:
			m.cleanupExpired()
		}
	}
}

// cleanupExpired removes expired tasks based on TTL.
func (m *TaskMemory) cleanupExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	expiredIDs := make([]string, 0)

	for id, task := range m.tasks {
		if now.Sub(task.AccessedAt) > m.ttl {
			expiredIDs = append(expiredIDs, id)
		}
	}

	for _, id := range expiredIDs {
		delete(m.tasks, id)
	}
}

// Get retrieves task data.
func (m *TaskMemory) Get(ctx context.Context, taskID string) (*TaskData, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, false
	}

	if time.Since(task.AccessedAt) > m.ttl {
		delete(m.tasks, taskID)
		return nil, false
	}

	task.AccessedAt = time.Now()
	return task, true
}

// Set stores task data.
func (m *TaskMemory) Set(ctx context.Context, taskID, sessionID, userID, input string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.tasks) >= m.maxSize {
		m.evictOldest()
	}

	task := &TaskData{
		TaskID:     taskID,
		SessionID:  sessionID,
		UserID:     userID,
		Input:      input,
		Context:    make(map[string]interface{}),
		Steps:      make([]StepRecord, 0),
		Results:    make([]ResultRecord, 0),
		CreatedAt:  time.Now(),
		AccessedAt: time.Now(),
	}

	m.tasks[taskID] = task
	return nil
}

// UpdateOutput updates task output.
func (m *TaskMemory) UpdateOutput(ctx context.Context, taskID, output string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	task.Output = output
	task.AccessedAt = time.Now()

	return nil
}

// AddStep adds a step record.
func (m *TaskMemory) AddStep(ctx context.Context, taskID string, step StepRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	step.Timestamp = time.Now()
	task.Steps = append(task.Steps, step)
	task.AccessedAt = time.Now()

	return nil
}

// GetSteps returns task steps.
func (m *TaskMemory) GetSteps(ctx context.Context, taskID string) ([]StepRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}

	steps := make([]StepRecord, len(task.Steps))
	copy(steps, task.Steps)
	return steps, nil
}

// AddResult adds a result record.
func (m *TaskMemory) AddResult(ctx context.Context, taskID string, result ResultRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	result.Timestamp = time.Now()
	task.Results = append(task.Results, result)
	task.AccessedAt = time.Now()

	return nil
}

// GetResults returns task results.
func (m *TaskMemory) GetResults(ctx context.Context, taskID string) ([]ResultRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}

	results := make([]ResultRecord, len(task.Results))
	copy(results, task.Results)
	return results, nil
}

// SetContext sets a context value.
func (m *TaskMemory) SetContext(ctx context.Context, taskID string, key string, value interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	task.Context[key] = value
	task.AccessedAt = time.Now()

	return nil
}

// GetContext returns a context value.
func (m *TaskMemory) GetContext(ctx context.Context, taskID string, key string) (interface{}, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, false
	}

	val, exists := task.Context[key]
	return val, exists
}

// Delete removes a task.
func (m *TaskMemory) Delete(ctx context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.tasks, taskID)
	return nil
}

// Size returns the number of tasks.
func (m *TaskMemory) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.tasks)
}

// evictOldest removes the oldest task.
func (m *TaskMemory) evictOldest() {
	var oldest *TaskData
	var oldestID string

	for id, task := range m.tasks {
		if oldest == nil || task.AccessedAt.Before(oldest.AccessedAt) {
			oldest = task
			oldestID = id
		}
	}

	if oldestID != "" {
		delete(m.tasks, oldestID)
	}
}

// Distill extracts key information from task for future reference.
func (m *TaskMemory) Distill(ctx context.Context, taskID string) (*models.Task, error) {
	m.mu.RLock()
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.RUnlock()
		return nil, ErrTaskNotFound
	}

	taskInput := task.Input
	taskOutput := task.Output
	taskCreatedAt := task.CreatedAt

	contextCopy := make(map[string]interface{}, len(task.Context))
	for k, v := range task.Context {
		contextCopy[k] = v
	}
	m.mu.RUnlock()

	distilled := &models.Task{
		TaskID:   taskID,
		Priority: 0,
		Payload: map[string]any{
			"input":   taskInput,
			"output":  taskOutput,
			"context": contextCopy,
		},
		CreatedAt: taskCreatedAt,
	}

	return distilled, nil
}
