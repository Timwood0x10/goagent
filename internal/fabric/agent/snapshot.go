package agentfabric

import (
	"sync"
	"time"
)

// AgentSnapshot is the revival record captured when an agent dies with live
// cognition (fusion plan Phase A1). It carries everything the recovery
// subsystem needs to revive an EQUIVALENT execution body in place:
//
//   - Cognitive: the agent's last CognitiveState, already carrying its
//     SchemaVersion (the versioned-envelope requirement of code_rules
//     — a bare map is forbidden here).
//   - Capabilities: the declared capability set, so a revived body matches
//     the scheduler's candidate scoring exactly as the dead one did.
//   - Parent: provenance continuity — the revived agent keeps pointing at
//     its origin (Rule 2: spawn establishes provenance, never hierarchy).
type AgentSnapshot struct {
	Cognitive    CognitiveState
	Capabilities []string
	Parent       string
	// DiedAt is the fabric-clock instant the death was captured. When several
	// dead agents share a capability, recovery picks the MOST RECENT death —
	// the freshest cognition is the safest revival seed (A2 review fix:
	// map-iteration order must not choose arbitrarily).
	DiedAt time.Time
}

// snapshotStore keeps the LAST snapshot per agent identity. Entries are
// cleared on Retire (terminal: no revival possible) and are meant to be
// consumed via ClearSnapshot after a successful in-place revival, keeping
// long-running processes bounded.
type snapshotStore struct {
	mu   sync.RWMutex
	byID map[string]AgentSnapshot
}

func newSnapshotStore() *snapshotStore {
	return &snapshotStore{byID: make(map[string]AgentSnapshot)}
}

// save stores (or overwrites) the snapshot for agentID.
func (s *snapshotStore) save(id string, snap AgentSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[id] = snap
}

// load returns the stored snapshot for agentID.
func (s *snapshotStore) load(id string) (AgentSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.byID[id]
	return snap, ok
}

// clear drops the snapshot for agentID (consumed by revival, or terminal
// Retire).
func (s *snapshotStore) clear(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
}

// findByCapability returns the first stored snapshot whose declared
// capabilities contain capability. Scan cost is bounded by the live-agent
// population (entries are cleared on consume/Retire), so linear scan is fine.
func (s *snapshotStore) findByCapability(capability string) (string, AgentSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	bestID := ""
	var best AgentSnapshot
	for id, snap := range s.byID {
		matches := false
		for _, c := range snap.Capabilities {
			if c == capability {
				matches = true
				break
			}
		}
		if !matches {
			continue
		}
		if bestID == "" || snap.DiedAt.After(best.DiedAt) {
			bestID, best = id, snap
		}
	}
	if bestID == "" {
		return "", AgentSnapshot{}, false
	}
	return bestID, best, true
}

// captureFromAgent copies the revivable facts off an agent about to be
// removed by Kill. Callers must hold Fabric.mu; the cognitive field itself is
// additionally guarded by the agent's own lock, so we take it briefly to get
// a consistent copy (code_rules: shared-state ownership documented at
// the field).
func captureFromAgent(a *Agent, diedAt time.Time) AgentSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cog := a.cognitive
	if cog.SchemaVersion == 0 {
		cog.SchemaVersion = CognitiveStateSchemaVersion
	}
	caps := append([]string(nil), a.Capabilities...)
	return AgentSnapshot{Cognitive: cog, Capabilities: caps, Parent: a.Parent, DiedAt: diedAt}
}

// LastSnapshot returns the snapshot captured when agentID died, if any.
// The recovery subsystem calls this before deciding between in-place revival
// and spawning a replacement (fusion plan A2 arbitration rule).
func (f *Fabric) LastSnapshot(agentID string) (AgentSnapshot, bool) {
	return f.snapshots.load(agentID)
}

// ClearSnapshot drops the death snapshot for agentID. The recovery subsystem
// calls it after a successful in-place revival so a later death cannot reuse
// stale cognition; Retire also clears it (terminal state, no revival).
func (f *Fabric) ClearSnapshot(agentID string) {
	f.snapshots.clear(agentID)
}

// FindRevivableSnapshot returns a dead agent (id + snapshot) whose declared
// capabilities cover capability — the A2 arbitration input: if such a victim
// exists and the restart budget allows, the recovery subsystem revives it in
// place instead of spawning a generic replacement.
func (f *Fabric) FindRevivableSnapshot(capability string) (string, AgentSnapshot, bool) {
	return f.snapshots.findByCapability(capability)
}
