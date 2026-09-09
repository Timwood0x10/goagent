// Package lease implements session leasing for concurrent access control
// (ares-vs-prime-agent 5.3, medium-high priority). A lease is an exclusive
// hold on a session owned by one agent/daemon for a bounded TTL; other
// workers cannot modify the session while the lease is held. This prevents
// concurrent writers from clobbering each other on long-lived sessions.
// (The action store it once paired with for replay/audit,
// agents/actionlog, was removed as dead — it had zero production
// constructors.)
package lease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sentinel errors for lease operations.
var (
	// ErrLeaseHeld is returned by Acquire when the session is already leased.
	ErrLeaseHeld = errors.New("lease: session already leased")
	// ErrLeaseNotFound is returned when releasing/renewing an unknown lease.
	ErrLeaseNotFound = errors.New("lease: no lease for session")
	// ErrLeaseOwnerMismatch is returned when the caller is not the lease owner.
	ErrLeaseOwnerMismatch = errors.New("lease: caller does not own the lease")
)

// Lease is an exclusive, expiring hold on a session.
type Lease struct {
	// SessionID is the leased session.
	SessionID string
	// Owner identifies the worker holding the lease.
	Owner string
	// ExpiresAt is when the lease expires (soft bound; see Manager).
	ExpiresAt time.Time
}

// Manager issues and tracks session leases in memory. It is safe for
// concurrent use; all state is guarded by mu.
type Manager struct {
	mu        sync.Mutex
	leases    map[string]Lease
	timeNow   func() time.Time // clock injection for tests
	sweepTick int              // periodic full-prune counter
}

// NewManager creates a lease Manager with the system clock.
func NewManager() *Manager {
	return &Manager{
		leases:  make(map[string]Lease),
		timeNow: time.Now,
	}
}

// Acquire takes an exclusive lease on sessionID for owner, valid for ttl.
// It fails with ErrLeaseHeld when the session is already leased by another
// owner (renewing your own lease is not allowed — call Renew instead).
func (m *Manager) Acquire(ctx context.Context, sessionID, owner string, ttl time.Duration) (Lease, error) {
	if sessionID == "" || owner == "" {
		return Lease{}, errors.New("lease: session ID and owner must not be empty")
	}
	if ttl <= 0 {
		return Lease{}, errors.New("lease: ttl must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.leases[sessionID]; ok && existing.ExpiresAt.After(m.timeNow()) {
		return Lease{}, fmt.Errorf("%w: %s held by %s", ErrLeaseHeld, sessionID, existing.Owner)
	}
	l := Lease{
		SessionID: sessionID,
		Owner:     owner,
		ExpiresAt: m.timeNow().Add(ttl),
	}
	m.leases[sessionID] = l
	return l, nil
}

// Renew extends an existing lease owned by owner by ttl.
func (m *Manager) Renew(ctx context.Context, sessionID, owner string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("lease: ttl must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	l, ok := m.leases[sessionID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrLeaseNotFound, sessionID)
	}
	if l.Owner != owner {
		return fmt.Errorf("%w: %s held by %s", ErrLeaseOwnerMismatch, sessionID, l.Owner)
	}
	// An expired lease is dead: renewing it would resurrect a hold the
	// manager has already given up. Callers must Acquire again.
	if l.ExpiresAt.Before(m.timeNow()) {
		delete(m.leases, sessionID)
		return fmt.Errorf("%w: %s expired at %s", ErrLeaseNotFound, sessionID, l.ExpiresAt.Format(time.RFC3339))
	}
	l.ExpiresAt = m.timeNow().Add(ttl)
	m.leases[sessionID] = l
	return nil
}

// Release surrenders the lease on sessionID. Only the owner may release.
func (m *Manager) Release(ctx context.Context, sessionID, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	l, ok := m.leases[sessionID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrLeaseNotFound, sessionID)
	}
	if l.Owner != owner {
		return fmt.Errorf("%w: %s held by %s", ErrLeaseOwnerMismatch, sessionID, l.Owner)
	}
	delete(m.leases, sessionID)
	return nil
}

// sweepEvery bounds the periodic full prune: expired leases are swept
// either when Count is called or, probabilistically, on a Get miss, so
// abandoned expired keys cannot accumulate unboundedly in a long-lived
// process that only ever reads via Get/Held.
const sweepEvery = 1024

// Get returns the current lease for sessionID, if unexpired.
func (m *Manager) Get(sessionID string) (Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.leases[sessionID]
	if !ok {
		// Periodic probabilistic full sweep: bounds abandoned expired leases
		// even when Count is never called.
		m.sweepTick++
		if m.sweepTick >= sweepEvery {
			m.sweepTick = 0
			m.pruneExpiredLocked()
		}
		return Lease{}, false
	}
	if l.ExpiresAt.Before(m.timeNow()) {
		// Lazy cleanup: drop the expired lease on first touch.
		delete(m.leases, sessionID)
		return Lease{}, false
	}
	return l, true
}

// Held reports whether sessionID currently has an unexpired lease.
func (m *Manager) Held(sessionID string) bool {
	_, ok := m.Get(sessionID)
	return ok
}

// Count returns the number of active leases, pruning expired entries first:
// the map had no sweep, so abandoned sessions accumulated forever and
// Count stayed inflated long after every lease expired.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked()
	return len(m.leases)
}

// pruneExpiredLocked deletes expired leases from the map (caller holds mu).
func (m *Manager) pruneExpiredLocked() {
	now := m.timeNow()
	for id, l := range m.leases {
		if l.ExpiresAt.Before(now) {
			delete(m.leases, id)
		}
	}
}
