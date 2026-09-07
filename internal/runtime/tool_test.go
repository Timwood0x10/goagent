package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolPlugin_RecordToolCall(t *testing.T) {
	collector := NewExecutionCollector("exec-1")
	toolPlugin := NewToolPlugin("tools")
	toolPlugin.WithCollector(collector)

	err := toolPlugin.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID:   "s1",
		Status:   StepStatusCompleted,
		Duration: 100 * time.Millisecond,
		Metadata: map[string]string{
			PayloadKeyToolName: "search",
		},
	})
	require.NoError(t, err)

	tools := collector.ToolHistory()
	assert.Len(t, tools, 1)
	assert.Equal(t, "search", tools[0].ToolName)
	assert.Equal(t, "s1", tools[0].StepID)
	assert.True(t, tools[0].Success)
	assert.Equal(t, 100*time.Millisecond, tools[0].Duration)
}

func TestToolPlugin_NoMetadata(t *testing.T) {
	collector := NewExecutionCollector("exec-1")
	toolPlugin := NewToolPlugin("tools")
	toolPlugin.WithCollector(collector)

	err := toolPlugin.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1",
		Status: StepStatusCompleted,
	})
	require.NoError(t, err)

	assert.Len(t, collector.ToolHistory(), 0)
}

func TestToolPlugin_Registry(t *testing.T) {
	p := NewToolPlugin("tools")
	assert.False(t, p.IsRegistered("search"))
	p.RegisterTool("search")
	assert.True(t, p.IsRegistered("search"))
	assert.False(t, p.IsRegistered("unknown"))
}

// TestToolPlugin_RegisteredToolPasses verifies that when an allowlist is
// configured, a step invoking a registered tool is recorded without error.
func TestToolPlugin_RegisteredToolPasses(t *testing.T) {
	collector := NewExecutionCollector("exec-1")
	p := NewToolPlugin("tools")
	p.WithCollector(collector)
	p.RegisterTool("search")

	err := p.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID:   "s1",
		Status:   StepStatusCompleted,
		Duration: 10 * time.Millisecond,
		Metadata: map[string]string{PayloadKeyToolName: "search"},
	})
	require.NoError(t, err)

	tools := collector.ToolHistory()
	require.Len(t, tools, 1)
	assert.Equal(t, "search", tools[0].ToolName)
}

// TestToolPlugin_UnregisteredToolFails verifies that when an allowlist is
// configured, a step invoking a tool NOT in the registry returns
// ErrToolNotRegistered (wrapped) and does not record the call.
func TestToolPlugin_UnregisteredToolFails(t *testing.T) {
	collector := NewExecutionCollector("exec-1")
	p := NewToolPlugin("tools")
	p.WithCollector(collector)
	p.RegisterTool("search") // allowlist configured; "unknown" is not in it

	err := p.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID:   "s1",
		Status:   StepStatusCompleted,
		Metadata: map[string]string{PayloadKeyToolName: "unknown"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolNotRegistered), "want ErrToolNotRegistered, got %v", err)
	assert.Contains(t, err.Error(), "unknown")
	// The unregistered tool must not be recorded.
	assert.Empty(t, collector.ToolHistory())
}

// TestToolPlugin_EmptyRegistrySkipsValidation verifies that an empty registry
// means "no allowlist configured": any tool is accepted and recorded, with no
// validation error. This preserves backward compatibility for callers that
// never register tools.
func TestToolPlugin_EmptyRegistrySkipsValidation(t *testing.T) {
	collector := NewExecutionCollector("exec-1")
	p := NewToolPlugin("tools")
	p.WithCollector(collector)
	// No RegisterTool calls — allowlist is empty.

	err := p.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID:   "s1",
		Status:   StepStatusCompleted,
		Metadata: map[string]string{PayloadKeyToolName: "anything"},
	})
	require.NoError(t, err)
	require.Len(t, collector.ToolHistory(), 1)
	assert.Equal(t, "anything", collector.ToolHistory()[0].ToolName)
}

// TestToolPlugin_ValidationIndependentOfCollector verifies the allowlist is
// enforced even when no collector is attached (validation is not gated on
// recording).
func TestToolPlugin_ValidationIndependentOfCollector(t *testing.T) {
	p := NewToolPlugin("tools")
	// No collector.
	p.RegisterTool("search")

	err := p.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID:   "s1",
		Status:   StepStatusCompleted,
		Metadata: map[string]string{PayloadKeyToolName: "unknown"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrToolNotRegistered))
}
