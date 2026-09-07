package taskfabric

import "time"

// Lease is a TTL-based ownership lease (abstracted from SessionLease — design
// §3). The same shape serves TaskLease / ResourceLease / CapabilityLease.
type Lease struct {
	// Owner is the agent identity holding the lease.
	Owner string
	// ExpiresAt is the wall-clock expiry; a lease is renewable before expiry.
	ExpiresAt time.Time
	// Epoch is bumped on every acquisition, making stale renews observable.
	Epoch uint64
}

// NewLease creates a lease for owner with a ttl duration.
func NewLease(owner string, ttl time.Duration, epoch uint64) Lease {
	return Lease{Owner: owner, ExpiresAt: time.Now().Add(ttl), Epoch: epoch}
}

// IsExpired reports whether the lease has passed its expiry.
func (l Lease) IsExpired(now time.Time) bool {
	return !l.ExpiresAt.After(now)
}
