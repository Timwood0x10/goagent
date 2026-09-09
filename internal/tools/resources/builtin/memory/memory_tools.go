package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// MemorySearch searches distilled memories and user preferences.
type MemorySearch struct {
	*base.BaseTool
	memoryMgr memory.MemoryManager
}

// NewMemorySearch creates a new MemorySearch tool.
func NewMemorySearch(memoryMgr memory.MemoryManager) *MemorySearch {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"query": {
				Type:        "string",
				Description: "Search query for memories and preferences",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of results to return",
				Default:     5,
			},
		},
		Required: []string{"query"},
	}

	ms := &MemorySearch{
		memoryMgr: memoryMgr,
	}
	ms.BaseTool = base.NewBaseToolWithCapabilities("memory_search", "Search distilled memories and user preferences", core.CategoryMemory, []core.Capability{core.CapabilityMemory}, params)

	return ms
}

// Execute performs memory search.
func (t *MemorySearch) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	query, ok := params["query"].(string)
	if !ok || query == "" {
		return core.NewErrorResult("query is required"), nil
	}

	limit := getInt(params, "limit", 5)
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}

	if t.memoryMgr == nil {
		return core.NewErrorResult("memory manager not available"), nil
	}

	// Search similar tasks/memories
	tasks, err := t.memoryMgr.SearchSimilarTasks(ctx, query, limit)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	// Format results
	memories := make([]map[string]interface{}, len(tasks))
	for i, task := range tasks {
		input := ""
		output := ""
		context := ""
		score := 0.0

		if task.Payload != nil {
			if val, ok := task.Payload["input"].(string); ok {
				input = val
			}
			if val, ok := task.Payload["output"].(string); ok {
				output = val
			}
			if val, ok := task.Payload["context"].(string); ok {
				context = val
			}
			if val, ok := task.Payload["score"].(float64); ok {
				score = val
			}
		}

		memories[i] = map[string]interface{}{
			"task_id": task.TaskID,
			"input":   input,
			"output":  output,
			"context": context,
			"score":   score,
		}
	}

	return core.NewResult(true, map[string]interface{}{
		"memories":      memories,
		"total_results": len(memories),
		"query":         query,
	}), nil
}

// UserProfile retrieves user profile and preferences from memory.
// The distilled_memories database path was removed (schema ghost: the
// repository never had a production constructor, so the table saw zero
// reads/writes); the tool runs on the memory manager's task history, which
// is the path that actually executed in production.
type UserProfile struct {
	*base.BaseTool
	memoryMgr memory.MemoryManager
}

// NewUserProfile creates a new UserProfile tool.
func NewUserProfile(memoryMgr memory.MemoryManager) *UserProfile {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"user_id": {
				Type:        "string",
				Description: "User identifier",
			},
			"tenant_id": {
				Type:        "string",
				Description: "Tenant identifier for multi-tenant isolation",
			},
			"session_id": {
				Type:        "string",
				Description: "Session identifier (optional, for current session context)",
			},
		},
		Required: []string{"user_id", "tenant_id"},
	}

	up := &UserProfile{
		memoryMgr: memoryMgr,
	}
	up.BaseTool = base.NewBaseToolWithCapabilities("user_profile", "Retrieve user profile and preferences from memory", core.CategoryMemory, []core.Capability{core.CapabilityMemory}, params)

	return up
}

// Execute retrieves user profile.
//
//nolint:gocyclo // Complex user profile retrieval with multiple field types
//nolint:gocyclo
func (t *UserProfile) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	// Get required parameters
	userID, ok := params["user_id"].(string)
	if !ok || userID == "" {
		return core.NewErrorResult("user_id is required"), nil
	}

	tenantID, ok := params["tenant_id"].(string)
	if !ok || tenantID == "" {
		return core.NewErrorResult("tenant_id is required"), nil
	}

	profile := map[string]interface{}{
		"user_id":      userID,
		"tenant_id":    tenantID,
		"preferences":  make([]map[string]interface{}, 0),
		"interactions": make([]map[string]interface{}, 0),
		"tech_stack":   make([]string, 0),
		"memories":     make([]map[string]interface{}, 0),
	}

	// The distilled-memories database branch was removed with the schema
	// ghost (zero production writes ever reached the table); the memory
	// manager's task-history branch below is the live path.

	// Second, search in-memory tasks if memory manager is available
	if t.memoryMgr != nil {
		queries := []string{
			fmt.Sprintf("%s preferences", userID),
			fmt.Sprintf("%s likes", userID),
			fmt.Sprintf("%s dislikes", userID),
			fmt.Sprintf("%s tech stack", userID),
		}

		for _, query := range queries {
			tasks, err := t.memoryMgr.SearchSimilarTasks(ctx, query, 3)
			if err != nil {
				continue
			}

			for _, task := range tasks {
				if task.Payload == nil {
					continue
				}

				info := map[string]interface{}{
					"task_id": task.TaskID,
				}

				taskInput := ""
				if val, ok := task.Payload["input"].(string); ok {
					taskInput = val
					info["input"] = val
				}
				if output, ok := task.Payload["output"].(string); ok {
					info["output"] = output
				}
				if score, ok := task.Payload["score"].(float64); ok {
					info["score"] = score
				}

				// Classify based on content
				if containsKeywords(taskInput, []string{"喜欢", "like", "prefer", "爱好"}) {
					profile["preferences"] = append(profile["preferences"].([]map[string]interface{}), info)
				} else {
					profile["interactions"] = append(profile["interactions"].([]map[string]interface{}), info)
				}
			}
		}
	}

	// Get current session context if provided
	if sessID, ok := params["session_id"].(string); ok && sessID != "" && t.memoryMgr != nil {
		messages, err := t.memoryMgr.GetMessages(ctx, sessID)
		if err == nil && len(messages) > 0 {
			profile["current_session_messages"] = len(messages)
		}
	}

	return core.NewResult(true, profile), nil
}

// containsKeywords checks if text contains any of the keywords.
func containsKeywords(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if containsText(text, keyword) {
			return true
		}
	}
	return false
}

// containsText checks if text contains substring (case-insensitive).
func containsText(text, substr string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(substr))
}

// getInt extracts an integer value from params with a default value.
func getInt(params map[string]interface{}, key string, defaultVal int) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}
