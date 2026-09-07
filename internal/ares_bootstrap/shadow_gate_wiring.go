// shadow_gate_wiring.go decides whether the PRODUCTION G2 shadow gate is
// registered. It mirrors eval_gate_wiring.go's
// honest-absence contract: an absent gate is reported, never silently
// substituted with a pass-through pretending to be verification.
//
// The safety invariant this file encodes:
//
//	Skipping PRE-deployment verification is allowed only when
//	POST-deployment verification is armed.
//
// Concretely — no independent scorer means no shadow evidence can exist, so
// a registered G2 would reject every candidate forever (measured: only
// the seed strategy ever promotes, and asm.Previous() stays nil so
// automatic rollback is ALSO unreachable). In that state the honest options
// are (a) let canary + rollback carry the risk, or (b) refuse to promote at
// all — which one applies depends on whether rollback is actually armed,
// never on a default picked for the operator.
package ares_bootstrap

import (
	"errors"
)

// errShadowGateNotConfigured mirrors errEvalGateNotConfigured: an absent gate
// is reported, never silently substituted with a pass-through. Reserved so
// future callers can distinguish "intentionally absent" from "wiring bug"
// with errors.Is.
var errShadowGateNotConfigured = errors.New("bootstrap: shadow gate not configured")

// shadowGateMode decides whether the G2 shadow gate is registered.
//
// Three branches (exactly one per verification posture):
//
//	hasScorer | rollbackArmed | register | reason
//	---------+---------------+----------+------------------------------------
//	true      | either        | true     | "independent scorer wired" — the
//	          |               |          | pre-deployment gate is REAL.
//	false     | true          | false    | no scorer; canary+rollback armed —
//	          |               |          | promotion is reversible, so the G2
//	          |               |          | gate is honestly absent instead of
//	          |               |          | being a permanent wall.
//	false     | false         | true     | no scorer AND rollback disarmed —
//	          |               |          | neither verification exists, so the
//	          |               |          | gate stays fail-closed: refusing
//	          |               |          | promotion is the only correct answer.
//
// The third row is the safety floor: "no gate" must never be a state the
// system silently falls into. The intentionally-absent branch carries
// errShadowGateNotConfigured so callers can distinguish it from a decision
// error with errors.Is — the same contract as buildEvalGate.
//
// Args:
//   - hasScorer: whether an independent (non-heuristic) scorer is wired on
//     the shadow evaluator, i.e. whether shadow comparisons can exist at all.
//   - rollbackArmed: whether the post-deployment automatic rollback is
//     enabled (evolution.rollback.enabled, tri-state default true).
//
// Returns:
//   - register: whether the G2 shadow gate should be registered.
//   - reason: the fixed-vocabulary reason string for logs, the gate-skipped
//     metric and the lifecycle snapshot.
//   - err: errShadowGateNotConfigured when the gate is intentionally absent;
//     nil otherwise.
func shadowGateMode(hasScorer, rollbackArmed bool) (register bool, reason string, err error) {
	switch {
	case hasScorer:
		return true, "independent scorer wired", nil
	case rollbackArmed:
		return false, "no scorer; canary+rollback armed", errShadowGateNotConfigured
	default:
		return true, "no scorer and rollback disarmed — fail-closed", nil
	}
}
