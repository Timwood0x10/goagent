package agentfabric

import (
	"context"
	"testing"
)

// BenchmarkFabric_Spawn measures the syscall-style agent creation (P3:
// identity, capabilities, provenance link) including the kill teardown so
// each iteration starts from an empty registry.
func BenchmarkFabric_Spawn(b *testing.B) {
	f := NewFabric()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Spawn(ctx, SpawnSpec{
			Identity:     "agent-x",
			Capabilities: []string{"code", "review"},
		}); err != nil {
			b.Fatalf("spawn: %v", err)
		}
		if err := f.Kill(ctx, "agent-x"); err != nil {
			b.Fatalf("kill: %v", err)
		}
	}
}

// BenchmarkFabric_SpawnWithResources measures spawn under a resource budget
// (P5 admission: quota check before creating the agent).
func BenchmarkFabric_SpawnWithResources(b *testing.B) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 16, "memory": 8192})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Spawn(ctx, SpawnSpec{
			Identity:     "agent-x",
			Capabilities: []string{"code"},
			Resources:    map[string]any{"cpu": 2, "memory": 512},
		}); err != nil {
			b.Fatalf("spawn: %v", err)
		}
		if err := f.Kill(ctx, "agent-x"); err != nil {
			b.Fatalf("kill: %v", err)
		}
	}
}

// BenchmarkFabric_LifecycleSuspendResume measures the suspend/resume round
// trip on a live agent.
func BenchmarkFabric_LifecycleSuspendResume(b *testing.B) {
	f := NewFabric()
	ctx := context.Background()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "agent-x"}); err != nil {
		b.Fatalf("spawn: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := f.Suspend(ctx, "agent-x"); err != nil {
			b.Fatalf("suspend: %v", err)
		}
		if err := f.Resume(ctx, "agent-x"); err != nil {
			b.Fatalf("resume: %v", err)
		}
	}
}

// BenchmarkFabric_Children measures the Process Tree provenance lookup (P3).
func BenchmarkFabric_Children(b *testing.B) {
	f := NewFabric()
	ctx := context.Background()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "parent"}); err != nil {
		b.Fatalf("spawn parent: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := f.Spawn(ctx, SpawnSpec{Identity: "child-" + string(rune('0'+i)), ParentID: "parent"}); err != nil {
			b.Fatalf("spawn child: %v", err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = f.Children("parent")
	}
}
