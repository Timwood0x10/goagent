// Package memory provides unified memory management for the StyleAgent framework.
// It coordinates session memory, task memory, and distilled task storage through a single interface.
package memory

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// MemoryManager provides unified memory management.
// It coordinates session memory, task memory, and distilled task storage.
type MemoryManager interface {
	// CreateSession creates a new session and returns the session ID.
	CreateSession(ctx context.Context, userID string) (string, error)

	// AddMessage adds a message to the session.
	AddMessage(ctx context.Context, sessionID, role, content string) error

	// GetMessages retrieves all messages from the session.
	GetMessages(ctx context.Context, sessionID string) ([]Message, error)

	// AddStructuredMessage adds a structured message with metadata (TurnID, ToolCallID, ToolCalls)
	// to the session. This preserves the full message structure for turn-aware cleaning.
	AddStructuredMessage(ctx context.Context, sessionID string, msg Message) error

	// BuildPromptMessages returns all messages as a structured slice suitable for LLM prompt
	// construction. Unlike BuildContext, this returns typed Message structs instead of a flat string.
	BuildPromptMessages(ctx context.Context, sessionID string) ([]Message, error)

	// DeleteSession deletes a session and all its messages immediately.
	// This is different from TTL-based cleanup, which waits for expiration.
	DeleteSession(ctx context.Context, sessionID string) error

	// BuildContext builds input with conversation history context.
	BuildContext(ctx context.Context, input string, sessionID string) (string, error)

	// CreateTask creates a new task and returns the task ID.
	CreateTask(ctx context.Context, sessionID, userID, input string) (string, error)

	// CreateTaskWithID creates a task with a caller-assigned ID. This lets
	// upper layers track a task by its own ID instead of a generated one,
	// keeping cached results and the returned task ID consistent.
	CreateTaskWithID(ctx context.Context, taskID, sessionID, userID, input string) error

	// UpdateTaskOutput updates the task output.
	UpdateTaskOutput(ctx context.Context, taskID, output string) error

	// DistillTask extracts key information from task for future reference.
	DistillTask(ctx context.Context, taskID string) (*models.Task, error)

	// StoreDistilledTask stores a distilled task with local vector embedding.
	// The vector is generated locally using simple hash-based algorithms.
	StoreDistilledTask(ctx context.Context, taskID string, distilled *models.Task) error

	// SearchSimilarTasks searches for similar tasks using local cosine similarity.
	SearchSimilarTasks(ctx context.Context, query string, limit int) ([]*models.Task, error)

	// GetLatestSessionForAgent retrieves the most recent session ID for an
	// agent from checkpoint. Returns ("", nil) if no checkpoint exists for the
	// agent. Implementations that do not persist agent checkpoints return
	// ErrAgentCheckpointNotSupported so callers can distinguish "no session"
	// (empty string, nil error) from "unsupported backend" (non-nil error).
	GetLatestSessionForAgent(ctx context.Context, agentID string) (string, error)

	// Start starts the memory manager and background workers.
	Start(ctx context.Context) error

	// Stop stops the memory manager and cleans up resources.
	Stop(ctx context.Context) error

	// SetEventStore configures an optional EventStore for emitting lifecycle ares_events.
	// If store is nil, event emission is a no-op.
	SetEventStore(store ares_events.EventStore, streamID string)
}

// MemoryConfig holds configuration for MemoryManager.
type MemoryConfig struct {
	// Enabled enables memory features.
	Enabled bool

	// Storage type: "memory" or "postgres".
	Storage string

	// MaxHistory is the maximum number of turns to keep in context.
	MaxHistory int

	// MaxSessions is the maximum number of sessions to store.
	MaxSessions int

	// MaxTasks is the maximum number of tasks to store.
	MaxTasks int

	// MaxDistilledTasks is the maximum number of distilled tasks to store.
	// Implements LRU eviction when limit is reached.
	MaxDistilledTasks int

	// SessionTTL is the time-to-live for sessions.
	SessionTTL time.Duration

	// TaskTTL is time-to-live for tasks.
	TaskTTL time.Duration

	// DistilledTaskTTL is time-to-live for distilled tasks.
	DistilledTaskTTL time.Duration

	// VectorDim is the dimension of the vector (for local embedding).
	VectorDim int

	// EnablePostgres enables PostgreSQL storage.
	EnablePostgres bool

	// PostgresDSN is the PostgreSQL connection string.
	PostgresDSN string

	// UseStructuredCleaning enables the new structured prompt builder (BuildPromptMessages)
	// instead of the legacy text-based BuildContext. When true, callers should use
	// BuildPromptMessages for LLM input construction. Default: false (legacy mode).
	UseStructuredCleaning bool

	// CleanOptions configures context cleaning behavior. When nil, defaults are used.
	CleanOptions *llmcore.CleanOptions

	// EnableRAG enables retrieval-augmented generation: past experiences and
	// distilled memories are retrieved and injected into the LLM prompt.
	// When false, BuildContext/BuildPromptMessages behave as before (history only).
	EnableRAG bool

	// RAGTopK is the maximum number of retrieved snippets to inject.
	// Defaults to 5 when zero (applied lazily at retrieval time when EnableRAG is true).
	RAGTopK int

	// RAGMinScore is the minimum similarity score for a retrieved snippet to
	// be included. Snippets below this threshold are filtered out.
	// Defaults to 0.4 when zero (applied lazily at retrieval time when EnableRAG is true).
	RAGMinScore float64
}

// Message, ToolCall, ToolCallFunction are type aliases for the canonical
// types defined in the memctx (internal/memory/context) package.
type (
	Message          = memctx.Message
	ToolCall         = memctx.ToolCall
	ToolCallFunction = memctx.ToolCallFunction
)

// Storage type constants.
const (
	StorageMemory   = "memory"
	StoragePostgres = "postgres"
)

// ErrInvalidRAGConfig is returned when MemoryConfig.EnableRAG is true but
// RAGTopK or RAGMinScore are set to invalid values.
var ErrInvalidRAGConfig = errors.New("invalid RAG configuration")

// ErrAgentCheckpointNotSupported is returned by GetLatestSessionForAgent when
// the memory backend does not persist agent checkpoints (e.g. the in-memory
// memoryManager). Callers can use errors.Is to distinguish "no session for this
// agent" (("", nil)) from "backend cannot answer this question" (this error).
var ErrAgentCheckpointNotSupported = errors.New(
	"agent checkpoint lookup not supported by this memory backend")

// Role constants re-exported for convenience.
const (
	RoleUser       = memctx.RoleUser
	RoleAssistant  = memctx.RoleAssistant
	RoleSystem     = memctx.RoleSystem
	RoleToolCall   = memctx.RoleToolCall
	RoleToolResult = memctx.RoleToolResult
)

// ToCoreMessage converts a memory Message to an internal/llmcore Message.
func ToCoreMessage(sessionID string, msg Message) *llmcore.Message {
	return &llmcore.Message{
		SessionID: sessionID,
		Role:      llmcore.MessageRole(msg.Role),
		Content:   msg.Content,
		Time:      msg.Time,
		Metadata: llmcore.Metadata{
			"turn_id":      msg.TurnID,
			"tool_call_id": msg.ToolCallID,
			"tool_calls":   msg.ToolCalls,
		},
	}
}

// ToLLMMessage converts a memory Message to an internal/llmcore LLMMessage.
func ToLLMMessage(msg Message) *llmcore.LLMMessage {
	tcs := make([]llmcore.ToolCall, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		tcs[i] = llmcore.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: llmcore.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return &llmcore.LLMMessage{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCalls:  tcs,
		ToolCallID: msg.ToolCallID,
	}
}

// FromCoreMessage converts an internal/llmcore Message to a memory Message.
func FromCoreMessage(sessionID string, msg *llmcore.Message) Message {
	if msg == nil {
		return Message{}
	}
	m := Message{
		Role:    string(msg.Role),
		Content: msg.Content,
		Time:    msg.Time,
	}
	if msg.Metadata != nil {
		if tid, ok := msg.Metadata["turn_id"].(string); ok {
			m.TurnID = tid
		}
		if tcid, ok := msg.Metadata["tool_call_id"].(string); ok {
			m.ToolCallID = tcid
		}
		if tcs, ok := msg.Metadata["tool_calls"]; ok {
			switch v := tcs.(type) {
			case []ToolCall:
				m.ToolCalls = v
			case []interface{}:
				m.ToolCalls = convertRawToToolCalls(v)
			}
		}
	}
	_ = sessionID
	return m
}

// FromLLMMessage converts an internal/llmcore LLMMessage to a memory Message.
func FromLLMMessage(msg *llmcore.LLMMessage) Message {
	if msg == nil {
		return Message{}
	}
	tcs := make([]ToolCall, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		tcs[i] = ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		}
	}
	return Message{
		Role:       msg.Role,
		Content:    msg.Content,
		ToolCalls:  tcs,
		ToolCallID: msg.ToolCallID,
	}
}

// convertRawToToolCalls converts a raw []interface{} from JSON metadata into typed ToolCalls.
func convertRawToToolCalls(raw []interface{}) []ToolCall {
	calls := make([]ToolCall, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		var tc ToolCall
		if id, ok := m["id"].(string); ok {
			tc.ID = id
		}
		if typ, ok := m["type"].(string); ok {
			tc.Type = typ
		}
		if fn, ok := m["function"].(map[string]interface{}); ok {
			tcf := ToolCallFunction{}
			if name, ok := fn["name"].(string); ok {
				tcf.Name = name
			}
			if args, ok := fn["arguments"].(string); ok {
				tcf.Arguments = args
			}
			tc.Function = tcf
		}
		calls = append(calls, tc)
	}
	return calls
}

// validate checks that config fields have sensible values.
// Returns a descriptive error if any field is invalid.
func (c *MemoryConfig) validate() error {
	if c.MaxSessions <= 0 {
		return fmt.Errorf("MaxSessions must be positive, got %d", c.MaxSessions)
	}
	if c.SessionTTL <= 0 {
		return fmt.Errorf("SessionTTL must be positive, got %v", c.SessionTTL)
	}
	if c.MaxTasks <= 0 {
		return fmt.Errorf("MaxTasks must be positive, got %d", c.MaxTasks)
	}
	if c.TaskTTL <= 0 {
		return fmt.Errorf("TaskTTL must be positive, got %v", c.TaskTTL)
	}
	if c.MaxDistilledTasks <= 0 {
		return fmt.Errorf("MaxDistilledTasks must be positive, got %d", c.MaxDistilledTasks)
	}
	if c.DistilledTaskTTL <= 0 {
		return fmt.Errorf("DistilledTaskTTL must be positive, got %v", c.DistilledTaskTTL)
	}
	if c.MaxHistory <= 0 {
		return fmt.Errorf("MaxHistory must be positive, got %d", c.MaxHistory)
	}
	if c.VectorDim <= 0 {
		return fmt.Errorf("VectorDim must be positive, got %d", c.VectorDim)
	}
	// RAG validation: only enforce constraints when RAG is opt-in. When RAG
	// is disabled, RAGTopK/RAGMinScore may stay zero — defaults are applied
	// lazily at retrieval time, preserving legacy behavior for existing callers.
	if c.EnableRAG {
		if c.RAGTopK <= 0 {
			return fmt.Errorf("RAGTopK must be positive when EnableRAG is true, got %d: %w",
				c.RAGTopK, ErrInvalidRAGConfig)
		}
		if c.RAGMinScore < 0 {
			return fmt.Errorf("RAGMinScore must be non-negative when EnableRAG is true, got %f: %w",
				c.RAGMinScore, ErrInvalidRAGConfig)
		}
	}
	return nil
}

// DefaultMemoryConfig returns default configuration for MemoryManager.
func DefaultMemoryConfig() *MemoryConfig {
	opts := llmcore.DefaultCleanOptions()
	return &MemoryConfig{
		Enabled:               true,
		Storage:               StorageMemory,
		MaxHistory:            10,
		MaxSessions:           100,
		MaxTasks:              1000,
		MaxDistilledTasks:     5000,
		SessionTTL:            24 * time.Hour,
		TaskTTL:               7 * 24 * time.Hour,
		DistilledTaskTTL:      30 * 24 * time.Hour,
		VectorDim:             128,
		EnablePostgres:        false,
		UseStructuredCleaning: false,
		CleanOptions:          &opts,
		// RAG is opt-in: disabled by default. TopK/MinScore are still seeded
		// with sensible defaults so callers that flip EnableRAG to true without
		// further configuration still get usable retrieval behavior.
		EnableRAG:   false,
		RAGTopK:     5,
		RAGMinScore: 0.4,
	}
}
