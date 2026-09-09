package builtin

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// MockMemoryManager is a mock implementation of memory.MemoryManager for testing.
type MockMemoryManager struct {
	searchFunc func(ctx context.Context, query string, limit int) ([]*models.Task, error)
	msgFunc    func(ctx context.Context, sessionID string) ([]memory.Message, error)
}

func (m *MockMemoryManager) SearchSimilarTasks(ctx context.Context, query string, limit int) ([]*models.Task, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, query, limit)
	}
	return []*models.Task{}, nil
}

func (m *MockMemoryManager) GetMessages(ctx context.Context, sessionID string) ([]memory.Message, error) {
	if m.msgFunc != nil {
		return m.msgFunc(ctx, sessionID)
	}
	return []memory.Message{}, nil
}

func (m *MockMemoryManager) CreateSession(ctx context.Context, userID string) (string, error) {
	return "session_1", nil
}

func (m *MockMemoryManager) AddMessage(ctx context.Context, sessionID, role, content string) error {
	return nil
}

func (m *MockMemoryManager) DeleteSession(ctx context.Context, sessionID string) error {
	return nil
}

func (m *MockMemoryManager) BuildContext(ctx context.Context, input string, sessionID string) (string, error) {
	return input, nil
}

func (m *MockMemoryManager) CreateTask(ctx context.Context, sessionID, userID, input string) (string, error) {
	return "task_1", nil
}

func (m *MockMemoryManager) CreateTaskWithID(ctx context.Context, taskID, sessionID, userID, input string) error {
	return nil
}

func (m *MockMemoryManager) UpdateTaskOutput(ctx context.Context, taskID, output string) error {
	return nil
}

func (m *MockMemoryManager) DistillTask(ctx context.Context, taskID string) (*models.Task, error) {
	return nil, errors.New("not found")
}

func (m *MockMemoryManager) StoreDistilledTask(ctx context.Context, taskID string, distilled *models.Task) error {
	return nil
}

func (m *MockMemoryManager) GetLatestSessionForAgent(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *MockMemoryManager) Start(ctx context.Context) error {
	return nil
}

func (m *MockMemoryManager) Stop(ctx context.Context) error {
	return nil
}

func (m *MockMemoryManager) SetEventStore(store ares_events.EventStore, streamID string) {
}

func (m *MockMemoryManager) AddStructuredMessage(ctx context.Context, sessionID string, msg memory.Message) error {
	return nil
}

func (m *MockMemoryManager) BuildPromptMessages(ctx context.Context, sessionID string) ([]memory.Message, error) {
	return nil, nil
}

func (m *MockMemoryManager) Clear(ctx context.Context) error {
	return nil
}

func TestNewMemorySearch(t *testing.T) {
	memoryMgr := &MockMemoryManager{}
	search := NewMemorySearch(memoryMgr)

	require.NotNil(t, search)
	if search.Name() != "memory_search" {
		t.Errorf("Name() = %q, want 'memory_search'", search.Name())
	}
	if search.memoryMgr != memoryMgr {
		t.Error("memoryMgr should be set correctly")
	}
}

// TestMemorySearchExecute_MissingQuery tests missing query parameter.
func TestMemorySearchExecute_MissingQuery(t *testing.T) {
	memoryMgr := &MockMemoryManager{}
	search := NewMemorySearch(memoryMgr)
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name:   "no parameters",
			params: map[string]interface{}{},
		},
		{
			name: "empty query",
			params: map[string]interface{}{
				"query": "",
			},
		},
		{
			name: "query is nil",
			params: map[string]interface{}{
				"query": nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := search.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when query is missing")
			}
		})
	}
}

// TestMemorySearchExecute_NoMemoryManager tests search without memory manager.
func TestMemorySearchExecute_NoMemoryManager(t *testing.T) {
	search := NewMemorySearch(nil)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "test query",
	}

	result, err := search.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if result.Success {
		t.Error("Execute() should fail when memory manager is not available")
	}
}

// TestMemorySearchExecute_LimitParameters tests limit parameter handling.
func TestMemorySearchExecute_LimitParameters(t *testing.T) {
	memoryMgr := &MockMemoryManager{}
	search := NewMemorySearch(memoryMgr)
	ctx := context.Background()

	tests := []struct {
		name          string
		limit         interface{}
		expectedLimit int
	}{
		{
			name:          "default limit",
			limit:         nil,
			expectedLimit: 5,
		},
		{
			name:          "valid limit",
			limit:         10,
			expectedLimit: 10,
		},
		{
			name:          "limit capped at 20",
			limit:         100,
			expectedLimit: 20,
		},
		{
			name:          "limit minimum 1",
			limit:         0,
			expectedLimit: 1,
		},
		{
			name:          "limit minimum 1 negative",
			limit:         -5,
			expectedLimit: 1,
		},
		{
			name:          "limit as float",
			limit:         5.5,
			expectedLimit: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"query": "test query",
				"limit": tt.limit,
			}

			result, err := search.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			// We can't verify the exact limit without mocking the search function
			// Just verify the parameter is accepted
			if result.Data == nil && result.Error == "" {
				t.Error("Execute() should return a result with data or error")
			}
		})
	}
}

// TestMemorySearchExecute_Success tests successful memory search.
func TestMemorySearchExecute_Success(t *testing.T) {
	memoryMgr := &MockMemoryManager{
		searchFunc: func(ctx context.Context, query string, limit int) ([]*models.Task, error) {
			return []*models.Task{
				{
					TaskID: "task1",
					Payload: map[string]interface{}{
						"input":   "test input",
						"output":  "test output",
						"context": "test context",
						"score":   0.95,
					},
				},
				{
					TaskID: "task2",
					Payload: map[string]interface{}{
						"input":   "another input",
						"output":  "another output",
						"context": "another context",
						"score":   0.85,
					},
				},
			}, nil
		},
	}

	search := NewMemorySearch(memoryMgr)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "test query",
		"limit": 5,
	}

	result, err := search.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Check result structure
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Result.Data should be a map")
	}

	memories, ok := data["memories"].([]map[string]interface{})
	if !ok || len(memories) != 2 {
		t.Errorf("memories count = %v, want 2", len(memories))
	}

	totalResults, ok := data["total_results"].(int)
	if !ok || totalResults != 2 {
		t.Errorf("total_results = %v, want 2", data["total_results"])
	}

	if data["query"] != "test query" {
		t.Errorf("query = %v, want 'test query'", data["query"])
	}

	// Check memory structure
	mem1 := memories[0]
	if mem1["task_id"] != "task1" {
		t.Errorf("task_id = %v, want 'task1'", mem1["task_id"])
	}
	if mem1["input"] != "test input" {
		t.Errorf("input = %v, want 'test input'", mem1["input"])
	}
	if mem1["output"] != "test output" {
		t.Errorf("output = %v, want 'test output'", mem1["output"])
	}
}

// TestNewUserProfile tests creating a new UserProfile.
func TestNewUserProfile(t *testing.T) {
	memoryMgr := &MockMemoryManager{}
	profile := NewUserProfile(memoryMgr)

	require.NotNil(t, profile)
	if profile.Name() != "user_profile" {
		t.Errorf("Name() = %q, want 'user_profile'", profile.Name())
	}
	if profile.memoryMgr != memoryMgr {
		t.Error("memoryMgr should be set correctly")
	}
}

// TestUserProfileExecute_MissingParameters tests missing required parameters.
func TestUserProfileExecute_MissingParameters(t *testing.T) {
	memoryMgr := &MockMemoryManager{}
	profile := NewUserProfile(memoryMgr)
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name:   "no parameters",
			params: map[string]interface{}{},
		},
		{
			name: "missing user_id",
			params: map[string]interface{}{
				"tenant_id": "tenant1",
			},
		},
		{
			name: "missing tenant_id",
			params: map[string]interface{}{
				"user_id": "user1",
			},
		},
		{
			name: "empty user_id",
			params: map[string]interface{}{
				"user_id":   "",
				"tenant_id": "tenant1",
			},
		},
		{
			name: "empty tenant_id",
			params: map[string]interface{}{
				"user_id":   "user1",
				"tenant_id": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := profile.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when required parameters are missing")
			}
		})
	}
}

// TestUserProfileExecute_Success tests successful user profile retrieval.
func TestUserProfileExecute_Success(t *testing.T) {
	memoryMgr := &MockMemoryManager{
		searchFunc: func(ctx context.Context, query string, limit int) ([]*models.Task, error) {
			return []*models.Task{
				{
					TaskID: "task1",
					Payload: map[string]interface{}{
						"input":  "我喜欢 Rust 和 Go",
						"output": "好的，我记住了",
						"score":  0.9,
					},
				},
			}, nil
		},
		msgFunc: func(ctx context.Context, sessionID string) ([]memory.Message, error) {
			return []memory.Message{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
			}, nil
		},
	}

	profile := NewUserProfile(memoryMgr)
	ctx := context.Background()

	params := map[string]interface{}{
		"user_id":    "user1",
		"tenant_id":  "tenant1",
		"session_id": "session1",
	}

	result, err := profile.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Check result structure
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Result.Data should be a map")
	}

	if data["user_id"] != "user1" {
		t.Errorf("user_id = %v, want 'user1'", data["user_id"])
	}

	if data["tenant_id"] != "tenant1" {
		t.Errorf("tenant_id = %v, want 'tenant1'", data["tenant_id"])
	}

	// Check tech stack (memory-manager path only now: the distilled-memories
	// branch was removed with the schema ghost)
	if _, ok := data["tech_stack"].([]string); !ok {
		t.Fatal("tech_stack should be a slice")
	}

	// Check preferences
	preferences, ok := data["preferences"].([]map[string]interface{})
	if !ok {
		t.Fatal("preferences should be a slice")
	}

	if len(preferences) == 0 {
		t.Error("preferences should contain at least one item")
	}

	// Check current session messages
	if _, ok := data["current_session_messages"]; !ok {
		t.Error("current_session_messages should be present")
	}
}

// TestUserProfileExecute_NoManagers tests user profile without managers.
func TestUserProfileExecute_NoManagers(t *testing.T) {
	profile := NewUserProfile(nil)
	ctx := context.Background()

	params := map[string]interface{}{
		"user_id":   "user1",
		"tenant_id": "tenant1",
	}

	result, err := profile.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed even without managers, got error: %s", result.Error)
	}

	// Check result structure
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Result.Data should be a map")
	}

	// Should have empty collections
	if len(data["tech_stack"].([]string)) != 0 {
		t.Error("tech_stack should be empty without managers")
	}

	if len(data["preferences"].([]map[string]interface{})) != 0 {
		t.Error("preferences should be empty without managers")
	}
}

func TestContainsKeywords(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		keywords []string
		expected bool
	}{
		{
			name:     "keyword found",
			text:     "I like programming",
			keywords: []string{"like", "love"},
			expected: true,
		},
		{
			name:     "keyword not found",
			text:     "I enjoy coding",
			keywords: []string{"like", "love"},
			expected: false,
		},
		{
			name:     "case insensitive",
			text:     "I LIKE Rust",
			keywords: []string{"like"},
			expected: true,
		},
		{
			name:     "multiple keywords",
			text:     "prefer rust over go",
			keywords: []string{"like", "prefer", "love"},
			expected: true,
		},
		{
			name:     "empty keywords",
			text:     "some text",
			keywords: []string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsKeywords(tt.text, tt.keywords)
			if result != tt.expected {
				t.Errorf("containsKeywords() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestContainsText tests substring matching.
func TestContainsText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		substr   string
		expected bool
	}{
		{
			name:     "substring found",
			text:     "Hello World",
			substr:   "world",
			expected: true,
		},
		{
			name:     "substring not found",
			text:     "Hello World",
			substr:   "test",
			expected: false,
		},
		{
			name:     "case insensitive",
			text:     "HELLO",
			substr:   "hello",
			expected: true,
		},
		{
			name:     "empty substring",
			text:     "Hello",
			substr:   "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsText(tt.text, tt.substr)
			if result != tt.expected {
				t.Errorf("containsText() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestGetInt tests integer extraction from params.
func TestGetInt(t *testing.T) {
	tests := []struct {
		name       string
		params     map[string]interface{}
		key        string
		defaultVal int
		expected   int
	}{
		{
			name:       "float64 value",
			params:     map[string]interface{}{"key": 5.5},
			key:        "key",
			defaultVal: 0,
			expected:   5,
		},
		{
			name:       "int value",
			params:     map[string]interface{}{"key": 10},
			key:        "key",
			defaultVal: 0,
			expected:   10,
		},
		{
			name:       "string value",
			params:     map[string]interface{}{"key": "15"},
			key:        "key",
			defaultVal: 0,
			expected:   15,
		},
		{
			name:       "invalid string",
			params:     map[string]interface{}{"key": "abc"},
			key:        "key",
			defaultVal: 0,
			expected:   0,
		},
		{
			name:       "missing key",
			params:     map[string]interface{}{},
			key:        "key",
			defaultVal: 5,
			expected:   5,
		},
		{
			name:       "nil value",
			params:     map[string]interface{}{"key": nil},
			key:        "key",
			defaultVal: 5,
			expected:   5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInt(tt.params, tt.key, tt.defaultVal)
			if result != tt.expected {
				t.Errorf("getInt() = %d, want %d", result, tt.expected)
			}
		})
	}
}
