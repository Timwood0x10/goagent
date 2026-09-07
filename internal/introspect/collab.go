// Package introspect — collaboration read-model.
//
// Multi-agent runtimes produce collaboration the same way a real team does:
// one agent finishes a piece of work and hands it to another (review, test,
// synthesis). This file defines the read-model the panel renders as the
// collaboration graph — a bounded record of real agent→agent IPC edges
// (who asked whom, about which task). The edges are recorded by the runtime
// (demo or serve) from the agentipc bus, not fabricated by the panel.
package introspect

import (
	"sync"
	"time"
)

// CollabEdge is one directed collaboration record: agent From handed work
// (or a message) about TaskID to agent To at TS. Multiple edges between the
// same pair are collapsed in the UI; the graph layout reads them as one line
// with an activity count.
type CollabEdge struct {
	// From is the sender agent id.
	From string `json:"from"`
	// To is the receiver agent id.
	To string `json:"to"`
	// Topic is the IPC topic (e.g. "handoff-review", "verify-conclusion").
	Topic string `json:"topic"`
	// TaskID is the task the collaboration was about (may be empty for
	// fire-and-forget peer chatter).
	TaskID string `json:"task_id,omitempty"`
	// TS is when the collaboration happened.
	TS time.Time `json:"ts"`
}

// CollabReporter is a bounded, lock-guarded record of collaboration edges. A
// single instance is created per runtime and handed to both the agent IPC
// layer (to record edges) and the panel collector (to read them). Bounded so
// a long-running process never grows the graph without bound — old edges are
// evicted first (the panel only needs a recent collaboration window).
type CollabReporter struct {
	mu     sync.Mutex
	edges  []CollabEdge
	latest time.Time
}

// maxCollabEdges bounds the recorded collaboration ring.
const maxCollabEdges = 200

// NewCollabReporter builds an empty collaboration recorder.
func NewCollabReporter() *CollabReporter { return &CollabReporter{} }

// Record appends one directed collaboration edge (newest last).
//
// Graph semantics: edges are a BIPARTITE agent↔task projection —
// From is the acting agent, To is EITHER another agent (direct IPC) or a task
// id (the work the event was about; the Sink projects lifecycle/task events
// here). The panel renders both node kinds; Topic disambiguates the edge
// origin (ipc topic vs event type).
func (c *CollabReporter) Record(e CollabEdge) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edges = append(c.edges, e)
	c.latest = e.TS
	if len(c.edges) > maxCollabEdges {
		c.edges = c.edges[len(c.edges)-maxCollabEdges:]
	}
}

// Edges returns a copy of the recorded edges (oldest first), so the panel
// renders a stable graph per refresh instead of an ever-shifting tail.
func (c *CollabReporter) Edges() []CollabEdge {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CollabEdge, len(c.edges))
	copy(out, c.edges)
	return out
}

// Snapshot returns the current collaboration view: the deduplicated edge
// graph (pair + topic + latest time + count) plus the raw edge tail for the
// activity list. Implements the panel's collab Source contract.
func (c *CollabReporter) Snapshot() CollabSnapshot {
	c.mu.Lock()
	edges := make([]CollabEdge, len(c.edges))
	copy(edges, c.edges)
	latest := c.latest
	c.mu.Unlock()
	if len(edges) == 0 {
		return CollabSnapshot{}
	}
	// Collapse repeated pairs into one graph edge with an activity count.
	pairs := make(map[string]*CollabEdge)
	order := make([]string, 0, len(edges))
	for _, e := range edges {
		key := e.From + "\x00" + e.To + "\x00" + e.Topic
		if _, ok := pairs[key]; !ok {
			cloned := e
			pairs[key] = &cloned
			order = append(order, key)
			continue
		}
		// Keep the latest edge's timestamp and task id for the collapsed
		// edge (the graph shows the most recent collaboration on the pair).
		// A newer edge with an empty task id does not erase a known one.
		if e.TS.After(pairs[key].TS) {
			pairs[key].TS = e.TS
			if e.TaskID != "" {
				pairs[key].TaskID = e.TaskID
			}
		} else if e.TaskID != "" && pairs[key].TaskID == "" {
			pairs[key].TaskID = e.TaskID
		}
	}
	graph := make([]CollabEdge, 0, len(order))
	for _, key := range order {
		graph = append(graph, *pairs[key])
	}
	return CollabSnapshot{Graph: graph, Recent: edges, Latest: latest}
}

// CollabSnapshot is one collaboration frame: the collapsed graph (for the
// node-edge rendering) and the recent raw tail (for the activity list).
type CollabSnapshot struct {
	// Graph is the deduplicated agent→agent edge set (pair + topic + latest
	// TS). The UI draws one line per entry.
	Graph []CollabEdge `json:"graph"`
	// Recent is the most recent raw edges, newest LAST (append order), used
	// for the "what just happened" list.
	Recent []CollabEdge `json:"recent"`
	// Latest is the most recent collaboration timestamp.
	Latest time.Time `json:"latest"`
}
