package flight

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// ── Genealogy Tests ────────────────────────────

func TestRecordSpawn(t *testing.T) {
	g := NewGenealogy()

	g.RecordSpawn("parent-1", "child-1", "sub", nil)

	child, ok := g.GetNode("child-1")
	if !ok {
		t.Fatal("expected child node")
	}
	if child.ParentID != "parent-1" {
		t.Errorf("ParentID = %s, want parent-1", child.ParentID)
	}
	if child.Relation != RelationSpawned {
		t.Errorf("Relation = %s, want spawned", child.Relation)
	}
	if !child.IsAlive {
		t.Error("expected child to be alive")
	}
	if child.Type != "sub" {
		t.Errorf("Type = %s, want sub", child.Type)
	}
}

func TestRecordSpawnParentNotTracked(t *testing.T) {
	g := NewGenealogy()

	// Parent not tracked — placeholder parent is created as root.
	g.RecordSpawn("unknown-parent", "child-1", "sub", nil)

	roots := g.Roots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 root (placeholder parent), got %d", len(roots))
	}
	if roots[0].ID != "unknown-parent" {
		t.Errorf("root ID = %s, want unknown-parent", roots[0].ID)
	}

	// Child should exist and have parent.
	child, ok := g.GetNode("child-1")
	if !ok {
		t.Fatal("expected child-1 to exist")
	}
	if child.ParentID != "unknown-parent" {
		t.Errorf("ParentID = %s, want unknown-parent", child.ParentID)
	}
}

func TestRecordResurrection(t *testing.T) {
	g := NewGenealogy()

	// Set up: parent spawns child.
	g.RecordSpawn("parent-1", "child-1", "sub", nil)

	// Child dies and is resurrected.
	g.RecordResurrection("child-1", "child-2")

	// Old node should be dead.
	old, ok := g.GetNode("child-1")
	if !ok {
		t.Fatal("expected old node to exist")
	}
	if old.IsAlive {
		t.Error("expected old node to be dead")
	}
	if old.DiedAt.IsZero() {
		t.Error("expected DiedAt to be set")
	}

	// New node should be alive.
	newNode, ok := g.GetNode("child-2")
	if !ok {
		t.Fatal("expected new node")
	}
	if !newNode.IsAlive {
		t.Error("expected new node to be alive")
	}
	if newNode.Type != "sub" {
		t.Errorf("Type = %s, want sub (inherited)", newNode.Type)
	}
	if newNode.Relation != RelationResurrected {
		t.Errorf("Relation = %s, want resurrected", newNode.Relation)
	}

	// Parent should now have new node as child.
	parent, _ := g.GetNode("parent-1")
	found := false
	for _, c := range parent.Children {
		if c.ID == "child-2" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected parent to have child-2 as child")
	}
}

func TestRecordResurrectionUnknownAgent(t *testing.T) {
	g := NewGenealogy()

	// Resurrect an agent we never tracked.
	g.RecordResurrection("unknown-1", "new-1")

	newNode, ok := g.GetNode("new-1")
	if !ok {
		t.Fatal("expected new node")
	}
	if !newNode.IsAlive {
		t.Error("expected new node to be alive")
	}

	// Should be a root since parent was unknown.
	roots := g.Roots()
	found := false
	for _, r := range roots {
		if r.ID == "new-1" {
			found = true
		}
	}
	if !found {
		t.Error("expected new-1 to be a root")
	}
}

func TestRecordDeath(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("p", "a1", "sub", nil)

	g.RecordDeath("a1")

	node, _ := g.GetNode("a1")
	if node.IsAlive {
		t.Error("expected dead")
	}
	if node.DiedAt.IsZero() {
		t.Error("expected DiedAt to be set")
	}
}

func TestRecordDeathUnknown(t *testing.T) {
	g := NewGenealogy()
	// Should not panic.
	g.RecordDeath("nonexistent")
}

func TestRecordPromotion(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("p", "a1", "sub", nil)

	g.RecordPromotion("a1")

	node, _ := g.GetNode("a1")
	if node.Relation != RelationPromoted {
		t.Errorf("Relation = %s, want promoted", node.Relation)
	}
}

func TestGetNodeNotFound(t *testing.T) {
	g := NewGenealogy()
	_, ok := g.GetNode("nonexistent")
	if ok {
		t.Error("expected false")
	}
}

func TestRoots(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "r1", "leader", nil)
	g.RecordSpawn("", "r2", "leader", nil)
	g.RecordSpawn("r1", "c1", "sub", nil)

	roots := g.Roots()
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(roots))
	}
}

func TestDescendants(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("p", "a1", "leader", nil)
	g.RecordSpawn("a1", "a2", "sub", nil)
	g.RecordSpawn("a1", "a3", "sub", nil)
	g.RecordSpawn("a2", "a4", "tool", nil)

	desc := g.Descendants("a1")
	if len(desc) != 3 {
		t.Fatalf("expected 3 descendants, got %d", len(desc))
	}

	// Check a4 is included (grandchild).
	found := false
	for _, d := range desc {
		if d.ID == "a4" {
			found = true
		}
	}
	if !found {
		t.Error("expected a4 in descendants")
	}
}

func TestDescendantsEmpty(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("p", "leaf", "sub", nil)

	desc := g.Descendants("leaf")
	if len(desc) != 0 {
		t.Errorf("expected 0 descendants, got %d", len(desc))
	}
}

func TestAncestors(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "root", "leader", nil)
	g.RecordSpawn("root", "mid", "sub", nil)
	g.RecordSpawn("mid", "leaf", "tool", nil)

	chain := g.Ancestors("leaf")
	if len(chain) != 3 {
		t.Fatalf("expected 3 ancestors, got %d", len(chain))
	}
	if chain[0] != "root" || chain[1] != "mid" || chain[2] != "leaf" {
		t.Errorf("chain = %v, want [root mid leaf]", chain)
	}
}

func TestAncestorsRoot(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "root", "leader", nil)

	chain := g.Ancestors("root")
	if len(chain) != 1 || chain[0] != "root" {
		t.Errorf("chain = %v, want [root]", chain)
	}
}

func TestIsAlive(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("p", "a1", "sub", nil)

	if !g.IsAlive("a1") {
		t.Error("expected alive")
	}
	if g.IsAlive("nonexistent") {
		t.Error("expected false for nonexistent")
	}

	g.RecordDeath("a1")
	if g.IsAlive("a1") {
		t.Error("expected dead after RecordDeath")
	}
}

func TestExportMermaid(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "root", "leader", nil)
	g.RecordSpawn("root", "child", "sub", nil)

	mermaid := g.ExportMermaid()
	if mermaid == "" {
		t.Fatal("expected non-empty mermaid")
	}
	if mermaid[:7] != "graph L" {
		t.Errorf("expected mermaid graph, got %s", mermaid[:7])
	}
}

func TestExportMermaidEmpty(t *testing.T) {
	g := NewGenealogy()
	mermaid := g.ExportMermaid()
	if mermaid == "" {
		t.Fatal("expected non-empty")
	}
}

func TestExportJSON(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "root", "leader", nil)
	g.RecordSpawn("root", "child", "sub", nil)

	data, err := g.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty JSON")
	}

	var result []*LineageNode
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 root in JSON, got %d", len(result))
	}
}

func TestAllNodes(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "r", "leader", nil)
	g.RecordSpawn("r", "c1", "sub", nil)
	g.RecordSpawn("r", "c2", "sub", nil)

	nodes := g.AllNodes()
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
}

func TestMultipleResurrections(t *testing.T) {
	g := NewGenealogy()
	g.RecordSpawn("", "agent-v1", "sub", nil)

	// First resurrection.
	g.RecordResurrection("agent-v1", "agent-v2")
	if g.IsAlive("agent-v1") {
		t.Error("v1 should be dead")
	}
	if !g.IsAlive("agent-v2") {
		t.Error("v2 should be alive")
	}

	// Second resurrection.
	g.RecordResurrection("agent-v2", "agent-v3")
	if g.IsAlive("agent-v2") {
		t.Error("v2 should be dead")
	}
	if !g.IsAlive("agent-v3") {
		t.Error("v3 should be alive")
	}
}

func TestConcurrentRecordSpawn(t *testing.T) {
	g := NewGenealogy()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			g.RecordSpawn("", fmt.Sprintf("agent-%d", n), "sub", nil)
		}(i)
	}

	wg.Wait()

	roots := g.Roots()
	if len(roots) != 50 {
		t.Errorf("expected 50 roots, got %d", len(roots))
	}
}

// ── GenealogyCollector Tests ────────────────────

func TestGenealogyCollectorStartStop(t *testing.T) {
	c := NewGenealogyCollector(nil)
	ctx := context.Background()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	c.Stop()
}

func TestGenealogyCollectorProcessAgentStarted(t *testing.T) {
	c := NewGenealogyCollector(nil)

	c.processEvent(&ares_events.Event{
		ID:        "e1",
		StreamID:  "agent-1",
		Type:      ares_events.EventAgentStarted,
		Timestamp: time.Now(),
		Payload:   map[string]any{"type": "leader"},
	})

	g := c.Genealogy()
	node, ok := g.GetNode("agent-1")
	if !ok {
		t.Fatal("expected agent-1 in genealogy")
	}
	if node.Type != "leader" {
		t.Errorf("Type = %s, want leader", node.Type)
	}
	if !node.IsAlive {
		t.Error("expected alive")
	}
}

func TestGenealogyCollectorProcessSpawn(t *testing.T) {
	c := NewGenealogyCollector(nil)

	// Parent starts.
	c.processEvent(&ares_events.Event{
		StreamID: "parent", Type: ares_events.EventAgentStarted,
		Timestamp: time.Now(), Payload: map[string]any{"type": "leader"},
	})

	// Child starts with parent_id.
	c.processEvent(&ares_events.Event{
		StreamID: "child", Type: ares_events.EventAgentStarted,
		Timestamp: time.Now(), Payload: map[string]any{"type": "sub", "parent_id": "parent"},
	})

	g := c.Genealogy()
	child, ok := g.GetNode("child")
	if !ok {
		t.Fatal("expected child")
	}
	if child.ParentID != "parent" {
		t.Errorf("ParentID = %s, want parent", child.ParentID)
	}
}

func TestGenealogyCollectorProcessAgentStopped(t *testing.T) {
	c := NewGenealogyCollector(nil)

	c.processEvent(&ares_events.Event{
		StreamID: "a1", Type: ares_events.EventAgentStarted,
		Timestamp: time.Now(), Payload: map[string]any{"type": "sub"},
	})
	c.processEvent(&ares_events.Event{
		StreamID: "a1", Type: ares_events.EventAgentStopped,
		Timestamp: time.Now(),
	})

	if c.Genealogy().IsAlive("a1") {
		t.Error("expected dead")
	}
}

func TestGenealogyCollectorProcessFailover(t *testing.T) {
	c := NewGenealogyCollector(nil)

	// Old agent starts.
	c.processEvent(&ares_events.Event{
		StreamID: "old-agent", Type: ares_events.EventAgentStarted,
		Timestamp: time.Now(), Payload: map[string]any{"type": "leader"},
	})

	// Failover triggered.
	c.processEvent(&ares_events.Event{
		StreamID: "old-agent", Type: ares_events.EventFailoverTriggered,
		Timestamp: time.Now(), Payload: map[string]any{"agent_id": "old-agent"},
	})

	// Failover completed with resurrection.
	c.processEvent(&ares_events.Event{
		StreamID: "new-agent", Type: ares_events.EventFailoverCompleted,
		Timestamp: time.Now(), Payload: map[string]any{
			"old_agent_id": "old-agent",
			"new_agent_id": "new-agent",
		},
	})

	g := c.Genealogy()
	if g.IsAlive("old-agent") {
		t.Error("old-agent should be dead")
	}
	if !g.IsAlive("new-agent") {
		t.Error("new-agent should be alive")
	}

	newNode, _ := g.GetNode("new-agent")
	if newNode.Relation != RelationResurrected {
		t.Errorf("Relation = %s, want resurrected", newNode.Relation)
	}
}

func TestGenealogyCollectorNilEvent(t *testing.T) {
	c := NewGenealogyCollector(nil)
	// Should not panic.
	c.processEvent(nil)
}
