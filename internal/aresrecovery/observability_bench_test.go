package aresrecovery

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// Benchmarks for the v0.3.0 M4 observability features (global tracer +
// simulation sandbox). These measure the per-event recording cost and the
// recovery-chain replay throughput, which matter when tracing is enabled on a
// live runtime.

// BenchmarkGlobalTracerTraceTask measures per-event task-span recording.
func BenchmarkGlobalTracerTraceTask(b *testing.B) {
	tr := NewGlobalTracer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.TraceTask("t1", "step", nil)
	}
}

// BenchmarkGlobalTracerTraceMessage measures per-event message-span recording
// with parent linking.
func BenchmarkGlobalTracerTraceMessage(b *testing.B) {
	tr := NewGlobalTracer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.TraceMessage("corr-1", "sent", "t1", nil)
	}
}

// BenchmarkGlobalTracerSpans measures snapshotting the whole span set.
func BenchmarkGlobalTracerSpans(b *testing.B) {
	tr := NewGlobalTracer()
	for i := 0; i < 100; i++ {
		tr.TraceTask("t", "step", nil)
		tr.TraceMessage("m", "sent", "t", nil)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tr.Spans()
	}
}

// BenchmarkSandboxReplayRecoveryChain measures a full scripted recovery chain
// (create → spawn → acquire → kill → lease.expire → recover.all).
func BenchmarkSandboxReplayRecoveryChain(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks := taskfabric.NewFabric()
		agents := agentfabric.NewFabric()
		rec := New(tasks, agents, DefaultRestartPolicy())
		sb := NewSandbox(tasks, agents, rec)
		_, _ = sb.Replay(ctx, []SandboxEvent{
			{Type: SandboxEventTaskCreate, TaskID: "t1"},
			{Type: SandboxEventAgentSpawn, AgentID: "a1"},
			{Type: SandboxEventTaskAcquire, TaskID: "t1", AgentID: "a1"},
			{Type: SandboxEventAgentKill, AgentID: "a1"},
			{Type: SandboxEventLeaseExpire, TaskID: "t1"},
			{Type: SandboxEventRecoverAll, TaskID: "t1"},
		})
	}
}

// BenchmarkSandboxSimulateAgentDeath measures one offline failure prediction.
func BenchmarkSandboxSimulateAgentDeath(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tasks := taskfabric.NewFabric()
		agents := agentfabric.NewFabric()
		rec := New(tasks, agents, DefaultRestartPolicy())
		sb := NewSandbox(tasks, agents, rec)
		sb.WithClock(time.Now)
		_ = tasks.Create(&taskfabric.Task{ID: "t1", Capability: sandboxCapability})
		_, _ = agents.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a1", Capabilities: []string{sandboxCapability}})
		_, _ = tasks.Acquire("t1", "a1", time.Minute)
		_, _ = sb.Simulate(ctx, "a1", "t1")
	}
}
