package agentfabric

import "errors"

// Resource admission control : the Kernel validates
// resource quotas at spawn — a syscall-style gate. The Fabric keeps a named
// budget (e.g. {"cpu": 8, "memory": 4096}) and rejects a spawn whose requested
// resources would exceed the remaining quota. Claims are released when the
// agent is killed or retired, so the budget reflects live agents only.
//
// Only resource keys present in the budget are enforced; resources without a
// budget entry are carried as hints (pre-P5 behavior) and never rejected.
// Non-numeric hint values (e.g. {"gpu": "a100"}) are ignored for accounting.

// ErrResourceQuotaExceeded is returned by Spawn when the requested resources
// exceed the Fabric's remaining quota.
var ErrResourceQuotaExceeded = errors.New("agentfabric: resource quota exceeded")

// WithResourceBudget sets the total allocatable resources (name → max amount).
// A nil or empty budget disables resource admission control (backward
// compatible: spawns without a budget never fail on resources). The budget is
// copied, so later mutation of the caller's map has no effect.
//
// Args:
//   - budget: resource name → maximum total amount allocatable across all
//     live agents.
//
// Returns:
//   - *Fabric: the fabric for chaining.
func (f *Fabric) WithResourceBudget(budget map[string]float64) *Fabric {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resourceBudget = make(map[string]float64, len(budget))
	for name, max := range budget {
		f.resourceBudget[name] = max
	}
	return f
}

// UpdateResourceBudget dynamically replaces the resource budget (v0.3.0 M2-2:
// evolution-driven resource allocation — the Evolution system adjusts CPU /
// memory quota weights at runtime without recreating the fabric). Existing
// allocations are NOT retroactively rejected: the new budget applies to
// future spawn admission checks. A nil or empty map disables enforcement.
//
// Args:
//   - budget: the new resource budget (name → max amount).
func (f *Fabric) UpdateResourceBudget(budget map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resourceBudget = make(map[string]float64, len(budget))
	for name, max := range budget {
		f.resourceBudget[name] = max
	}
}

// parseResourceClaim converts a SpawnSpec.Resources map into a numeric claim.
// Numeric values are kept; non-numeric values are hints and ignored. Caller
// does not need the lock — the input map is caller-owned.
func parseResourceClaim(resources map[string]any) map[string]float64 {
	if len(resources) == 0 {
		return nil
	}
	claim := make(map[string]float64, len(resources))
	for name, v := range resources {
		switch n := v.(type) {
		case float64:
			claim[name] = n
		case int:
			claim[name] = float64(n)
		case int64:
			claim[name] = float64(n)
		}
	}
	return claim
}

// canAllocateLocked reports whether claim fits in the remaining budget.
// Caller must hold f.mu. A claim for a resource not in the budget is always
// allowed (no quota configured for it).
func (f *Fabric) canAllocateLocked(claim map[string]float64) bool {
	for name, amount := range claim {
		max, budgeted := f.resourceBudget[name]
		if !budgeted {
			continue
		}
		if f.allocated[name]+amount > max {
			return false
		}
	}
	return true
}

// allocateLocked adds claim to the allocated totals. Caller must hold f.mu and
// must have verified canAllocateLocked first.
func (f *Fabric) allocateLocked(claim map[string]float64) {
	for name, amount := range claim {
		if amount <= 0 {
			continue
		}
		f.allocated[name] += amount
	}
}

// releaseLocked removes claim from the allocated totals (kill / retire). Caller
// must hold f.mu. Idempotent: a nil claim (already released or never claimed)
// is a no-op.
func (f *Fabric) releaseLocked(claim map[string]float64) {
	for name, amount := range claim {
		if amount <= 0 {
			continue
		}
		f.allocated[name] -= amount
		if f.allocated[name] < 0 {
			f.allocated[name] = 0
		}
	}
}
