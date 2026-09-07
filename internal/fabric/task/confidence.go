package taskfabric

// ConfidenceSource supplies the experience-derived confidence for a task
// pattern (design  ares-runtime: Skill-first — the Experience
// BestMatch SuccessRate is the natural confidence source feeding the
// scheduler score: capability_overlap × (1-load) × confidence). The
// interface is defined here on the consumer side (code_rules §5.2);
// ares_skills implements it via an adapter over Experience.
type ConfidenceSource interface {
	// Confidence returns the experience prior for a task pattern, in [0,1].
	// 0 means "no experience yet" — the candidate keeps its declared
	// confidence (or stays unscheduled when it has none).
	Confidence(taskPattern string) float64
}
