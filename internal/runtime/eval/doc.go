// Package eval is the ARES evaluation service (convergence Phase 3).
//
// It judges agent work: evidence collection, process/result verifiers,
// rubric-dimension judges, suite loading, and benchmark harnesses. It serves
// the G3 evolution gate (pass/fail verdicts on candidate strategies) and the
// `ares eval` operator surface. It executes nothing itself — verdicts are
// computed from task envelopes and evidence records produced elsewhere.
package eval
