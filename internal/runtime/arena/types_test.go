package arena

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionSerialization(t *testing.T) {
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	action := Action{
		ID:        "test-1",
		Type:      ActionKillLeader,
		TargetID:  "leader-1",
		Metadata:  map[string]any{"key": "value"},
		CreatedAt: now,
	}

	data, err := json.Marshal(action)
	require.NoError(t, err)

	var decoded Action
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, action.ID, decoded.ID)
	assert.Equal(t, action.Type, decoded.Type)
	assert.Equal(t, action.TargetID, decoded.TargetID)
	assert.Equal(t, "value", decoded.Metadata["key"])
}

func TestActionSerializationOmitEmpty(t *testing.T) {
	action := Action{
		ID:       "test-2",
		Type:     ActionKillAgent,
		TargetID: "agent-1",
	}

	data, err := json.Marshal(action)
	require.NoError(t, err)

	// source_id and metadata should be omitted when empty.
	assert.NotContains(t, string(data), "source_id")
	assert.NotContains(t, string(data), "metadata")
}

func TestResultSerialization(t *testing.T) {
	result := Result{
		Success: true,
		Action: Action{
			ID:       "test-3",
			Type:     ActionRemoveNode,
			TargetID: "node-1",
		},
		Duration: 150 * time.Millisecond,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded Result
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.True(t, decoded.Success)
	assert.Equal(t, "test-3", decoded.Action.ID)
	assert.Empty(t, decoded.Error)
}

func TestResultWithError(t *testing.T) {
	result := Result{
		Success: false,
		Action: Action{
			ID:       "test-4",
			Type:     ActionKillAgent,
			TargetID: "agent-2",
		},
		Error:    "agent not found",
		Duration: 50 * time.Millisecond,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded Result
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.False(t, decoded.Success)
	assert.Equal(t, "agent not found", decoded.Error)
}

func TestStatsCalculation(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 7,
		FailedActions:     3,
		LastAction:        time.Now(),
	}

	assert.Equal(t, 10, stats.TotalActions)
	assert.Equal(t, 7, stats.SuccessfulActions)
	assert.Equal(t, 3, stats.FailedActions)
	assert.True(t, stats.LastAction.After(time.Time{}))
}

func TestStatsSerialization(t *testing.T) {
	stats := Stats{
		TotalActions:      5,
		SuccessfulActions: 4,
		FailedActions:     1,
	}

	data, err := json.Marshal(stats)
	require.NoError(t, err)

	var decoded Stats
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, 5, decoded.TotalActions)
	assert.Equal(t, 4, decoded.SuccessfulActions)
	assert.Equal(t, 1, decoded.FailedActions)
}

func TestActionTypes(t *testing.T) {
	assert.Equal(t, ActionType("kill_leader"), ActionKillLeader)
	assert.Equal(t, ActionType("kill_agent"), ActionKillAgent)
	assert.Equal(t, ActionType("remove_node"), ActionRemoveNode)
	assert.Equal(t, ActionType("remove_edge"), ActionRemoveEdge)
}

func TestNewActionTypes(t *testing.T) {
	assert.Equal(t, ActionType("tool_timeout"), ActionToolTimeout)
	assert.Equal(t, ActionType("memory_corrupt"), ActionMemoryCorrupt)
	assert.Equal(t, ActionType("mcp_disconnect"), ActionMCPDisconnect)
	assert.Equal(t, ActionType("llm_failure"), ActionLLMFailure)
}
