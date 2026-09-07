package eval

import (
	"fmt"
)

// Check status constants shared by the deterministic verifiers.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusMissing = "missing"
	StatusSkipped = "skipped"
)

// ResultCheck is a single deterministic bottom-layer check outcome.
// It is produced by tests, tool returns, or environment state probes and does
// not depend on an LLM (Ch.8: the bottom verification layer must anchor on
// ground truth).
type ResultCheck struct {
	// Name identifies the check, e.g. "unit_test_auth".
	Name string

	// Status is the outcome: "passed" / "failed" / "missing" / "skipped".
	Status string

	// Detail carries the actual output for auditability.
	Detail string
}

// ResultVerifier implements the bottom verification layer (Ch.8): it answers
// "was the task actually accomplished" using deterministic ground truth such
// as test results, tool return values, and environment state. It never calls
// an LLM.
type ResultVerifier struct{}

// NewResultVerifier creates a bottom-layer result verifier.
func NewResultVerifier() *ResultVerifier {
	return &ResultVerifier{}
}

// Verify evaluates deterministic result checks and produces structured
// Evidence with Source "result_verifier".
//
// Verdict derivation:
//   - all checks passed                 -> VerdictPass
//   - at least one check explicitly failed -> VerdictFail
//   - no failure but missing/skipped    -> VerdictUncertain (cannot prove success)
//   - empty checks                      -> VerdictUncertain
//
// Args:
//
//	taskID - the task being evaluated.
//	role - the agent role whose execution is assessed.
//	checks - deterministic check outcomes; nil is treated as empty.
//
// Returns:
//
//	evidence - the structured diagnosis; never nil.
func (v *ResultVerifier) Verify(taskID, role string, checks []ResultCheck) *Evidence {
	ev := NewEvidence(taskID, role, "result_verifier")
	if len(checks) == 0 {
		ev.Verdict = VerdictUncertain
		ev.Confidence = 0
		ev.Flag = "no result checks provided"
		return ev
	}

	items := make([]EvidenceItem, 0, len(checks))
	failedCount := 0
	indeterminateCount := 0
	for _, check := range checks {
		status := check.Status
		if status == "" {
			status = StatusMissing
		}
		items = append(items, EvidenceItem{
			Type:   "result_check",
			Name:   check.Name,
			Status: status,
			Detail: check.Detail,
		})
		switch status {
		case StatusFailed:
			failedCount++
		case StatusMissing, StatusSkipped:
			indeterminateCount++
		}
	}

	pass := failedCount == 0 && indeterminateCount == 0
	// The dimension pass flag is set explicitly (every check must pass)
	// instead of relying on the 2/3 threshold, because the bottom layer must
	// prove the task truly succeeded: any failure means it did not.
	ev.Dimensions = append(ev.Dimensions, DimensionScore{
		Name:     "task_result",
		Score:    len(checks) - failedCount,
		Max:      len(checks),
		Pass:     pass,
		Evidence: items,
	})

	switch {
	case failedCount > 0:
		ev.Verdict = VerdictFail
		ev.Confidence = confidenceFor(len(checks), failedCount)
		if ev.Flag == "" {
			ev.Flag = fmt.Sprintf("%d result check(s) failed", failedCount)
		}
	case indeterminateCount > 0:
		ev.Verdict = VerdictUncertain
		ev.Confidence = confidenceFor(len(checks), failedCount)
		if ev.Flag == "" {
			ev.Flag = fmt.Sprintf("%d check(s) missing or skipped", indeterminateCount)
		}
	default:
		ev.Verdict = VerdictPass
		ev.Confidence = 1
	}
	return ev
}

// confidenceFor computes a simple confidence estimate from passed/total ratio.
// It is only used for non-pass verdicts; a fully passing set is always 1.0.
func confidenceFor(total, failed int) float64 {
	if total <= 0 {
		return 0
	}
	passed := total - failed
	if passed < 0 {
		passed = 0
	}
	return float64(passed) / float64(total)
}
