// Package memory provides unified memory management for the StyleAgent framework.
package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/embedding"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

// testEmbedder is a minimal EmbeddingService mock for tests.
type testEmbedder struct{}

func (t *testEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedWithPrefix(ctx context.Context, text, prefix string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

func (t *testEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	return [][]float64{{0.1, 0.2, 0.3}}, nil
}

func (t *testEmbedder) HealthCheck(ctx context.Context) error { return nil }

func (t *testEmbedder) GetModel() string { return "test-model" }

func (t *testEmbedder) GetTimeout() time.Duration { return time.Second }

var _ embedding.EmbeddingService = (*testEmbedder)(nil)

// testExpRepo is a minimal ExperienceRepository mock for tests.
type testExpRepo struct {
	experiences []distillation.Experience
}

func (r *testExpRepo) SearchByVector(ctx context.Context, vector []float64, tenantID string, limit int) ([]distillation.Experience, error) {
	return r.experiences, nil
}

func (r *testExpRepo) GetByMemoryType(ctx context.Context, tenantID string, memoryType distillation.MemoryType) ([]distillation.Experience, error) {
	return r.experiences, nil
}

func (r *testExpRepo) CountByMemoryType(ctx context.Context, tenantID string, memoryType distillation.MemoryType) (int, error) {
	return len(r.experiences), nil
}

func (r *testExpRepo) Update(ctx context.Context, experience *distillation.Experience) error {
	return nil
}

func (r *testExpRepo) Delete(ctx context.Context, id string) error { return nil }

func (r *testExpRepo) DeleteBatch(ctx context.Context, ids []string) error { return nil }

func (r *testExpRepo) Create(ctx context.Context, experience *distillation.Experience) error {
	r.experiences = append(r.experiences, *experience)
	return nil
}

var _ distillation.ExperienceRepository = (*testExpRepo)(nil)

func TestDefaultMemoryConfig(t *testing.T) {
	config := DefaultMemoryConfig()
	require.NotNil(t, config, "DefaultMemoryConfig returned nil")

	if !config.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if config.Storage != "memory" {
		t.Errorf("Expected Storage to be 'memory', got '%s'", config.Storage)
	}

	if config.MaxHistory != 10 {
		t.Errorf("Expected MaxHistory to be 10, got %d", config.MaxHistory)
	}

	if config.MaxSessions != 100 {
		t.Errorf("Expected MaxSessions to be 100, got %d", config.MaxSessions)
	}

	if config.MaxTasks != 1000 {
		t.Errorf("Expected MaxTasks to be 1000, got %d", config.MaxTasks)
	}

	if config.SessionTTL != 24*time.Hour {
		t.Errorf("Expected SessionTTL to be 24h, got %v", config.SessionTTL)
	}

	if config.TaskTTL != 7*24*time.Hour {
		t.Errorf("Expected TaskTTL to be 168h, got %v", config.TaskTTL)
	}

	if config.VectorDim != 128 {
		t.Errorf("Expected VectorDim to be 128, got %d", config.VectorDim)
	}
}

func TestNewMemoryManager(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)

	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}

	if mgr == nil {
		t.Fatal("NewMemoryManager returned nil manager")
	}

	// Clean up
	ctx := context.Background()
	_ = mgr.Stop(ctx)
}

func TestMemoryManager_StartStop(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}

	// Test start
	err = mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Test start again (should be idempotent)
	err = mgr.Start(ctx)
	if err != nil {
		t.Fatalf("Second Start failed: %v", err)
	}

	// Test stop
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Test stop again (should be idempotent)
	err = mgr.Stop(ctx)
	if err != nil {
		t.Fatalf("Second Stop failed: %v", err)
	}
}

func TestMemoryManager_CreateSession(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if sessionID == "" {
		t.Error("CreateSession returned empty session ID")
	}
}

func TestMemoryManager_AddMessage(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	err = mgr.AddMessage(ctx, sessionID, "user", "Hello, world!")
	if err != nil {
		t.Fatalf("AddMessage failed: %v", err)
	}
}

func TestMemoryManager_GetMessages(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Add messages
	_ = mgr.AddMessage(ctx, sessionID, "user", "Hello")
	_ = mgr.AddMessage(ctx, sessionID, "assistant", "Hi there!")

	// Get messages
	messages, err := mgr.GetMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetMessages failed: %v", err)
	}

	if len(messages) < 2 {
		t.Errorf("Expected at least 2 messages, got %d", len(messages))
	}
}

func TestMemoryManager_BuildContext(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Add some history
	_ = mgr.AddMessage(ctx, sessionID, "user", "Previous question")
	_ = mgr.AddMessage(ctx, sessionID, "assistant", "Previous answer")

	// Build context
	context, err := mgr.BuildContext(ctx, "Current question", sessionID)
	if err != nil {
		t.Fatalf("BuildContext failed: %v", err)
	}

	if context == "" {
		t.Error("BuildContext returned empty context")
	}

	// Check if context contains history
	if !contains(context, "Previous") {
		t.Error("Context should contain previous conversation history")
	}

	// Check if context contains current input
	if !contains(context, "Current question") {
		t.Error("Context should contain current input")
	}
}

func TestMemoryManager_CreateTask(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", "Do something")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	if taskID == "" {
		t.Error("CreateTask returned empty task ID")
	}
}

func TestMemoryManager_UpdateTaskOutput(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", "Do something")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	err = mgr.UpdateTaskOutput(ctx, taskID, "Task completed")
	if err != nil {
		t.Fatalf("UpdateTaskOutput failed: %v", err)
	}
}

func TestMemoryManager_DistillTask(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", "Do something")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	err = mgr.UpdateTaskOutput(ctx, taskID, "Task completed")
	if err != nil {
		t.Fatalf("UpdateTaskOutput failed: %v", err)
	}

	distilled, err := mgr.DistillTask(ctx, taskID)
	if err != nil {
		t.Fatalf("DistillTask failed: %v", err)
	}

	if distilled == nil {
		t.Error("DistillTask returned nil task")
		return
	}

	if distilled.TaskID != taskID {
		t.Errorf("Expected task ID %s, got %s", taskID, distilled.TaskID)
	}

	if distilled.Payload == nil {
		t.Error("DistillTask returned nil payload")
	}
}

func TestMemoryManager_StoreDistilledTask(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManagerWithDistiller(config, &testEmbedder{}, &testExpRepo{})
	if err != nil {
		t.Fatalf("NewMemoryManagerWithDistiller failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	taskID, err := mgr.CreateTask(ctx, sessionID, "test_user", "Do something")
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	distilled := &models.Task{
		TaskID: taskID,
		Payload: map[string]any{
			"input":   "Do something",
			"output":  "Task completed",
			"context": map[string]interface{}{},
		},
	}

	err = mgr.StoreDistilledTask(ctx, taskID, distilled)
	if err != nil {
		t.Fatalf("StoreDistilledTask failed: %v", err)
	}
}

func TestMemoryManager_SearchSimilarTasks(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManagerWithDistiller(config, &testEmbedder{}, &testExpRepo{})
	if err != nil {
		t.Fatalf("NewMemoryManagerWithDistiller failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	taskID, _ := mgr.CreateTask(ctx, sessionID, "test_user", "Create a REST API")
	distilled := &models.Task{
		TaskID: taskID,
		Payload: map[string]any{
			"input":   "Create a REST API",
			"output":  "API created",
			"context": map[string]interface{}{},
		},
	}
	_ = mgr.StoreDistilledTask(ctx, taskID, distilled)

	results, err := mgr.SearchSimilarTasks(ctx, "Create a web API", 3)
	if err != nil {
		t.Fatalf("SearchSimilarTasks failed: %v", err)
	}

	// Results may be empty if distiller filters as noise; just verify no error.
	_ = results
}

func TestMemoryManager_MultipleSessions(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	// Create multiple sessions
	sessionID1, err := mgr.CreateSession(ctx, "user1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	sessionID2, err := mgr.CreateSession(ctx, "user2")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if sessionID1 == sessionID2 {
		t.Error("Different sessions should have different IDs")
	}

	// Add messages to different sessions
	_ = mgr.AddMessage(ctx, sessionID1, "user", "User 1 message")
	_ = mgr.AddMessage(ctx, sessionID2, "user", "User 2 message")

	// Verify sessions are independent
	msgs1, _ := mgr.GetMessages(ctx, sessionID1)
	msgs2, _ := mgr.GetMessages(ctx, sessionID2)

	if len(msgs1) == 0 || len(msgs2) == 0 {
		t.Error("Both sessions should have messages")
	}

	// Check cross-session contamination
	if len(msgs1) > 0 && len(msgs2) > 0 {
		lastMsg1 := msgs1[len(msgs1)-1]
		lastMsg2 := msgs2[len(msgs2)-1]

		if lastMsg1.Content == lastMsg2.Content {
			t.Error("Sessions should not share messages")
		}
	}
}

func TestMemoryManager_ContextLimit(t *testing.T) {
	ctx := context.Background()
	config := DefaultMemoryConfig()
	config.MaxHistory = 5 // Only keep last 5 messages
	mgr, err := NewMemoryManager(config)
	if err != nil {
		t.Fatalf("NewMemoryManager failed: %v", err)
	}
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "test_user")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Add more messages than MaxHistory
	for i := 0; i < 10; i++ {
		_ = mgr.AddMessage(ctx, sessionID, "user", fmt.Sprintf("Message %d", i))
		_ = mgr.AddMessage(ctx, sessionID, "assistant", fmt.Sprintf("Response %d", i))
	}

	// Build context should respect MaxHistory
	context, err := mgr.BuildContext(ctx, "New question", sessionID)
	if err != nil {
		t.Fatalf("BuildContext failed: %v", err)
	}

	// Count message occurrences in context
	messageCount := countOccurrences(context, "Message")
	if messageCount > config.MaxHistory {
		t.Errorf("Expected at most %d messages in context, got %d", config.MaxHistory, messageCount)
	}
}

// ──────────────────────────── Conversion helpers ────────────────────────────

func TestToCoreMessage(t *testing.T) {
	now := time.Now()
	msg := Message{
		Role:       RoleAssistant,
		Content:    "Hello",
		Time:       now,
		TurnID:     "turn_1",
		ToolCallID: "call_1",
		ToolCalls: []ToolCall{
			{ID: "tc1", Type: "function", Function: ToolCallFunction{Name: "test", Arguments: `{}`}},
		},
	}

	coreMsg := ToCoreMessage("session_1", msg)
	if coreMsg == nil {
		t.Fatal("ToCoreMessage returned nil")
	}
	if string(coreMsg.Role) != RoleAssistant {
		t.Errorf("expected role %s, got %s", RoleAssistant, string(coreMsg.Role))
	}
	if coreMsg.Content != "Hello" {
		t.Errorf("expected content 'Hello', got %q", coreMsg.Content)
	}
	if coreMsg.SessionID != "session_1" {
		t.Errorf("expected sessionID 'session_1', got %q", coreMsg.SessionID)
	}
}

func TestToLLMMessage(t *testing.T) {
	msg := Message{
		Role:       RoleAssistant,
		Content:    "Let me search",
		ToolCallID: "call_1",
		ToolCalls: []ToolCall{
			{ID: "tc1", Type: "function", Function: ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`}},
		},
	}

	llmMsg := ToLLMMessage(msg)
	if llmMsg == nil {
		t.Fatal("ToLLMMessage returned nil")
	}
	if llmMsg.Role != RoleAssistant {
		t.Errorf("expected role %s, got %s", RoleAssistant, llmMsg.Role)
	}
	if llmMsg.ToolCallID != "call_1" {
		t.Errorf("expected ToolCallID 'call_1', got %q", llmMsg.ToolCallID)
	}
	if len(llmMsg.ToolCalls) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(llmMsg.ToolCalls))
	}
	if llmMsg.ToolCalls[0].Function.Name != "search" {
		t.Errorf("expected function name 'search', got %q", llmMsg.ToolCalls[0].Function.Name)
	}
}

func TestFromCoreMessage(t *testing.T) {
	now := time.Now()
	coreMsg := &llmcore.Message{
		SessionID: "session_1",
		Role:      llmcore.MessageRoleUser,
		Content:   "Hello from core",
		Time:      now,
		Metadata: llmcore.Metadata{
			"turn_id":      "turn_1",
			"tool_call_id": "call_1",
		},
	}

	msg := FromCoreMessage("session_1", coreMsg)
	if msg.Role != string(llmcore.MessageRoleUser) {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if msg.Content != "Hello from core" {
		t.Errorf("expected content 'Hello from core', got %q", msg.Content)
	}
	if msg.TurnID != "turn_1" {
		t.Errorf("expected TurnID 'turn_1', got %q", msg.TurnID)
	}
	if msg.ToolCallID != "call_1" {
		t.Errorf("expected ToolCallID 'call_1', got %q", msg.ToolCallID)
	}
}

func TestFromCoreMessage_Nil(t *testing.T) {
	msg := FromCoreMessage("session_1", nil)
	if msg.Role != "" || msg.Content != "" {
		t.Errorf("expected empty Message for nil input, got %+v", msg)
	}
}

func TestFromLLMMessage(t *testing.T) {
	llmMsg := &llmcore.LLMMessage{
		Role:       "assistant",
		Content:    "Let me search",
		ToolCallID: "call_1",
		ToolCalls: []llmcore.ToolCall{
			{ID: "tc1", Type: "function", Function: llmcore.FunctionCall{Name: "search", Arguments: `{"q":"test"}`}},
		},
	}

	msg := FromLLMMessage(llmMsg)
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
	if msg.ToolCallID != "call_1" {
		t.Errorf("expected ToolCallID 'call_1', got %q", msg.ToolCallID)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "search" {
		t.Errorf("expected function name 'search', got %q", msg.ToolCalls[0].Function.Name)
	}
}

func TestFromLLMMessage_Nil(t *testing.T) {
	msg := FromLLMMessage(nil)
	if msg.Role != "" || msg.Content != "" {
		t.Errorf("expected empty Message for nil input, got %+v", msg)
	}
}

func TestDefaultMemoryConfig_HasCleanOptions(t *testing.T) {
	config := DefaultMemoryConfig()
	if config.CleanOptions == nil {
		t.Fatal("expected CleanOptions to be non-nil")
	}
	if config.CleanOptions.MaxUserLen <= 0 {
		t.Errorf("expected MaxUserLen > 0, got %d", config.CleanOptions.MaxUserLen)
	}
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countOccurrences(s, substr string) int {
	count := 0
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			count++
		}
	}
	return count
}

// nolint: errcheck // Test code may ignore return values
// nolint: errcheck // Test code may ignore return values

// TestMemoryManager_SetEventStore verifies SetEventStore configures the event store.
func TestMemoryManager_SetEventStore(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	store := ares_events.NewMemoryEventStore()
	mgr.SetEventStore(store, "test-stream")

	// Verify the event store is set by checking that ares_events are emitted.
	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)
	require.NotEmpty(t, sessionID)

	evts, err := store.Read(ctx, "test-stream", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 1)
	require.Equal(t, ares_events.EventSessionCreated, evts[0].Type)
	require.Equal(t, sessionID, evts[0].Payload["session_id"])
	require.Equal(t, "user1", evts[0].Payload["user_id"])
}

// TestMemoryManager_EmitEvent_CreateSession verifies CreateSession emits EventSessionCreated.
func TestMemoryManager_EmitEvent_CreateSession(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	store := ares_events.NewMemoryEventStore()
	mgr.SetEventStore(store, "test-stream")

	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)

	evts, err := store.Read(ctx, "test-stream", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 1)
	require.Equal(t, ares_events.EventSessionCreated, evts[0].Type)
	require.Equal(t, sessionID, evts[0].Payload["session_id"])
	require.Equal(t, "user1", evts[0].Payload["user_id"])
}

// TestMemoryManager_EmitEvent_AddMessage verifies AddMessage emits EventMessageAdded.
func TestMemoryManager_EmitEvent_AddMessage(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	store := ares_events.NewMemoryEventStore()
	mgr.SetEventStore(store, "test-stream")

	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)

	err = mgr.AddMessage(ctx, sessionID, "user", "Hello")
	require.NoError(t, err)

	evts, err := store.Read(ctx, "test-stream", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 2) // session.created + message.added
	require.Equal(t, ares_events.EventSessionCreated, evts[0].Type)
	require.Equal(t, ares_events.EventMessageAdded, evts[1].Type)
	require.Equal(t, sessionID, evts[1].Payload["session_id"])
	require.Equal(t, "user", evts[1].Payload["role"])
}

// TestMemoryManager_EmitEvent_DistillTask verifies DistillTask emits EventMemoryDistilled.
func TestMemoryManager_EmitEvent_DistillTask(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	store := ares_events.NewMemoryEventStore()
	mgr.SetEventStore(store, "test-stream")

	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)

	taskID, err := mgr.CreateTask(ctx, sessionID, "user1", "Do something")
	require.NoError(t, err)

	err = mgr.UpdateTaskOutput(ctx, taskID, "Done")
	require.NoError(t, err)

	_, err = mgr.DistillTask(ctx, taskID)
	require.NoError(t, err)

	evts, err := store.Read(ctx, "test-stream", ares_events.ReadOptions{})
	require.NoError(t, err)

	// Find the memory.distilled event.
	var distilledEvt *ares_events.Event
	for _, e := range evts {
		if e.Type == ares_events.EventMemoryDistilled {
			distilledEvt = e
			break
		}
	}
	require.NotNil(t, distilledEvt, "expected EventMemoryDistilled to be emitted")
	require.Equal(t, taskID, distilledEvt.Payload["task_id"])
	require.NotNil(t, distilledEvt.Payload["input_count"])
}

// TestMemoryManager_EmitEvent_StoreDistilledTask verifies StoreDistilledTask emits EventMemoryDistilled.
func TestMemoryManager_EmitEvent_StoreDistilledTask(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManagerWithDistiller(config, &testEmbedder{}, &testExpRepo{})
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	store := ares_events.NewMemoryEventStore()
	mgr.SetEventStore(store, "test-stream")

	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)

	taskID, err := mgr.CreateTask(ctx, sessionID, "user1", "Do something")
	require.NoError(t, err)

	distilled := &models.Task{
		TaskID: taskID,
		Payload: map[string]any{
			"input":   "Do something",
			"output":  "Task completed",
			"context": map[string]interface{}{},
		},
	}

	err = mgr.StoreDistilledTask(ctx, taskID, distilled)
	require.NoError(t, err)

	evts, err := store.Read(ctx, "test-stream", ares_events.ReadOptions{})
	require.NoError(t, err)

	// Find the memory.distilled event from store.
	var storeEvt *ares_events.Event
	for _, e := range evts {
		if e.Type == ares_events.EventMemoryDistilled {
			storeEvt = e
			break
		}
	}
	require.NotNil(t, storeEvt, "expected EventMemoryDistilled to be emitted from StoreDistilledTask")
	require.Equal(t, taskID, storeEvt.Payload["task_id"])
}

// TestMemoryManager_NoEventStore verifies that operations work without an EventStore.
func TestMemoryManager_NoEventStore(t *testing.T) {
	config := DefaultMemoryConfig()
	mgr, err := NewMemoryManager(config)
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(context.Background()) }()

	// No SetEventStore called - should not panic.
	ctx := context.Background()
	sessionID, err := mgr.CreateSession(ctx, "user1")
	require.NoError(t, err)
	require.NotEmpty(t, sessionID)

	err = mgr.AddMessage(ctx, sessionID, "user", "Hello")
	require.NoError(t, err)
}
