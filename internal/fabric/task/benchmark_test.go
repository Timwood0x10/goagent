package taskfabric

import (
	"strconv"
	"testing"
	"time"
)

// benchmarkCandidates returns a small capable candidate pool (typical served
// deployment: a handful of agents).
func benchmarkCandidates() []Candidate {
	return []Candidate{
		{AgentID: "a1", Capabilities: []string{"code", "review"}, Load: 0.3, Confidence: 0.9},
		{AgentID: "a2", Capabilities: []string{"code"}, Load: 0.1, Confidence: 0.95},
		{AgentID: "a3", Capabilities: []string{"research", "write"}, Load: 0.6, Confidence: 0.8},
		{AgentID: "a4", Capabilities: []string{"write"}, Load: 0.0, Confidence: 0.7},
	}
}

// BenchmarkFabric_Create measures the durable-intent registration path (a
// stream of distinct tasks, as a scheduler drains a work queue).
func BenchmarkFabric_Create(b *testing.B) {
	f := NewFabric()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Create(&Task{ID: "t-" + strconv.Itoa(i), Capability: "code"})
	}
}

// BenchmarkFabric_Schedule measures the capability-aware winner pick +
// acquire (P1: score = capability_overlap × (1-load) × confidence) including
// the lease-release cycle so each iteration starts from READY.
func BenchmarkFabric_Schedule(b *testing.B) {
	f := NewFabric()
	cands := benchmarkCandidates()
	taskID := "t-bench-schedule"
	_ = f.Create(&Task{ID: taskID, Capability: "code"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		winner, epoch, err := f.Schedule(taskID, cands, time.Minute)
		if err != nil {
			b.Fatalf("schedule: %v", err)
		}
		if err := f.Release(taskID, winner, epoch); err != nil {
			b.Fatalf("release: %v", err)
		}
	}
}

// BenchmarkFabric_RunQuantum measures one full execution quantum of a
// freshly created + acquired task (Create → Schedule → Start → step →
// COMPLETE). Each iteration uses a distinct task id because a COMPLETED task
// cannot be re-executed (durable intent, no state reset).
func BenchmarkFabric_RunQuantum(b *testing.B) {
	f := NewFabric()
	cands := benchmarkCandidates()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		taskID := "t-bench-quantum-" + strconv.Itoa(i)
		if err := f.Create(&Task{ID: taskID, Capability: "code"}); err != nil {
			b.Fatalf("create: %v", err)
		}
		winner, epoch, err := f.Schedule(taskID, cands, time.Minute)
		if err != nil {
			b.Fatalf("schedule: %v", err)
		}
		if err := f.RunQuantum(taskID, winner, epoch, func() (any, bool, error) {
			return map[string]any{"ok": true}, true, nil
		}); err != nil {
			b.Fatalf("run quantum: %v", err)
		}
	}
}

// BenchmarkFabric_ReadyTasks measures the DAG work-source drain over a small
// graph (P2: ReadyTasks gates on completed dependencies).
func BenchmarkFabric_ReadyTasks(b *testing.B) {
	f := NewFabric()
	for i := 0; i < 20; i++ {
		_ = f.Create(&Task{ID: "t-" + strconv.Itoa(i), Capability: "code"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.ReadyTasks()
	}
}

// BenchmarkFabric_IsReady measures the per-task DAG gate (dependency check).
func BenchmarkFabric_IsReady(b *testing.B) {
	f := NewFabric()
	_ = f.Create(&Task{ID: "dep", Capability: "code"})
	_ = f.Create(&Task{ID: "t", Capability: "code", Dependencies: []string{"dep"}})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = f.IsReady("t")
	}
}
