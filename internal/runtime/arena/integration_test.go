package arena

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

func TestFlightBridgeNilRecorder(t *testing.T) {
	bridge := NewFlightBridge(nil)

	action := Action{
		ID:        "test-1",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}
	result := Result{
		Success:  false,
		Action:   action,
		Error:    "test error",
		Duration: 100 * time.Millisecond,
	}

	assert.NotPanics(t, func() {
		bridge.OnActionExecuted(action, result)
	})
}

func TestFlightBridgeOnActionExecuted(t *testing.T) {
	recorder := flight.NewFlightRecorder(flight.FlightRecorderConfig{})
	require.NotNil(t, recorder)

	bridge := NewFlightBridge(recorder)
	require.NotNil(t, bridge)

	action := Action{
		ID:        "test-2",
		Type:      ActionKillAgent,
		TargetID:  "agent-2",
		SourceID:  "source-1",
		CreatedAt: time.Now(),
	}
	result := Result{
		Success:  false,
		Action:   action,
		Error:    "kill failed",
		Duration: 200 * time.Millisecond,
	}

	bridge.OnActionExecuted(action, result)

	timelineEvents := recorder.Timeline().Events()
	assert.Equal(t, 1, len(timelineEvents), "should have one timeline event")

	te := timelineEvents[0]
	assert.Equal(t, action.ID, te.ID)
	assert.Equal(t, action.TargetID, te.AgentID)
	assert.Equal(t, flight.EventToolCall, te.Type)
	assert.Equal(t, "arena:"+string(action.Type), te.Name)
	assert.Equal(t, "arena", te.Metadata["source"])
	assert.Equal(t, false, te.Metadata["success"])

	diagRecords := recorder.Diagnostics().All()
	assert.Equal(t, 1, len(diagRecords), "should have one diagnostic record for failed action")

	dr := diagRecords[0]
	assert.Equal(t, action.ID+"-diag", dr.ID)
	assert.Equal(t, action.TargetID, dr.AgentID)
	assert.Equal(t, "arena-"+string(action.Type), dr.TaskID)
	assert.Contains(t, dr.RootCause, "fault_injection")
	assert.Equal(t, result.Error, dr.Context["error"])
}

func TestFlightBridgeOnActionExecuted_SuccessNoDiagnostic(t *testing.T) {
	recorder := flight.NewFlightRecorder(flight.FlightRecorderConfig{})

	bridge := NewFlightBridge(recorder)

	action := Action{
		ID:        "test-3",
		Type:      ActionPauseAgent,
		TargetID:  "agent-3",
		CreatedAt: time.Now(),
	}
	result := Result{
		Success:  true,
		Action:   action,
		Duration: 50 * time.Millisecond,
	}

	bridge.OnActionExecuted(action, result)

	timelineEvents := recorder.Timeline().Events()
	assert.Equal(t, 1, len(timelineEvents), "successful action should still create timeline event")

	diagRecords := recorder.Diagnostics().All()
	assert.Equal(t, 0, len(diagRecords), "successful action should not create diagnostic record")
}

func TestArenaActionToCategory(t *testing.T) {
	tests := []struct {
		actionType ActionType
		expected   flight.DiagnosticCategory
	}{
		{ActionKillLeader, flight.DiagConcurrencyError},
		{ActionKillAgent, flight.DiagConcurrencyError},
		{ActionKillOrchestrator, flight.DiagConcurrencyError},
		{ActionNetworkPartition, flight.DiagNetworkError},
		{ActionRemoveNode, flight.DiagConfigError},
		{ActionRemoveEdge, flight.DiagConfigError},
		{ActionPauseAgent, flight.DiagConcurrencyError},
		{ActionResumeAgent, flight.DiagConcurrencyError},
		{ActionSlowAgent, flight.DiagToolTimeout},
	}

	for _, tt := range tests {
		t.Run(string(tt.actionType), func(t *testing.T) {
			got := arenaActionToCategory(tt.actionType)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestServiceWithBridge(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []runtime.AgentInfo {
			return []runtime.AgentInfo{{ID: "agent-1", Type: "sub"}}
		},
	}
	inj := NewInjector(rt, nil)
	svc := NewService(inj, nil, nil)

	recorder := flight.NewFlightRecorder(flight.FlightRecorderConfig{})
	bridge := NewFlightBridge(recorder)
	svc.SetFlightBridge(bridge)

	action := Action{
		ID:        "bridge-test-1",
		Type:      ActionKillAgent,
		TargetID:  "agent-1",
		CreatedAt: time.Now(),
	}

	result := svc.Execute(context.Background(), action)
	assert.True(t, result.Success)

	events := recorder.Timeline().Events()
	assert.Equal(t, 1, len(events), "execute should trigger bridge to record timeline event")
	assert.Equal(t, "bridge-test-1", events[0].ID)
	assert.Equal(t, "arena", events[0].Metadata["source"])
}
