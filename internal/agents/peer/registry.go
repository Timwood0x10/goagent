// Package peer provides peer-to-peer agent messaging primitives: a registry
// that maps agent IDs to send functions and a direct-delivery Send path, so
// agents can exchange messages without routing through the leader (primitive
// 2: peer-to-peer agent communication).
//
// The registry is the discovery surface (sub-agent registry + capability
// discovery): an agent registers its delivery function under its ID, and any
// other agent can look the ID up and send directly. This complements — and
// does not replace — the existing leader-dispatched task path
// (dispatcher.dispatchViaEvents).
package peer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// SendFunc delivers a message to one agent. Implementations enqueue onto the
// target's message queue or call its handler synchronously.
type SendFunc func(ctx context.Context, msg *ahp.AHPMessage) error

// Registry maps agent IDs to their delivery functions and provides direct
// peer-to-peer Send. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	peers map[string]SendFunc
}

// NewRegistry creates an empty peer registry.
func NewRegistry() *Registry {
	return &Registry{
		peers: make(map[string]SendFunc),
	}
}

// Register associates a delivery function with an agent ID. Registering an
// already-known ID replaces the previous function (idempotent re-registration
// for restart/resurrection).
func (r *Registry) Register(agentID string, send SendFunc) error {
	if agentID == "" {
		return errors.New("peer: agent ID must not be empty")
	}
	if send == nil {
		return errors.New("peer: send function must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[agentID] = send
	return nil
}

// Unregister removes an agent from the registry. It is a no-op for unknown IDs.
func (r *Registry) Unregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peers, agentID)
}

// Lookup returns the delivery function for an agent, if registered.
func (r *Registry) Lookup(agentID string) (SendFunc, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.peers[agentID]
	return fn, ok
}

// IDs returns the sorted list of registered agent IDs.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.peers))
	for id := range r.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Send delivers a message directly to the target agent's registered send
// function. It stamps TargetAgent onto a COPY of the message before delivery
// so the receiver can attribute it without trusting the sender, and the
// caller's message (which may be shared concurrently) is never mutated.
func (r *Registry) Send(ctx context.Context, targetID string, msg *ahp.AHPMessage) error {
	fn, ok := r.Lookup(targetID)
	if !ok {
		return fmt.Errorf("peer: agent %q not registered", targetID)
	}
	if msg == nil {
		return errors.New("peer: message must not be nil")
	}
	stamped := msg.Clone()
	stamped.TargetAgent = targetID
	return fn(ctx, stamped)
}
