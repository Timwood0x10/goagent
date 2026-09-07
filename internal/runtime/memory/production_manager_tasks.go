package memory

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"strconv"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/evidence"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/services"
)

// Repeated string constants for goconst compliance.
const (
	agentID        = "style-agent"
	embeddingModel = "intfloat/e5-large"
	keyTaskID      = "task_id"
	keyOutput      = "output"
	keyInput       = "input"
	keyScore       = "score"
	keyContent     = "content"
)

// CreateTask creates a new task and returns the task ID.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// userID - user identifier.
// input - task input.
// Returns task ID or error if creation fails.
// Note: This creates task_result WITHOUT embedding (embedding only for experiences).
// task_results table stores execution history, experiences store reusable knowledge.
func (m *ProductionMemoryManager) CreateTask(ctx context.Context, sessionID, userID, input string) (string, error) {
	taskID := "task_" + strconv.FormatInt(time.Now().UnixNano(), 10)

	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; see the tenant-scope note in CreateConversation.
	tenantID := m.getCurrentTenantID()

	// Create task result record (NO embedding, only for execution history)
	taskResult := &storage_models.TaskResult{
		ID:               taskID,
		TenantID:         tenantID,
		SessionID:        sessionID,
		TaskType:         "user_request",
		AgentID:          agentID,
		Input:            map[string]interface{}{keyContent: input},
		Output:           nil,
		Embedding:        nil, // No embedding for task results
		EmbeddingModel:   embeddingModel,
		EmbeddingVersion: 1,
		Status:           "pending",
		Metadata:         make(map[string]interface{}),
	}

	if err := m.taskResultRepository.Create(ctx, taskResult); err != nil {
		return "", errors.Wrap(err, "create task result")
	}

	log.Debug("Task created", "task_id", taskID, "session_id", sessionID)
	return taskID, nil
}

// CreateTaskWithID creates a task using a caller-assigned ID. It validates
// the ID is non-empty and stores the task under that exact ID so the
// caller's tracking ID matches the stored task (and any cached result).
func (m *ProductionMemoryManager) CreateTaskWithID(ctx context.Context, taskID, sessionID, userID, input string) error {
	if taskID == "" {
		return errors.New("task ID cannot be empty")
	}

	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; the session-based SetTenantContext was
	// removed — its SET LOCAL evaporated under autocommit on a pooled
	// connection, providing no isolation.
	tenantID := m.getCurrentTenantID()

	taskResult := &storage_models.TaskResult{
		ID:               taskID,
		TenantID:         tenantID,
		SessionID:        sessionID,
		TaskType:         "user_request",
		AgentID:          agentID,
		Input:            map[string]interface{}{keyContent: input},
		Output:           nil,
		Embedding:        nil, // No embedding for task results
		EmbeddingModel:   embeddingModel,
		EmbeddingVersion: 1,
		Status:           "pending",
		Metadata:         make(map[string]interface{}),
	}

	if err := m.taskResultRepository.Create(ctx, taskResult); err != nil {
		return errors.Wrap(err, "create task result with id")
	}

	log.Debug("Task created with id", "task_id", taskID, "session_id", sessionID)
	return nil
}

// UpdateTaskOutput updates the task output.
// Args:
// ctx - database operation context.
// taskID - task identifier.
// output - task output.
// Returns error if update fails.
func (m *ProductionMemoryManager) UpdateTaskOutput(ctx context.Context, taskID, output string) error {
	if taskID == "" {
		return errors.New("task ID cannot be empty")
	}

	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; the session-based SetTenantContext was
	// removed — its SET LOCAL evaporated under autocommit on a pooled
	// connection, providing no isolation.
	tenantID := m.getCurrentTenantID()

	// Get existing task
	task, err := m.taskResultRepository.GetByID(ctx, tenantID, taskID)
	if err != nil {
		return errors.Wrap(err, "get task result")
	}

	// Update task
	task.Output = map[string]interface{}{"content": output}
	task.Status = "completed"
	task.LatencyMs = int(time.Since(task.CreatedAt).Milliseconds())

	if err := m.taskResultRepository.Update(ctx, task); err != nil {
		return errors.Wrap(err, "update task result")
	}

	log.Debug("Task output updated", "task_id", taskID)
	return nil
}

// DistillTask extracts key information from task for future reference.
// Args:
// ctx - database operation context.
// taskID - task identifier.
// Returns distilled task or error if distillation fails.
// Note: This retrieves stored task result and converts to Task format.
func (m *ProductionMemoryManager) DistillTask(ctx context.Context, taskID string) (*models.Task, error) {
	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; the session-based SetTenantContext was
	// removed — its SET LOCAL evaporated under autocommit on a pooled
	// connection, providing no isolation.
	tenantID := m.getCurrentTenantID()

	// Get task result
	taskResult, err := m.taskResultRepository.GetByID(ctx, tenantID, taskID)
	if err != nil {
		return nil, errors.Wrap(err, "get task result")
	}

	// Convert to models.Task format
	task := &models.Task{
		TaskID:    taskResult.ID,
		SessionID: taskResult.SessionID, // #61: carry the owning session when known
		TaskType:  models.AgentType(taskResult.TaskType),
		Payload:   taskResult.Input,
		Priority:  50, // Default priority
		CreatedAt: taskResult.CreatedAt,
	}

	// Emit memory distilled event.
	inputCount := 0
	if content, ok := taskResult.Input["content"].(string); ok {
		inputCount = len(content)
	}
	m.emitEvent(ctx, ares_events.EventMemoryDistilled, map[string]any{
		keyTaskID:     taskID,
		"input_count": inputCount,
	})

	log.Debug("Task distilled", "task_id", taskID)
	return task, nil
}

// StoreDistilledTask stores a distilled task with async embedding chain.
// Args:
// ctx - database operation context.
// taskID - task identifier.
// distilled - distilled task data.
// Returns error if storage fails.
// Note: Per design standard, this uses asynchronous embedding chain:
// 1. Write to DB with embedding_status = 'pending'
// 2. Write to embedding_queue with dedupe_key (for deduplication)
// 3. Background Worker processes embedding tasks
// 4. Worker updates DB with embedding and status = 'completed'
func (m *ProductionMemoryManager) StoreDistilledTask(ctx context.Context, taskID string, distilled *models.Task) error {
	if distilled == nil {
		return errors.New("distilled task cannot be nil")
	}

	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; the session-based SetTenantContext was
	// removed — its SET LOCAL evaporated under autocommit on a pooled
	// connection, providing no isolation.
	tenantID := m.getCurrentTenantID()

	// Extract problem and solution from distilled payload.
	inputStr, ok := distilled.Payload["input"].(string)
	if !ok {
		log.Warn("StoreProductionMemory: missing or invalid input", keyTaskID, taskID)
		inputStr = ""
	}
	outputStr, ok := distilled.Payload[keyOutput].(string)
	if !ok {
		log.Warn("StoreProductionMemory: missing or invalid output", keyTaskID, taskID)
		outputStr = ""
	}

	// Build canonical memory experience spec for unified embedding.
	spec := memembed.BuildMemoryExperienceSpec(string(evidence.KindKnowledge), inputStr, outputStr, m.embeddingClient.GetModel(), 1, 0)

	// Use write buffer for async embedding chain (write backpressure layer per design standard)
	writeItem := &postgres.WriteItem{
		TenantID:   tenantID,
		Table:      "experiences_1024",
		Content:    spec.Text, // Canonical memory experience text, not payload dump
		SpecKind:   string(spec.Kind),
		SpecPrefix: spec.Prefix,
		SpecDim:    0,
		SpecHash:   spec.Hash,
		Metadata: map[string]interface{}{
			keyOutput:  outputStr,
			"type":     "solution",
			"agent_id": agentID,
		},
	}

	if err := m.writeBuffer.Write(ctx, writeItem); err != nil {
		return errors.Wrap(err, "write to buffer")
	}

	// Emit evidence to the unified Evidence Store.
	if m.evidenceCollector != nil {
		_ = m.evidenceCollector.Emit(ctx, string(evidence.KindKnowledge), map[string]any{
			keyTaskID:  taskID,
			keyInput:   inputStr,
			keyOutput:  outputStr,
			"agent_id": agentID,
		}, "source", "memory", "type", "distillation")
	}

	// Emit memory distilled event.
	m.emitEvent(ctx, ares_events.EventMemoryDistilled, map[string]any{
		keyTaskID:      taskID,
		"output_count": 1,
	})

	log.Debug("Distilled task queued for async embedding", keyTaskID, taskID)
	return nil
}

// SearchSimilarTasks searches for similar tasks using intelligent retrieval.
// Args:
// ctx - database operation context.
// query - search query.
// limit - maximum number of results.
// Returns list of similar tasks or error if search fails.
// Note: This returns experiences (agent knowledge) rather than execution tasks.
func (m *ProductionMemoryManager) SearchSimilarTasks(ctx context.Context, query string, limit int) ([]*models.Task, error) {
	if query == "" {
		return nil, errors.New("query cannot be empty")
	}

	// Tenant scope flows via explicit tenantID parameters on repository
	// methods; the session-based SetTenantContext was
	// removed — its SET LOCAL evaporated under autocommit on a pooled
	// connection, providing no isolation.
	tenantID := m.getCurrentTenantID()

	// Create search request
	searchRequest := &services.SearchRequest{
		Query:    query,
		TenantID: tenantID,
		TopK:     limit,
		Plan:     services.DefaultRetrievalPlan(),
	}

	// Enable experience search only (hybrid search: vector + BM25)
	searchRequest.Plan.SearchExperience = true
	searchRequest.Plan.SearchKnowledge = false
	searchRequest.Plan.SearchTools = false
	searchRequest.Plan.ExperienceWeight = 1.0

	// Execute search with fallback (per design standard)
	results, err := m.retrievalService.Search(ctx, searchRequest)
	if err != nil {
		return nil, errors.Wrap(err, "search similar tasks")
	}

	// Convert experiences to models.Task format
	tasks := make([]*models.Task, 0, len(results))
	for _, result := range results {
		if result.Source == "experience" {
			// Convert experience to Task format for backward compatibility
			task := &models.Task{
				TaskID:   result.ID,
				TaskType: models.AgentType("experience"),
				Payload: map[string]any{
					keyInput:  result.Content,
					keyOutput: result.Metadata[keyOutput],
					keyScore:  result.Score,
				},
				Priority:  int(result.Score * 100), // Convert score to priority
				CreatedAt: result.CreatedAt,
			}
			tasks = append(tasks, task)
		}
	}

	log.Debug("Similar experiences found", "query", query, "count", len(tasks))
	return tasks, nil
}
