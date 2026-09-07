package agentfabric

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestKillCapturesLastCognitiveSnapshot is the A1 acceptance: an agent that
// runs, writes cognition, and is then KILLED leaves behind a complete,
// versioned snapshot — the raw material for in-place cognitive revival.
func TestKillCapturesLastCognitiveSnapshot(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()

	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:     "worker",
		Capabilities: []string{"audit", "rust"},
		ParentID:     "origin-A",
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	want := CognitiveState{
		SchemaVersion: CognitiveStateSchemaVersion,
		Context:       "analyzing FFI boundaries",
		Observation:   "17 unsafe blocks",
		WorkingMemory: map[string]any{"step": 3},
		Decision:      "split investigation",
		Checkpoint:    "mid-quantum",
	}
	if err := f.SetCognitiveState("worker", want); err != nil {
		t.Fatalf("SetCognitiveState: %v", err)
	}

	if err := f.Kill(ctx, "worker"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Post-death the registry entry is gone...
	if _, err := f.Get("worker"); err == nil {
		t.Fatal("killed agent must be removed from the registry")
	}
	// ...but the revival record must survive with full fidelity.
	snap, ok := f.LastSnapshot("worker")
	if !ok {
		t.Fatal("death must capture a cognitive snapshot")
	}
	if snap.Cognitive.Observation != "17 unsafe blocks" ||
		snap.Cognitive.Checkpoint != "mid-quantum" {
		t.Fatalf("snapshot fields incomplete: %+v", snap.Cognitive)
	}
	if snap.Cognitive.SchemaVersion != CognitiveStateSchemaVersion {
		t.Fatalf("snapshot schema version = %d, want %d (code_rules)",
			snap.Cognitive.SchemaVersion, CognitiveStateSchemaVersion)
	}
	// Revival needs the declared capabilities and provenance to rebuild an
	// equivalent body (fusion plan A1 rationale).
	if len(snap.Capabilities) != 2 || snap.Capabilities[0] != "audit" {
		t.Fatalf("snapshot capabilities = %v, want [audit rust]", snap.Capabilities)
	}
	if snap.Parent != "origin-A" {
		t.Fatalf("snapshot parent = %q, want origin-A (provenance continuity)", snap.Parent)
	}
}

// TestSnapshotZeroVersionNormalizedAtCapture covers the legacy-state boundary:
// a state written before versioning (SchemaVersion 0) is stamped with the
// current schema version at capture time, so the stored envelope always
// self-describes its schema.
func TestSnapshotZeroVersionNormalizedAtCapture(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "legacy"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Write directly through the agent to simulate a pre-versioning writer
	// (SetCognitiveState normalizes; this path must also be safe).
	a, err := f.Get("legacy")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a.mu.Lock()
	a.cognitive = CognitiveState{Context: "old writer"}
	a.mu.Unlock()

	if err := f.Kill(ctx, "legacy"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	snap, ok := f.LastSnapshot("legacy")
	if !ok {
		t.Fatal("snapshot must exist")
	}
	if snap.Cognitive.SchemaVersion != CognitiveStateSchemaVersion {
		t.Fatalf("zero-version state must be normalized at capture, got %d",
			snap.Cognitive.SchemaVersion)
	}
}

// TestRetireClearsDeathSnapshot locks the terminal-state rule: Retire ends an
// identity's revivability. A stale death snapshot from an earlier kill/revive
// cycle of the same identity must not survive a later Retire — otherwise a
// retired agent could be resurrected from outdated cognition.
func TestRetireClearsDeathSnapshot(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	for i := 0; i < 2; i++ { // spawn → kill (captures) → re-spawn same id → retire
		if _, err := f.Spawn(ctx, SpawnSpec{Identity: "recycled"}); err != nil {
			t.Fatalf("Spawn cycle %d: %v", i, err)
		}
		if i == 0 {
			if err := f.Kill(ctx, "recycled"); err != nil {
				t.Fatalf("Kill: %v", err)
			}
			if _, ok := f.LastSnapshot("recycled"); !ok {
				t.Fatal("precondition: snapshot captured by kill")
			}
			continue
		}
		if err := f.Retire(ctx, "recycled"); err != nil {
			t.Fatalf("Retire: %v", err)
		}
	}
	if _, ok := f.LastSnapshot("recycled"); ok {
		t.Fatal("retired identity must not keep a revival snapshot")
	}
}

// TestClearSnapshotConsumesRecord verifies the consumption API used by a
// successful in-place revival: after clearing, the snapshot is gone so a much
// later death cannot accidentally restore stale cognition.
func TestClearSnapshotConsumesRecord(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "revive-me"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Kill(ctx, "revive-me"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, ok := f.LastSnapshot("revive-me"); !ok {
		t.Fatal("precondition: snapshot exists")
	}
	f.ClearSnapshot("revive-me")
	if _, ok := f.LastSnapshot("revive-me"); ok {
		t.Fatal("cleared snapshot must not be readable")
	}
}

// TestConcurrentKillAndSnapshotRead exercises the race surface called out in
// the fusion plan A1 acceptance: concurrent kills of DIFFERENT agents plus
// concurrent snapshot reads/writes must be clean under -race (the store has
// its own lock; capture happens under Fabric.mu + the agent's lock).
func TestConcurrentKillAndSnapshotRead(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	const n = 32
	for i := 0; i < n; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i%10))
		if _, err := f.Spawn(ctx, SpawnSpec{Identity: id}); err != nil {
			t.Fatalf("Spawn %s: %v", id, err)
		}
		_ = f.SetCognitiveState(id, CognitiveState{Context: id})
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i%10))
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = f.Kill(ctx, id)
		}()
		go func() {
			defer wg.Done()
			_, _ = f.LastSnapshot(id)
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		id := string(rune('a'+i%26)) + string(rune('0'+i%10))
		if _, ok := f.LastSnapshot(id); !ok {
			t.Fatalf("snapshot missing for %s after concurrent kill storm", id)
		}
	}
}

// TestFindRevivableSnapshotPicksMostRecentDeath locks the A2 review fix: when
// several dead agents share the requested capability, recovery must seed
// revival from the MOST RECENT death (freshest cognition), never from Go's
// random map iteration order.
func TestFindRevivableSnapshotPicksMostRecentDeath(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()

	for _, id := range []string{"old-victim", "new-victim"} {
		if _, err := f.Spawn(ctx, SpawnSpec{
			Identity:     id,
			Capabilities: []string{"audit"},
		}); err != nil {
			t.Fatalf("spawn %s: %v", id, err)
		}
	}
	// Distinct cognition so the assertion can tell them apart.
	_ = f.SetCognitiveState("old-victim", CognitiveState{Observation: "stale"})
	_ = f.SetCognitiveState("new-victim", CognitiveState{Observation: "fresh"})

	if err := f.Kill(ctx, "old-victim"); err != nil {
		t.Fatalf("kill old: %v", err)
	}
	// Ensure DiedAt strictly increases despite coarse clock resolution.
	time.Sleep(2 * time.Millisecond)
	if err := f.Kill(ctx, "new-victim"); err != nil {
		t.Fatalf("kill new: %v", err)
	}

	id, snap, ok := f.FindRevivableSnapshot("audit")
	if !ok {
		t.Fatal("revivable snapshot must be found")
	}
	if id != "new-victim" {
		t.Fatalf("revival must pick the most recent death, got %q (cognition %q)", id, snap.Cognitive.Observation)
	}
	if snap.Cognitive.Observation != "fresh" {
		t.Fatalf("snapshot cognition = %q, want the freshest one", snap.Cognitive.Observation)
	}
}
