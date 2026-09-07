package flight

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AgentRelation classifies the relationship between agents.
type AgentRelation string

const (
	RelationSpawned     AgentRelation = "spawned"
	RelationResurrected AgentRelation = "resurrected"
	RelationPromoted    AgentRelation = "promoted"
)

// LineageNode represents a single agent in the genealogy tree.
type LineageNode struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	ParentID  string         `json:"parent_id,omitempty"`
	Relation  AgentRelation  `json:"relation"`
	SpawnedAt time.Time      `json:"spawned_at"`
	DiedAt    time.Time      `json:"died_at,omitempty"`
	IsAlive   bool           `json:"is_alive"`
	Children  []*LineageNode `json:"children,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Genealogy tracks the complete family tree of all agents.
type Genealogy struct {
	roots []*LineageNode
	nodes map[string]*LineageNode
	mu    sync.RWMutex
}

// NewGenealogy creates an empty genealogy tree.
func NewGenealogy() *Genealogy {
	return &Genealogy{
		roots: make([]*LineageNode, 0),
		nodes: make(map[string]*LineageNode),
	}
}

// RecordSpawn records a parent-child relationship.
func (g *Genealogy) RecordSpawn(parentID, childID, agentType string, metadata map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()

	child := &LineageNode{
		ID:        childID,
		Type:      agentType,
		Relation:  RelationSpawned,
		SpawnedAt: time.Now(),
		IsAlive:   true,
		Metadata:  metadata,
	}

	if parentID != "" {
		child.ParentID = parentID

		// Ensure parent exists (create placeholder if needed).
		parent, ok := g.nodes[parentID]
		if !ok {
			parent = &LineageNode{
				ID:        parentID,
				SpawnedAt: time.Now(),
				IsAlive:   true,
			}
			g.nodes[parentID] = parent
			g.roots = append(g.roots, parent)
		}
		parent.Children = append(parent.Children, child)
	} else {
		g.roots = append(g.roots, child)
	}

	g.nodes[childID] = child
}

// RecordResurrection records that oldID died and was resurrected as newID.
func (g *Genealogy) RecordResurrection(oldID, newID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	oldNode, hasOld := g.nodes[oldID]
	if hasOld {
		oldNode.IsAlive = false
		oldNode.DiedAt = time.Now()
	}

	newNode := &LineageNode{
		ID:        newID,
		Relation:  RelationResurrected,
		SpawnedAt: time.Now(),
		IsAlive:   true,
	}

	// Inherit type and parent from old node.
	if hasOld {
		newNode.Type = oldNode.Type
		newNode.ParentID = oldNode.ParentID

		// Add as child of old node's parent.
		if oldNode.ParentID != "" {
			if parent, ok := g.nodes[oldNode.ParentID]; ok {
				parent.Children = append(parent.Children, newNode)
			}
		} else {
			// Old node was a root — new node replaces it.
			for i, r := range g.roots {
				if r.ID == oldID {
					g.roots[i] = newNode
					break
				}
			}
		}

		// Old node's children now belong to new node.
		newNode.Children = oldNode.Children
		oldNode.Children = nil
	} else {
		g.roots = append(g.roots, newNode)
	}

	g.nodes[newID] = newNode
}

// RecordRoot records a root agent (no parent).
func (g *Genealogy) RecordRoot(id, agentType string, metadata map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; !exists {
		node := &LineageNode{
			ID:        id,
			Type:      agentType,
			SpawnedAt: time.Now(),
			IsAlive:   true,
			Metadata:  metadata,
		}
		g.nodes[id] = node
		g.roots = append(g.roots, node)
	} else {
		// Node already exists (e.g., from a spawn record) — just mark alive.
		if node, ok := g.nodes[id]; ok {
			node.IsAlive = true
			node.SpawnedAt = time.Now()
		}
	}
}

// RecordDeath marks an agent as dead.
func (g *Genealogy) RecordDeath(agentID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if node, ok := g.nodes[agentID]; ok {
		node.IsAlive = false
		node.DiedAt = time.Now()
	}
}

// RecordPromotion marks an agent as promoted to leader.
func (g *Genealogy) RecordPromotion(agentID string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if node, ok := g.nodes[agentID]; ok {
		node.Relation = RelationPromoted
		if node.Metadata == nil {
			node.Metadata = make(map[string]any)
		}
		node.Metadata["promoted_at"] = time.Now()
	}
}

// GetNode returns a node by ID.
func (g *Genealogy) GetNode(id string) (*LineageNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	return n, ok
}

// Roots returns root nodes (agents with no parent).
func (g *Genealogy) Roots() []*LineageNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]*LineageNode, len(g.roots))
	copy(result, g.roots)
	return result
}

// Descendants returns all descendants of an agent.
func (g *Genealogy) Descendants(id string) []*LineageNode {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, ok := g.nodes[id]
	if !ok {
		return nil
	}

	var result []*LineageNode
	collectDescendants(node, &result)
	return result
}

func collectDescendants(node *LineageNode, result *[]*LineageNode) {
	for _, child := range node.Children {
		*result = append(*result, child)
		collectDescendants(child, result)
	}
}

// Ancestors returns the ancestor chain from root to this agent.
func (g *Genealogy) Ancestors(id string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var chain []string
	current := id
	for current != "" {
		chain = append([]string{current}, chain...)
		node, ok := g.nodes[current]
		if !ok {
			break
		}
		current = node.ParentID
	}
	return chain
}

// IsAlive checks if an agent is currently alive.
func (g *Genealogy) IsAlive(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	node, ok := g.nodes[id]
	if !ok {
		return false
	}
	return node.IsAlive
}

// ExportMermaid renders the genealogy as a Mermaid flowchart.
func (g *Genealogy) ExportMermaid() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if len(g.roots) == 0 {
		return "graph LR\n    empty[No agents]"
	}

	var b strings.Builder
	b.WriteString("graph LR\n")

	for _, root := range g.roots {
		writeLineageMermaid(&b, root, "    ")
	}
	return b.String()
}

func writeLineageMermaid(b *strings.Builder, n *LineageNode, indent string) {
	icon := "🤖"
	if !n.IsAlive {
		icon = "💀"
	}
	if n.Relation == RelationPromoted {
		icon = "👑"
	}

	status := "alive"
	if !n.IsAlive {
		status = "dead"
	}

	nodeID := strings.ReplaceAll(n.ID, "-", "_")
	fmt.Fprintf(b, "%s%s[\"%s %s (%s) %s\"]\n", indent, nodeID, icon, n.ID, n.Type, status)

	for _, child := range n.Children {
		childID := strings.ReplaceAll(child.ID, "-", "_")
		rel := string(child.Relation)
		fmt.Fprintf(b, "%s%s -->|%s| %s\n", indent, nodeID, rel, childID)
		writeLineageMermaid(b, child, indent)
	}
}

// ExportJSON serializes the genealogy as JSON.
func (g *Genealogy) ExportJSON() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return json.MarshalIndent(g.roots, "", "  ")
}

// AllNodes returns all tracked nodes.
func (g *Genealogy) AllNodes() []*LineageNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]*LineageNode, 0, len(g.nodes))
	for _, n := range g.nodes {
		result = append(result, n)
	}
	return result
}
