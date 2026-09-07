package eval

import (
	"fmt"
)

// ProcessCheck is a single middle-layer rule check outcome.
// It is derived from the execution trace (ares_events) and verifies whether
// the task was completed in an allowed manner (Ch.8: the middle verification
// layer answers "was it done in the allowed way").
type ProcessCheck struct {
	// Rule identifies the rule, permission boundary, or action-sequence
	// invariant being checked, e.g. "no_pii_leak", "permission_scope",
	// "action_order".
	Rule string

	// Allowed is true when the action complied with the rule.
	Allowed bool

	// Detail carries the observed behavior for auditability.
	Detail string
}

// ProcessVerifier implements the middle verification layer (Ch.8): it checks
// rules, permission boundaries, and action sequences recorded in the event
// trace to answer "was the task done in an allowed way". It is deterministic
// and does not call an LLM.
type ProcessVerifier struct{}

// NewProcessVerifier creates a middle-layer process verifier.
func NewProcessVerifier() *ProcessVerifier {
	return &ProcessVerifier{}
}

// Verify evaluates rule checks and produces structured Evidence with Source
// "process_verifier".
//
// Verdict derivation:
//   - no checks                       -> VerdictUncertain (nothing to prove)
//   - all checks allowed              -> VerdictPass
//   - at least one check disallowed   -> VerdictFail
//
// Args:
//
//	taskID - the task being evaluated.
//	role - the agent role whose execution is assessed.
//	checks - rule compliance outcomes; nil is treated as empty.
//
// Returns:
//
//	evidence - the structured diagnosis; never nil.
func (v *ProcessVerifier) Verify(taskID, role string, checks []ProcessCheck) *Evidence {
	ev := NewEvidence(taskID, role, "process_verifier")
	if len(checks) == 0 {
		ev.Verdict = VerdictUncertain
		ev.Confidence = 0
		ev.Flag = "no process checks provided"
		return ev
	}

	items := make([]EvidenceItem, 0, len(checks))
	violations := 0
	for _, check := range checks {
		status := StatusPassed
		if !check.Allowed {
			status = StatusFailed
			violations++
		}
		items = append(items, EvidenceItem{
			Type:   "rule_check",
			Name:   check.Rule,
			Status: status,
			Detail: check.Detail,
		})
	}

	pass := violations == 0
	// The dimension pass flag is set explicitly (all rules must be allowed)
	// instead of relying on the 2/3 threshold, because process rules are hard
	// constraints: any violation means the execution was not permitted.
	ev.Dimensions = append(ev.Dimensions, DimensionScore{
		Name:     "rule_compliance",
		Score:    len(checks) - violations,
		Max:      len(checks),
		Pass:     pass,
		Evidence: items,
	})

	switch {
	case violations > 0:
		ev.Verdict = VerdictFail
		ev.Confidence = float64(len(checks)-violations) / float64(len(checks))
		if ev.Flag == "" {
			ev.Flag = fmt.Sprintf("%d process rule(s) violated", violations)
		}
	default:
		ev.Verdict = VerdictPass
		ev.Confidence = 1
	}
	return ev
}
