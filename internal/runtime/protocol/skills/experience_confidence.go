package ares_skills

import (
	"github.com/Timwood0x10/ares/internal/taskfabric"
)

// Compile-time guarantee: ExperienceConfidenceSource satisfies the consumer-
// side ConfidenceSource interface (code_rules §5.2: interfaces live on the
// consumer; the adapter proves conformance at build time).
var _ taskfabric.ConfidenceSource = (*ExperienceConfidenceSource)(nil)

// ExperienceConfidenceSource adapts the learned-source Experience to the
// taskfabric.ConfidenceSource interface (design of ares-runtime: Score's
// Confidence comes from Experience BestMatch SuccessRate — the Skill-first
// final landing point: the capability-aware scheduler is driven by real
// learned priors, not constants).
type ExperienceConfidenceSource struct {
	exp *Experience
}

// NewExperienceConfidenceSource wraps an Experience as a confidence source.
//
// Args:
//   - exp: the learned-source Experience (nil yields 0 confidence).
//
// Returns:
//   - *ExperienceConfidenceSource: the adapter.
func NewExperienceConfidenceSource(exp *Experience) *ExperienceConfidenceSource {
	return &ExperienceConfidenceSource{exp: exp}
}

// Confidence returns the best prior's success rate for a task pattern, in
// [0,1]. A pattern with no prior yields 0 (no experience yet — the candidate
// keeps its declared confidence or stays unscheduled).
func (s *ExperienceConfidenceSource) Confidence(taskPattern string) float64 {
	if s == nil || s.exp == nil {
		return 0
	}
	rec, ok := s.exp.BestMatch(taskPattern)
	if !ok {
		return 0
	}
	return rec.SuccessRate
}
