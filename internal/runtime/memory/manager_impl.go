// Package memory provides unified memory management for the StyleAgent framework.
package memory

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents/lease"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	apiembed "github.com/Timwood0x10/ares/internal/embedding"
	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

// memoryManager implements MemoryManager interface.
// It coordinates session memory, task memory, and distilled task storage.
type memoryManager struct {
	sessionMemory *memctx.SessionMemory
	taskMemory    *memctx.TaskMemory
	mu            sync.RWMutex
	config        *MemoryConfig
	started       bool
	stopped       bool

	// Distillation components (nil when using NewMemoryManager without distiller).
	distiller *distillation.Distiller
	embedder  apiembed.EmbeddingService
	expRepo   distillation.ExperienceRepository

	// EmbeddingPipeline: unified embedding generation for memory and query paths.
	pipeline memembed.EmbeddingPipeline

	// Event sourcing: optional EventStore for emitting lifecycle ares_events.
	eventStore ares_events.EventStore
	streamID   string // Stream ID used when appending ares_events.

	// ContextCleaner: strips tool call noise and repetitive content before LLM calls.
	ctxCleaner *memctx.ContextCleaner

	// retrievers hold optional ContextRetrievers (MemoryRetriever, KnowledgeRetriever
	// adapter, etc.) queried in BuildContext/BuildPromptMessages when config.EnableRAG
	// is true. Populated post-construction via SetRetrievers.
	retrievers []memctx.ContextRetriever

	// skillsRegistry, when non-nil, injects the skill descriptions (name +
	// one-line description only — progressive disclosure) into BuildContext so
	// capabilities are always resident while full details load on demand via
	// the registry. Populated via SetSkillsRegistry.
	skillsRegistry *skills.Registry

	// leaseMgr, when non-nil, provides session-level leases for concurrent
	// access control (ares-vs-prime-agent 5.3: session lease). Upper layers
	// acquire a lease before mutating a session so concurrent workers cannot
	// clobber each other. Populated via SetLeaseManager.
	leaseMgr *lease.Manager

	// defaultTenantID is the tenant ID used for search operations when none is
	// explicitly provided. Must match the tenant used during write (StoreDistilledTask).
	// Default: "default". Override via SetDefaultTenantID.
	defaultTenantID string
}

// SetSkillsRegistry attaches a skills registry for progressive disclosure.
// When set, BuildContext prepends a resident "Available skills" block listing
// each skill's name and description only; full skill details are fetched on
// demand by ID via the registry. nil detaches the block.
func (m *memoryManager) SetSkillsRegistry(reg *skills.Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skillsRegistry = reg
}

// SetLeaseManager attaches a lease.Manager for session-level concurrency
// control. nil disables leasing (existing behavior unchanged). The manager is
// shared by all sessions; upper layers call AcquireSessionLease before
// mutating a session and ReleaseSessionLease when done.
func (m *memoryManager) SetLeaseManager(mgr *lease.Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseMgr = mgr
}

// AcquireSessionLease takes an exclusive, expiring lease on a session for the
// given owner. It fails when leasing is not configured or the session is
// already held by another owner.
func (m *memoryManager) AcquireSessionLease(ctx context.Context, sessionID, owner string, ttl time.Duration) (lease.Lease, error) {
	m.mu.RLock()
	mgr := m.leaseMgr
	m.mu.RUnlock()
	if mgr == nil {
		return lease.Lease{}, errors.New("memory: session leasing not configured")
	}
	return mgr.Acquire(ctx, sessionID, owner, ttl)
}

// ReleaseSessionLease surrenders the lease on a session. Only the owner may
// release it.
func (m *memoryManager) ReleaseSessionLease(ctx context.Context, sessionID, owner string) error {
	m.mu.RLock()
	mgr := m.leaseMgr
	m.mu.RUnlock()
	if mgr == nil {
		return errors.New("memory: session leasing not configured")
	}
	return mgr.Release(ctx, sessionID, owner)
}

// NewMemoryManager creates a new MemoryManager with the given configuration.
// For distillation support, use NewMemoryManagerWithDistiller.
func NewMemoryManager(config *MemoryConfig) (MemoryManager, error) {
	if config == nil {
		config = DefaultMemoryConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}

	sessionMemory := memctx.NewSessionMemory(
		config.MaxSessions,
		config.SessionTTL,
	)

	taskMemory := memctx.NewTaskMemory(
		config.MaxTasks,
		config.TaskTTL,
	)

	return &memoryManager{
		sessionMemory:   sessionMemory,
		taskMemory:      taskMemory,
		config:          config,
		ctxCleaner:      memctx.NewContextCleaner(),
		skillsRegistry:  nil,
		leaseMgr:        lease.NewManager(), // session-level concurrency control (primitive: session lease)
		defaultTenantID: "default",
	}, nil
}

// NewMemoryManagerWithDistiller creates a new MemoryManager with the new distillation engine.
// This is the recommended method for production use.
//
// Args:
//
//	config - memory configuration.
//	embedder - embedding service for generating vectors.
//	expRepo - experience repository for storage and retrieval.
//
// Returns:
//
//	MemoryManager - configured memory manager instance.
//	error - any error encountered.
func NewMemoryManagerWithDistiller(config *MemoryConfig, embedder apiembed.EmbeddingService, expRepo distillation.ExperienceRepository) (MemoryManager, error) {
	if config == nil {
		config = DefaultMemoryConfig()
	}
	if err := config.validate(); err != nil {
		return nil, err
	}

	sessionMemory := memctx.NewSessionMemory(
		config.MaxSessions,
		config.SessionTTL,
	)

	taskMemory := memctx.NewTaskMemory(
		config.MaxTasks,
		config.TaskTTL,
	)

	// Create new distillation engine
	distillConfig := distillation.DefaultDistillationConfig()
	distiller := distillation.NewDistiller(distillConfig, embedder, expRepo)

	pipeline, err := memembed.NewEmbeddingPipeline(embedder)
	if err != nil {
		return nil, fmt.Errorf("create embedding pipeline: %w", err)
	}
	distiller.SetEmbeddingPipeline(pipeline)

	return &memoryManager{
		sessionMemory:   sessionMemory,
		taskMemory:      taskMemory,
		config:          config,
		distiller:       distiller,
		embedder:        embedder,
		pipeline:        pipeline,
		expRepo:         expRepo,
		ctxCleaner:      memctx.NewContextCleaner(),
		defaultTenantID: "default",
	}, nil
}

// GetConfig returns the current MemoryConfig pointer.
// The caller must hold the lock (Lock()) before reading the returned config.
// This method implements the MemoryConfigStore interface.
func (m *memoryManager) GetConfig() *MemoryConfig {
	return m.config
}

// Lock acquires the exclusive lock protecting the config.
// This method implements the MemoryConfigStore interface.
func (m *memoryManager) Lock() {
	m.mu.Lock()
}

// Unlock releases the exclusive lock protecting the config.
// This method implements the MemoryConfigStore interface.
func (m *memoryManager) Unlock() {
	m.mu.Unlock()
}

// SetRetrievers configures the RAG retrievers used by BuildContext and
// BuildPromptMessages. Pass an empty slice to disable retrieval at runtime
// (even when config.EnableRAG is true). Retrieval only fires when
// config.EnableRAG is true AND len(retrievers) > 0.
//
// This method is safe to call before Start; retrievers are read on every
// BuildContext/BuildPromptMessages call. Callers MUST NOT mutate the slice
// after passing it in — make a copy if you need to.
func (m *memoryManager) SetRetrievers(retrievers []memctx.ContextRetriever) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrievers = retrievers
}

// Ensure memoryManager implements MemoryConfigStore.
var _ MemoryConfigStore = (*memoryManager)(nil)

// Start starts the memory manager and background workers.
func (m *memoryManager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return nil
	}

	m.sessionMemory.StartCleanup()
	m.taskMemory.Start(ctx)
	m.started = true

	log.Info("Memory manager started")
	return nil
}

// Stop stops the memory manager and cleans up resources.
// It safely handles nil components and collects all errors encountered during shutdown.
func (m *memoryManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return nil
	}

	var errs []error

	if m.taskMemory != nil {
		m.taskMemory.Stop()
	}

	if m.sessionMemory != nil {
		if err := m.sessionMemory.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close session memory: %w", err))
			log.Warn("Failed to close session memory", "error", err)
		}
	}

	m.stopped = true

	if len(errs) > 0 {
		var msg []string
		for _, e := range errs {
			msg = append(msg, e.Error())
		}
		log.Error("Memory manager stopped with errors", "error_count", len(errs))
		return fmt.Errorf("memory manager stop: %s", strings.Join(msg, "; "))
	}

	log.Info("Memory manager stopped")
	return nil
}

// SetEventStore configures an optional EventStore for emitting lifecycle ares_events.
// If store is nil, event emission is a no-op.
func (m *memoryManager) SetEventStore(store ares_events.EventStore, streamID string) {
	m.eventStore = store
	m.streamID = streamID
}

// emitEvent appends a single event using the canonical ares_events.Emit.
func (m *memoryManager) emitEvent(ctx context.Context, eventType ares_events.EventType, payload map[string]any) {
	if !ares_events.Emit(ctx, m.eventStore, m.streamID, eventType, "memory", payload) {
		log.Warn("failed to emit event", "event_type", eventType, "stream_id", m.streamID)
	}
}

// CreateSession creates a new session and returns the session ID.
func (m *memoryManager) CreateSession(ctx context.Context, userID string) (string, error) {
	// Use both time and userID to ensure uniqueness
	sessionID := fmt.Sprintf("session_%s_%d", userID, time.Now().UnixNano())

	messages := []memctx.Message{
		{
			Role:    "system",
			Content: "New session started",
			Time:    time.Now(),
		},
	}

	if err := m.sessionMemory.Set(ctx, sessionID, userID, messages); err != nil {
		return "", errors.Wrap(err, "create session")
	}

	// Emit session created event.
	m.emitEvent(ctx, ares_events.EventSessionCreated, map[string]any{
		"session_id": sessionID,
		"user_id":    userID,
	})

	log.Debug("Session created", "session_id", sessionID, "user_id", userID)
	return sessionID, nil
}

// AddMessage adds a message to the session.
func (m *memoryManager) AddMessage(ctx context.Context, sessionID, role, content string) error {
	msg := memctx.Message{
		Role:    role,
		Content: content,
		Time:    time.Now(),
	}

	if err := m.sessionMemory.AddMessage(ctx, sessionID, msg); err != nil {
		return errors.Wrap(err, "add message")
	}

	// Emit message added event.
	m.emitEvent(ctx, ares_events.EventMessageAdded, map[string]any{
		"session_id": sessionID,
		"role":       role,
	})

	log.Debug("Message added", "session_id", sessionID, "role", role)
	return nil
}

// GetMessages retrieves all messages from the session.
func (m *memoryManager) GetMessages(ctx context.Context, sessionID string) ([]Message, error) {
	sessionMemMessages, err := m.sessionMemory.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "get messages")
	}

	return sessionMemMessages, nil
}

// AddStructuredMessage adds a structured message with full metadata (TurnID, ToolCallID, ToolCalls)
// to the session. The underlying SessionMemory stores all Message fields faithfully.
func (m *memoryManager) AddStructuredMessage(ctx context.Context, sessionID string, msg Message) error {
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}
	if err := m.sessionMemory.AddMessage(ctx, sessionID, msg); err != nil {
		return errors.Wrap(err, "add structured message")
	}

	m.emitEvent(ctx, ares_events.EventMessageAdded, map[string]any{
		"session_id": sessionID,
		"role":       msg.Role,
	})
	return nil
}

// BuildPromptMessages returns all messages as []Message without folding into a flat string.
// This is the structured counterpart of BuildContext — it preserves the original message
// structure (role, content, tool calls, turn IDs) for LLM prompt construction.
//
// When config.EnableRAG is true and retrievers are configured, a system Message
// containing retrieved context (past experiences + AKG knowledge) is prepended
// to the cleaned history. The retrieval query is the last user message in the
// session; when no user message exists, retrieval is skipped.
func (m *memoryManager) BuildPromptMessages(ctx context.Context, sessionID string) ([]Message, error) {
	messages, err := m.sessionMemory.GetMessages(ctx, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "build prompt messages")
	}

	// Snapshot config fields under RLock so concurrent MemoryPatchExecutor
	// Apply calls (which mutate the config under the write lock) do not race
	// with these reads (runRetrieval does the same for its fields).
	m.mu.RLock()
	maxHistory := m.config.MaxHistory
	var cleanOpts *memctx.CleanOptions
	if m.config.CleanOptions != nil {
		snap := *m.config.CleanOptions
		cleanOpts = &snap
	}
	m.mu.RUnlock()

	// Apply max-history limit
	if len(messages) > maxHistory {
		messages = messages[len(messages)-maxHistory:]
	}

	// Apply intelligent context cleaning with configured options
	var opts []memctx.CleanOptions
	if cleanOpts != nil {
		opts = []memctx.CleanOptions{*cleanOpts}
	}
	cleaned := m.ctxCleaner.CleanWithTurns(messages, opts...)

	stats := m.ctxCleaner.Stats()
	if stats.BytesSaved > 0 || stats.DroppedToolMessages > 0 {
		log.Debug("Prompt messages cleaned", "session_id", sessionID,
			"history_in", stats.HistoryIn,
			"history_out", stats.HistoryOut,
			"bytes_saved", stats.BytesSaved,
			"dropped_tool_msgs", stats.DroppedToolMessages,
			"turns_processed", stats.TurnsProcessed)
	}

	// RAG injection: prepend retrieved context as a system Message when enabled.
	retrieved := m.retrieveForPrompt(ctx, lastUserMessage(messages))
	if len(retrieved) > 0 {
		cleaned = append(retrieved, cleaned...)
	}
	return cleaned, nil
}

// DeleteSession deletes a session and all its messages immediately.
func (m *memoryManager) DeleteSession(ctx context.Context, sessionID string) error {
	if err := m.sessionMemory.Delete(ctx, sessionID); err != nil {
		return errors.Wrap(err, "delete session")
	}

	log.Debug("Session deleted", "session_id", sessionID)
	return nil
}

// BuildContext builds input with conversation history context.
func (m *memoryManager) BuildContext(ctx context.Context, input string, sessionID string) (string, error) {
	messages, err := m.GetMessages(ctx, sessionID)
	if err != nil {
		// A missing session is the normal first-interaction case: there is
		// no history to build context from, so degrade gracefully to the
		// raw input instead of failing. Real retrieval errors propagate.
		if stderrors.Is(err, memctx.ErrSessionNotFound) {
			return input, nil
		}
		return "", errors.Wrap(err, "get messages")
	}

	// Keep only last N messages to avoid long context. Snapshot under RLock
	// (see BuildPromptMessages) so config patches do not race with the read.
	m.mu.RLock()
	maxHistory := m.config.MaxHistory
	m.mu.RUnlock()
	if len(messages) > maxHistory {
		messages = messages[len(messages)-maxHistory:]
	}

	// Apply intelligent context cleaning: strip tool noise, compress verbose content.
	cleaned := m.ctxCleaner.Clean(messages)

	// Build context string.
	var contextBuilder strings.Builder
	contextBuilder.Grow(len(cleaned) * 256)

	// Skills (progressive disclosure): prepend a resident block listing only
	// each skill's name + one-line description when a registry is attached.
	// Full skill details are loaded on demand by ID via the registry, not
	// injected here — keeping the resident context small.
	if m.skillsRegistry != nil {
		skills := m.skillsRegistry.List()
		if len(skills) > 0 {
			contextBuilder.WriteString("Available skills:\n")
			for _, sk := range skills {
				contextBuilder.WriteString("- " + sk.Name)
				if sk.Description != "" {
					contextBuilder.WriteString(": " + sk.Description)
				}
				contextBuilder.WriteString("\n")
			}
			contextBuilder.WriteString("Request a skill's full detail by name when needed.\n\n")
		}
	}

	// RAG injection: prepend retrieved context (past experiences + AKG knowledge)
	// before the conversation history when EnableRAG is true and retrievers are
	// configured. The current input is used as the retrieval query.
	if ragContext := m.retrieveContextString(ctx, input); ragContext != "" {
		contextBuilder.WriteString(ragContext)
		contextBuilder.WriteString("\n")
	}

	if len(cleaned) > 0 {
		contextBuilder.WriteString("Previous conversation history:\n\n")
		for _, msg := range cleaned {
			switch msg.Role {
			case memctx.RoleUser:
				fmt.Fprintf(&contextBuilder, "User: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			case memctx.RoleAssistant:
				fmt.Fprintf(&contextBuilder, "Assistant: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			case memctx.RoleToolCall:
				fmt.Fprintf(&contextBuilder, "Tool call: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			case memctx.RoleToolResult:
				fmt.Fprintf(&contextBuilder, "Tool result: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			case memctx.RoleSystem:
				fmt.Fprintf(&contextBuilder, "System: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			default:
				fmt.Fprintf(&contextBuilder, "Unknown: %s\n", truncpkg.WithEllipsis(msg.Content, 100))
			}
		}
		contextBuilder.WriteString("\nCurrent request:\n")
	}
	contextBuilder.WriteString(input)

	// Emit cleaner stats periodically for observability.
	stats := m.ctxCleaner.Stats()
	if stats.BytesSaved > 0 {
		log.Debug("Context cleaned", "session_id", sessionID,
			"history_in", stats.HistoryIn,
			"history_out", stats.HistoryOut,
			"bytes_saved", stats.BytesSaved,
			"tool_calls", stats.ToolCalls)
	}

	log.Debug("Context built", "session_id", sessionID, "history_length", len(cleaned))
	return contextBuilder.String(), nil
}

// CreateTask creates a new task and returns the task ID.
func (m *memoryManager) CreateTask(ctx context.Context, sessionID, userID, input string) (string, error) {
	taskID := "task_" + strconv.FormatInt(time.Now().UnixNano(), 10)

	if err := m.taskMemory.Set(ctx, taskID, sessionID, userID, input); err != nil {
		return "", errors.Wrap(err, "create task")
	}

	log.Debug("Task created", "task_id", taskID, "session_id", sessionID)
	return taskID, nil
}

// CreateTaskWithID creates a task using a caller-assigned ID. It validates
// the ID is non-empty, then stores the task under that exact ID so the
// caller's tracking ID matches the stored task (and any cached result).
func (m *memoryManager) CreateTaskWithID(ctx context.Context, taskID, sessionID, userID, input string) error {
	if taskID == "" {
		return errors.Wrap(errors.ErrInvalidArgument, "create task with id")
	}
	if err := m.taskMemory.Set(ctx, taskID, sessionID, userID, input); err != nil {
		return errors.Wrap(err, "create task with id")
	}
	log.Debug("Task created with id", "task_id", taskID, "session_id", sessionID)
	return nil
}

// UpdateTaskOutput updates the task output.
func (m *memoryManager) UpdateTaskOutput(ctx context.Context, taskID, output string) error {
	if err := m.taskMemory.UpdateOutput(ctx, taskID, output); err != nil {
		return errors.Wrap(err, "update task output")
	}

	log.Debug("Task output updated", "task_id", taskID)
	return nil
}

// DistillTask extracts key information from task for future reference.
func (m *memoryManager) DistillTask(ctx context.Context, taskID string) (*models.Task, error) {
	log.Info("[Memory Distillation] Starting task distillation", "task_id", taskID)

	task, err := m.taskMemory.Distill(ctx, taskID)
	if err != nil {
		return nil, errors.Wrap(err, "distill task")
	}

	inputStr, ok := task.Payload["input"].(string)
	if !ok {
		log.Warn("distill: missing or invalid input", "task_id", taskID)
		inputStr = ""
	}

	m.emitEvent(ctx, ares_events.EventMemoryDistilled, map[string]any{
		"task_id":     taskID,
		"input_count": len(inputStr),
	})

	log.Info("[Memory Distillation] Task distilled successfully",
		"task_id", taskID,
		"input_length", len(inputStr))

	return task, nil
}

// StoreDistilledTask stores a distilled task using the distillation engine.
// The input is cleaned through the context cleaner before being passed to the distiller.
// Session messages (if available) are used to provide rich tool-result-summarized history.
func (m *memoryManager) StoreDistilledTask(ctx context.Context, taskID string, distilled *models.Task) error {
	if distilled == nil {
		return errors.New("distilled task cannot be nil")
	}
	if m.distiller == nil || m.expRepo == nil {
		return errors.New("distillation engine not initialized, use NewMemoryManagerWithDistiller")
	}

	log.Info("[Memory Distillation] Storing distilled task", "task_id", taskID)

	inputStr, ok := distilled.Payload["input"].(string)
	if !ok {
		log.Warn("StoreDistilledTask: missing or invalid input", "task_id", taskID)
		inputStr = ""
	}
	outputStr, ok := distilled.Payload["output"].(string)
	if !ok {
		log.Warn("StoreDistilledTask: missing or invalid output", "task_id", taskID)
		outputStr = ""
	}

	// Try to get cleaned session messages for richer distillation input.
	distMessages := m.buildCleanedDistillationMessages(ctx, taskID, inputStr, outputStr)

	userID, ok := distilled.Payload["user_id"].(string)
	if !ok {
		log.Warn("StoreDistilledTask: missing or invalid user_id", "task_id", taskID)
		userID = ""
	}
	tenantID, ok := distilled.Payload["tenant_id"].(string)
	if !ok {
		log.Warn("StoreDistilledTask: missing or invalid tenant_id", "task_id", taskID)
		tenantID = ""
	}
	if tenantID == "" {
		tenantID = "default"
	}

	memories, err := m.distiller.DistillConversation(ctx, taskID, distMessages, tenantID, userID)
	if err != nil {
		return errors.Wrap(err, "distill conversation")
	}

	var storedCount int64
	g, storeCtx := errgroup.WithContext(ctx)
	for _, mem := range memories {
		mem := mem
		g.Go(func() error {
			problem, ok := mem.Metadata["problem"].(string)
			if !ok {
				log.Warn("StoreDistilledTask: missing or invalid problem in memory metadata", "task_id", taskID)
				problem = ""
			}
			solution, ok := mem.Metadata["solution"].(string)
			if !ok {
				log.Warn("StoreDistilledTask: missing or invalid solution in memory metadata", "task_id", taskID)
				solution = ""
			}
			confidence, ok := mem.Metadata["confidence"].(float64)
			if !ok {
				log.Warn("StoreDistilledTask: missing or invalid confidence in memory metadata", "task_id", taskID)
				confidence = 0
			}
			extractionMethodStr, ok := mem.Metadata["extraction_method"].(string)
			if !ok {
				log.Warn("StoreDistilledTask: missing or invalid extraction_method in memory metadata", "task_id", taskID)
				extractionMethodStr = ""
			}
			if extractionMethodStr == "" {
				extractionMethodStr = string(distillation.ExtractionDirect)
			}

			exp := &distillation.Experience{
				Type:             mem.Type,
				Problem:          problem,
				Solution:         solution,
				Confidence:       confidence,
				ExtractionMethod: distillation.ExtractionMethod(extractionMethodStr),
				Vector:           mem.Vector,
			}

			if err := m.expRepo.Create(storeCtx, exp); err != nil {
				log.Error("[Memory Distillation] Failed to store experience",
					"task_id", taskID, "error", err)
				return errors.Wrap(err, "store experience")
			}
			atomic.AddInt64(&storedCount, 1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// errgroup returns the first error from the goroutines.
		// Log it for observability and propagate to the caller.
		log.Error("memory manager: background task failed", "error", err)
		return errors.Wrap(err, "store distilled experiences")
	}

	// Note: there is no "all experiences failed to store" check here. errgroup.Wait
	// returns the first non-nil error if ANY goroutine fails, so reaching this point
	// means every goroutine returned nil and storedCount == len(memories). A
	// `len(memories) > 0 && storedCount == 0` branch would be unreachable dead code.
	m.emitEvent(ctx, ares_events.EventMemoryDistilled, map[string]any{
		"task_id":      taskID,
		"output_count": storedCount,
	})

	log.Info("[Memory Distillation] Distillation completed",
		"task_id", taskID,
		"memories_created", storedCount)

	return nil
}

// SearchSimilarTasks searches for similar tasks using vector-based search.
func (m *memoryManager) SearchSimilarTasks(ctx context.Context, query string, limit int) ([]*models.Task, error) {
	if m.pipeline == nil || m.expRepo == nil {
		return nil, errors.New("distillation engine not initialized, use NewMemoryManagerWithDistiller")
	}

	log.Info("[Memory Search] Searching for similar tasks",
		"query", truncpkg.WithEllipsis(query, 50),
		"limit", limit)

	spec := memembed.BuildMemoryQuerySpec(query, m.pipeline.Model(), 1, 0)
	queryVector, err := m.pipeline.Embed(ctx, spec)
	if err != nil {
		return nil, errors.Wrap(err, "generate query embedding")
	}

	experiences, err := m.expRepo.SearchByVector(ctx, queryVector, m.defaultTenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "search experiences")
	}

	tasks := make([]*models.Task, 0, limit)
	for i, exp := range experiences {
		task := &models.Task{
			TaskID: fmt.Sprintf("exp_%d_search", i),
			Payload: map[string]any{
				"input":  exp.Problem,
				"output": exp.Solution,
				"context": map[string]interface{}{
					"confidence":        exp.Confidence,
					"extraction_method": string(exp.ExtractionMethod),
					"source":            "experience_repository",
					"similarity_rank":   i + 1,
				},
			},
		}
		tasks = append(tasks, task)
	}

	log.Info("[Memory Search] Search completed",
		"results_count", len(tasks),
		"limit", limit)

	return tasks, nil
}

// GetLatestSessionForAgent returns the most recent session ID for an agent.
//
// The in-memory memoryManager does not persist agent checkpoints: sessions are
// keyed by session ID (each carrying a UserID, not an agent ID), and there
// is no agent->session mapping. Without that mapping any "lookup" would either
// silently return empty (the previous behavior, which hid the limitation and
// broke agent recovery in manager_lifecycle.go) or conflate the session's
// UserID with the agent ID and return wrong results.
//
// Per rule 0.2 we do not fake an implementation. Instead we return
// ErrAgentCheckpointNotSupported so the caller (ares_runtime cognitive
// recovery) can distinguish "no session" from "backend cannot answer" and log
// accordingly. The production path uses ProductionMemoryManager, which queries
// the agent_checkpoints table.
func (m *memoryManager) GetLatestSessionForAgent(_ context.Context, _ string) (string, error) {
	return "", ErrAgentCheckpointNotSupported
}

// SetDefaultTenantID overrides the default tenant ID used for search operations.
// Must match the tenant used during write (StoreDistilledTask) for correct multi-tenant isolation.
func (m *memoryManager) SetDefaultTenantID(tenantID string) {
	if tenantID == "" {
		return
	}
	m.mu.Lock()
	m.defaultTenantID = tenantID
	m.mu.Unlock()
}

// cosineSimilarity calculates cosine similarity between two vectors.
func cosineSimilarity(v1, v2 []float64) float64 {
	if len(v1) != len(v2) {
		return 0.0
	}

	dotProduct := 0.0
	norm1 := 0.0
	norm2 := 0.0

	for i := range v1 {
		dotProduct += v1[i] * v2[i]
		norm1 += v1[i] * v1[i]
		norm2 += v2[i] * v2[i]
	}

	if norm1 == 0 || norm2 == 0 {
		return 0.0
	}

	result := dotProduct / math.Sqrt(norm1*norm2)

	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0.0
	}

	return result
}

// buildCleanedDistillationMessages constructs a cleaned distillation message list.
// It fetches the task's session messages, runs them through the context cleaner,
// and converts to distillation.Message format. Falls back to input/output pair
// when session messages are unavailable.
func (m *memoryManager) buildCleanedDistillationMessages(ctx context.Context, taskID, inputStr, outputStr string) []distillation.Message {
	// Try to get session messages via the task's session.
	taskData, ok := m.taskMemory.Get(ctx, taskID)
	if !ok || taskData.SessionID == "" {
		log.Debug("[Memory Distillation] No session data for task, using raw input/output",
			"task_id", taskID)
		return []distillation.Message{
			{Role: "user", Content: inputStr},
			{Role: "assistant", Content: outputStr},
		}
	}

	rawMessages, err := m.sessionMemory.GetMessages(ctx, taskData.SessionID)
	if err != nil || len(rawMessages) == 0 {
		log.Debug("[Memory Distillation] No session messages for task, using raw input/output",
			"task_id", taskID, "error", err)
		return []distillation.Message{
			{Role: "user", Content: inputStr},
			{Role: "assistant", Content: outputStr},
		}
	}

	// Clean the session messages for meaningful distillation.
	m.mu.RLock()
	distillCleanOpts := llmcore.DefaultCleanOptions()
	if m.config.CleanOptions != nil {
		distillCleanOpts = *m.config.CleanOptions
	}
	m.mu.RUnlock()
	cleaned := m.ctxCleaner.CleanWithTurns(rawMessages, distillCleanOpts)
	log.Debug("[Memory Distillation] Built cleaned distillation messages",
		"task_id", taskID,
		"raw_count", len(rawMessages),
		"cleaned_count", len(cleaned))

	distMsgs := make([]distillation.Message, 0, len(cleaned)+2)
	for _, msg := range cleaned {
		dMsg := distillation.Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
			TurnID:     msg.TurnID,
			EventKind:  msg.EventKind,
			ParentID:   msg.ParentID,
		}
		if len(msg.ArtifactRefs) > 0 {
			dMsg.ArtifactRefs = make([]string, len(msg.ArtifactRefs))
			copy(dMsg.ArtifactRefs, msg.ArtifactRefs)
		}
		// Convert ToolCalls to generic format for the distillation package.
		if len(msg.ToolCalls) > 0 {
			tcs := make([]map[string]interface{}, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				tcs[i] = map[string]interface{}{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				}
			}
			dMsg.ToolCalls = tcs
		}
		distMsgs = append(distMsgs, dMsg)
	}
	// Append the task input/output as additional context for the distiller.
	// Tag them with a task-level TurnID so the distiller can associate evidence
	// without text-based matching.
	taskTurnID := "task_" + taskID
	distMsgs = append(distMsgs,
		distillation.Message{Role: "user", Content: inputStr, TurnID: taskTurnID},
		distillation.Message{Role: "assistant", Content: outputStr, TurnID: taskTurnID},
	)
	return distMsgs
}
