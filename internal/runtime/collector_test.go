package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExecutionCollector(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	require.NotNil(t, c)
	assert.Equal(t, "exec-1", c.ExecutionID())
	assert.Empty(t, c.RouteHistory())
	assert.Empty(t, c.ToolHistory())
	assert.Empty(t, c.MemoryHits())
	assert.Empty(t, c.InterruptLog())
	assert.Empty(t, c.ErrorLog())
}

func TestExecutionCollector_RecordRoute(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordRoute("s1", "s2", "output contains error", "expression")
	c.RecordRoute("s2", "s3", "default path", "expression")

	routes := c.RouteHistory()
	assert.Len(t, routes, 2)
	assert.Equal(t, "s1", routes[0].StepID)
	assert.Equal(t, "s2", routes[0].Decision)
	assert.Equal(t, "expression", routes[0].Source)
	assert.False(t, routes[0].Timestamp.IsZero())
}

func TestExecutionCollector_RecordTool(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordTool("s1", "calculator", "2+2", "4", 100*time.Millisecond, true)
	c.RecordTool("s2", "file_read", "/tmp/x", "content", 50*time.Millisecond, false)

	tools := c.ToolHistory()
	assert.Len(t, tools, 2)
	assert.Equal(t, "calculator", tools[0].ToolName)
	assert.True(t, tools[0].Success)
	assert.Equal(t, 100*time.Millisecond, tools[0].Duration)
	assert.False(t, tools[1].Success)
}

func TestExecutionCollector_RecordMemoryHit(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordMemoryHit("s1", "similar tasks", 3, 0.95, []string{"task-1", "task-2"})

	hits := c.MemoryHits()
	require.Len(t, hits, 1)
	assert.Equal(t, "s1", hits[0].StepID)
	assert.Equal(t, 3, hits[0].HitCount)
	assert.Equal(t, 0.95, hits[0].BestScore)
	assert.Equal(t, []string{"task-1", "task-2"}, hits[0].UsedIDs)
}

func TestExecutionCollector_RecordInterrupt(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordInterrupt("s1", "reject", "wrong approach")

	log := c.InterruptLog()
	require.Len(t, log, 1)
	assert.Equal(t, "reject", log[0].Action)
	assert.Equal(t, "wrong approach", log[0].Feedback)
}

func TestExecutionCollector_RecordError(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordError("s1", "connection refused")

	errs := c.ErrorLog()
	require.Len(t, errs, 1)
	assert.Equal(t, "connection refused", errs[0].Message)
}

func TestExecutionCollector_Export(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordRoute("s1", "s2", "test", "expression")
	c.RecordTool("s1", "calc", "1+1", "2", time.Second, true)

	exp := c.Export()
	assert.Equal(t, "exec-1", exp["execution_id"])
	assert.Len(t, exp["route_history"], 1)
	assert.Len(t, exp["tool_history"], 1)
	assert.Len(t, exp["error_log"], 0)
}

func TestExecutionCollector_Reset(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordRoute("s1", "s2", "test", "expression")
	assert.Len(t, c.RouteHistory(), 1)

	c.Reset()
	assert.Empty(t, c.RouteHistory())
	assert.Empty(t, c.ToolHistory())
	assert.Empty(t, c.MemoryHits())
}

func TestExecutionCollector_ThreadSafety(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.RecordRoute("s1", "s2", "test", "expression")
			c.RecordTool("s1", "calc", "1+1", "2", time.Millisecond, true)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			c.RouteHistory()
			c.ToolHistory()
			c.Export()
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 100; i++ {
			c.RecordMemoryHit("s1", "query", 1, 0.5, nil)
			c.RecordError("s1", "error")
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	<-done
	assert.Len(t, c.RouteHistory(), 100)
	assert.Len(t, c.ToolHistory(), 100)
	assert.Len(t, c.MemoryHits(), 100)
	assert.Len(t, c.ErrorLog(), 100)
}

func TestExecutionCollector_MergeInto(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	c.RecordRoute("s1", "s2", "test", "expression")
	c.RecordTool("s1", "calc", "1+1", "2", time.Second, true)
	c.RecordMemoryHit("s1", "query", 1, 0.5, nil)
	c.RecordInterrupt("s1", "approve", "looks good")
	c.RecordError("s2", "timeout")

	ckpt := &ExperienceCheckpoint{
		ExecutionID: "exec-1",
	}
	c.MergeInto(ckpt)

	assert.Len(t, ckpt.RouteHistory, 1)
	assert.Equal(t, "s2", ckpt.RouteHistory[0].ToStepID)
	assert.Len(t, ckpt.ToolHistory, 1)
	assert.Equal(t, "calc", ckpt.ToolHistory[0].ToolName)
	assert.Len(t, ckpt.MemoryHits, 1)
	assert.Equal(t, 0.5, ckpt.MemoryHits[0].Similarity)
	assert.Len(t, ckpt.InterruptHistory, 1)
	assert.True(t, ckpt.InterruptHistory[0].Approved)
	assert.Len(t, ckpt.ErrorHistory, 1)
	assert.Equal(t, "timeout", ckpt.ErrorHistory[0].Message)
}

func TestExecutionCollector_MergeIntoEmptyCollector(t *testing.T) {
	c := NewExecutionCollector("exec-1")
	ckpt := &ExperienceCheckpoint{ExecutionID: "exec-1"}
	c.MergeInto(ckpt)
	assert.Empty(t, ckpt.RouteHistory)
	assert.Empty(t, ckpt.ToolHistory)
}
