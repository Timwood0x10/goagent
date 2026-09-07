package flight

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// NodeType classifies a graph node.
type NodeType string

const (
	NodeAgent NodeType = "agent"
	NodeTool  NodeType = "tool"
	NodeLLM   NodeType = "llm"
)

// NodeStatus represents the current state of a graph node.
type NodeStatus string

const (
	StatusRunning   NodeStatus = "running"
	StatusCompleted NodeStatus = "completed"
	StatusFailed    NodeStatus = "failed"
)

// GraphNode represents a single node in the call graph.
type GraphNode struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parent_id,omitempty"`
	Type     NodeType       `json:"type"`
	Name     string         `json:"name"`
	Status   NodeStatus     `json:"status"`
	StartAt  time.Time      `json:"start_at"`
	EndAt    time.Time      `json:"end_at,omitempty"`
	Duration time.Duration  `json:"duration"`
	Children []*GraphNode   `json:"children,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Graph represents an agent call graph — a tree of agents, tools, and LLM calls.
type Graph struct {
	root            *GraphNode
	nodes           map[string]*GraphNode
	pendingChildren map[string][]*GraphNode
	mu              sync.RWMutex
	cap             int
}

// maxGraphNodes is the ring cap for graph nodes, aligned with
// introspect's 300-entry default.
const maxGraphNodes = 300

// NewGraph creates an empty graph.
func NewGraph() *Graph {
	return &Graph{
		nodes:           make(map[string]*GraphNode),
		pendingChildren: make(map[string][]*GraphNode),
		cap:             maxGraphNodes,
	}
}

// AddNode adds a node to the graph. If ParentID is set, it becomes a child of the parent.
func (g *Graph) AddNode(node *GraphNode) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if node == nil {
		return
	}

	g.nodes[node.ID] = node

	// B7: if this node has pending children from earlier arrivals,
	// attach them now.
	if pending, ok := g.pendingChildren[node.ID]; ok {
		node.Children = append(node.Children, pending...)
		delete(g.pendingChildren, node.ID)
	}

	// P1-2: ring cap — evict the oldest node when the cap is exceeded.
	// The oldest node is the one with the earliest StartAt. We evict it
	// from the nodes map only (its children entries remain in the tree
	// so the structural shape is not broken — only the lookup is lost).
	if g.cap > 0 && len(g.nodes) > g.cap {
		var oldestID string
		var oldestStart time.Time
		for id, n := range g.nodes {
			if oldestID == "" || n.StartAt.Before(oldestStart) {
				oldestID = id
				oldestStart = n.StartAt
			}
		}
		if oldestID != "" && oldestID != node.ID {
			delete(g.nodes, oldestID)
		}
	}

	if node.ParentID == "" {
		g.root = node
		return
	}

	// Guard against self-parenting: a node whose ParentID equals its own ID
	// would become its own child, creating a cycle that makes the recursive
	// traversals (Depth, ExportMermaid, ExportDOT) recurse forever and
	// overflow the stack (M12).
	if node.ParentID == node.ID {
		return
	}

	if parent, ok := g.nodes[node.ParentID]; ok {
		parent.Children = append(parent.Children, node)
	} else {
		// B7: parent has not arrived yet — record a pending child so
		// the parent can pick it up when it is added later (out-of-order
		// event arrival is common in the flight recorder).
		g.pendingChildren[node.ParentID] = append(g.pendingChildren[node.ParentID], node)
	}
}

// GetNode returns a node by ID.
func (g *Graph) GetNode(id string) (*GraphNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n, ok := g.nodes[id]
	return n, ok
}

// UpdateNodeStatus atomically updates the status, end time, and duration of a node.
// Duration is computed from the node's StartAt field under the write lock,
// avoiding the data race of calling GetNode (read lock) then mutating fields
// outside the lock (P0-2).
func (g *Graph) UpdateNodeStatus(id string, status NodeStatus, endAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n, ok := g.nodes[id]; ok {
		n.Status = status
		n.EndAt = endAt
		n.Duration = endAt.Sub(n.StartAt)
	}
}

// Root returns the root node.
func (g *Graph) Root() *GraphNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.root
}

// Nodes returns all nodes.
func (g *Graph) Nodes() []*GraphNode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]*GraphNode, 0, len(g.nodes))
	for _, n := range g.nodes {
		result = append(result, n)
	}
	return result
}

// Depth returns the maximum depth of the tree.
func (g *Graph) Depth() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.root == nil {
		return 0
	}
	return nodeDepth(g.root, 0)
}

func nodeDepth(n *GraphNode, current int) int {
	return nodeDepthVisited(n, current, make(map[string]bool))
}

// nodeDepthVisited is nodeDepth with cycle detection: a node already visited
// on the current path (a cycle in Children) stops recursion instead of
// overflowing the stack (M12).
func nodeDepthVisited(n *GraphNode, current int, visited map[string]bool) int {
	if n == nil || len(n.Children) == 0 {
		return current
	}
	if visited[n.ID] {
		return current
	}
	visited[n.ID] = true
	maxChild := current
	for _, c := range n.Children {
		if c == nil {
			continue
		}
		if d := nodeDepthVisited(c, current+1, visited); d > maxChild {
			maxChild = d
		}
	}
	return maxChild
}

// ExportMermaid renders the graph as a Mermaid flowchart.
func (g *Graph) ExportMermaid() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.root == nil {
		return "graph LR\n    empty[No data]"
	}

	var b strings.Builder
	b.WriteString("graph LR\n")

	g.writeMermaidNode(&b, g.root, "    ")
	return b.String()
}

func (g *Graph) writeMermaidNode(b *strings.Builder, n *GraphNode, indent string) {
	g.writeMermaidNodeVisited(b, n, indent, make(map[string]bool))
}

// writeMermaidNodeVisited is writeMermaidNode with cycle detection so a
// cyclic Children graph cannot recurse forever (M12).
func (g *Graph) writeMermaidNodeVisited(b *strings.Builder, n *GraphNode, indent string, visited map[string]bool) {
	if n == nil || visited[n.ID] {
		return
	}
	visited[n.ID] = true
	icon := nodeIcon(n.Type)
	label := fmt.Sprintf("%s%s %s", icon, n.Name, statusEmoji(n.Status))
	nodeID := sanitizeID(n.ID)
	fmt.Fprintf(b, "%s%s[\"%s\"]\n", indent, nodeID, label)

	for _, child := range n.Children {
		if child == nil {
			continue
		}
		childID := sanitizeID(child.ID)
		fmt.Fprintf(b, "%s%s --> %s\n", indent, nodeID, childID)
		g.writeMermaidNodeVisited(b, child, indent, visited)
	}
}

func nodeIcon(t NodeType) string {
	switch t {
	case NodeAgent:
		return "🤖 "
	case NodeTool:
		return "🔧 "
	case NodeLLM:
		return "🧠 "
	default:
		return ""
	}
}

func statusEmoji(s NodeStatus) string {
	switch s {
	case StatusRunning:
		return "⏳"
	case StatusCompleted:
		return "✅"
	case StatusFailed:
		return "❌"
	default:
		return ""
	}
}

func sanitizeID(id string) string {
	return strings.ReplaceAll(id, "-", "_")
}

// ExportDOT renders the graph as a Graphviz DOT diagram.
func (g *Graph) ExportDOT() string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if g.root == nil {
		return "digraph {}"
	}

	var b strings.Builder
	b.WriteString("digraph AgentCallGraph {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, style=rounded];\n")

	g.writeDOTNode(&b, g.root)
	b.WriteString("}\n")
	return b.String()
}

func (g *Graph) writeDOTNode(b *strings.Builder, n *GraphNode) {
	g.writeDOTNodeVisited(b, n, make(map[string]bool))
}

// writeDOTNodeVisited is writeDOTNode with cycle detection so a cyclic
// Children graph cannot recurse forever (M12).
func (g *Graph) writeDOTNodeVisited(b *strings.Builder, n *GraphNode, visited map[string]bool) {
	if n == nil || visited[n.ID] {
		return
	}
	visited[n.ID] = true
	color := nodeColor(n.Status)
	fmt.Fprintf(b, "  \"%s\" [label=\"%s\\n%s\", fillcolor=\"%s\", style=\"rounded,filled\"];\n",
		n.ID, string(n.Type), n.Name, color)

	for _, child := range n.Children {
		if child == nil {
			continue
		}
		fmt.Fprintf(b, "  \"%s\" -> \"%s\";\n", n.ID, child.ID)
		g.writeDOTNodeVisited(b, child, visited)
	}
}

func nodeColor(s NodeStatus) string {
	switch s {
	case StatusRunning:
		return "#FFF3CD"
	case StatusCompleted:
		return "#D4EDDA"
	case StatusFailed:
		return "#F8D7DA"
	default:
		return "#E2E3E5"
	}
}

// ExportJSON serializes the graph as JSON.
func (g *Graph) ExportJSON() ([]byte, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return json.MarshalIndent(g.root, "", "  ")
}
