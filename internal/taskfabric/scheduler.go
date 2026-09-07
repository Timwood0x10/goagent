package taskfabric

import (
	"math"
	"strings"
)

// Candidate is an agent competing to acquire a task (design  of
// docs/zh/architecture/ares-runtime.md). Load and Confidence are expected in [0,1].
type Candidate struct {
	AgentID      string
	Capabilities []string
	// Load is the current utilization (1 = fully busy).
	Load float64
	// Confidence is the experience-derived prior (ares_skills Experience
	// BestMatch SuccessRate is a natural source).
	Confidence float64
	// Priority is the scheduling priority of the candidate (>= 0; 0 =
	// normal). It models OS-thread priority: a higher-priority agent gets a
	// proportional score boost, so the scheduler prefers it when capability /
	// load / confidence are comparable. The boost is multiplicative
	// (score × (1 + priority)); the default 0 keeps pre-priority behavior.
	Priority float64
}

// Score computes the capability-aware scheduling score:
//
//	score = capability_overlap × (1 - load) × confidence × (1 + priority)
//
// A candidate whose capabilities do not overlap the task's required
// capability scores 0 (never chosen for a task it cannot do). Load discounts
// busy agents; confidence prefers historically successful executors — the
// Skill-first / Experience design feeds this directly. Priority is the
// OS-thread analog: a higher-priority candidate wins ties it would otherwise
// lose (e.g. a busy high-priority agent can outscore an idle low-priority one
// when the priority boost exceeds the load discount).
//
// CONTRACT: Priority is documented as >= 0 (0 = normal). The boost is
// intentionally NOT clamped: a priority boost only multiplies the score
// within the capability-gated space, and an extreme priority is a legitimate
// operator signal (e.g. a dedicated agent that must win every dispatch). To
// bound the boost, callers clamp the priority they inject; clamping here
// would silently distort the operator's intent.
func Score(taskCapability string, c Candidate) float64 {
	return ScoreBreakdown(taskCapability, c).Score
}

// ScoreParts is the decomposed scheduling score — every factor the Score
// formula multiplies, plus the final Score itself. It exists so observability
// consumers (the scheduler's decision recorder) can render the exact breakdown
// the scheduler used WITHOUT recomputing any factor: Score and its explanation
// come from one evaluation, so they can never drift.
type ScoreParts struct {
	// Overlap is the capability-overlap fraction [0,1].
	Overlap float64
	// Load is the clamped utilization [0,1].
	Load float64
	// Confidence is the clamped experience prior [0,1].
	Confidence float64
	// PriorityBoost is (1 + priority), clamped to a 1.0 floor.
	PriorityBoost float64
	// Score is the final score (0 when Overlap <= 0).
	Score float64
}

// ScoreBreakdown computes the scheduling score AND its decomposition in a
// single pass. Score delegates to this; the decision recorder consumes it
// directly so the panel's per-candidate breakdown is the very computation the
// scheduler ranked on. The score is 0 whenever capability does not overlap,
// matching Score's capability gate exactly.
func ScoreBreakdown(taskCapability string, c Candidate) ScoreParts {
	overlap := CapabilityOverlap(taskCapability, c.Capabilities)
	load := clamp01(c.Load)
	conf := clamp01(c.Confidence)
	boost := 1.0
	if c.Priority > 0 {
		boost = 1.0 + c.Priority
	}
	parts := ScoreParts{Overlap: overlap, Load: load, Confidence: conf, PriorityBoost: boost}
	if overlap > 0 {
		parts.Score = overlap * (1 - load) * conf * boost
	}
	return parts
}

// Pick returns the best candidate (highest Score) for the task, or nil when
// no candidate overlaps the required capability at all.
//
// Last-resort guarantee: among capability-overlapping candidates, a candidate
// whose recorded history is all failures has Confidence 0 ⇒ Score 0. If such
// zero-score filtering left NOTHING, Pick falls back to the best
// capability-overlapping candidate ranked WITHOUT the confidence factor.
// Without this, a single recorded failure permanently stranded any task whose
// only capable executor had failed once — bounded retry budgets could never
// spend their attempts and collab graphs hung until timeout. The healthy
// path is unchanged: any candidate with positive score still beats the
// fallback (bottom of the ranking, never excluded).
func Pick(taskCapability string, candidates []Candidate) *Candidate {
	var best *Candidate
	var lastResort *Candidate
	bestScore := 0.0
	resortScore := 0.0
	for i := range candidates {
		c := &candidates[i]
		s := Score(taskCapability, *c)
		if s > 0 && (best == nil || s > bestScore) {
			best = c
			bestScore = s
		}
		overlap := CapabilityOverlap(taskCapability, c.Capabilities)
		if overlap <= 0 {
			continue
		}
		// Confidence-free ranking for the fallback tier: capability gate,
		// load discount and priority boost still apply. Only strictly
		// positive fallback scores qualify — an agent at full load (or
		// otherwise zero-capacity) must stay unreachable exactly as it is
		// under normal scoring.
		fb := overlap * (1 - clamp01(c.Load))
		if c.Priority > 0 {
			fb *= 1 + c.Priority
		}
		if fb <= 0 {
			continue
		}
		if lastResort == nil || fb > resortScore {
			lastResort = c
			resortScore = fb
		}
	}
	if best != nil {
		return best
	}
	return lastResort
}

// CapabilityOverlap is the fraction of the required capability segments (a
// slash-separated chain like "rust/unsafe-analysis") that the candidate's
// declared capabilities cover. A candidate declaring the required chain
// verbatim covers it fully (overlap = 1). Otherwise a prefix match counts
// (agent declaring "rust" covers required "rust/unsafe-analysis"). An empty
// required capability means the task is unconstrained and open to any
// candidate (overlap = 1).
// Exported so observability consumers (e.g. the scheduler's decision recorder)
// can render the same breakdown the Score formula uses, without duplicating
// the matching logic.
func CapabilityOverlap(required string, have []string) float64 {
	trimmed := strings.TrimSpace(required)
	if trimmed == "" {
		return 1.0
	}
	// Exact whole-chain match short-circuit. A namespaced capability like
	// "tool/write" is a single declared string whose only "/"-segment the
	// proportional loop below would credit is "tool" (via the prefix rule),
	// scoring 0.5 — tying it with every sibling "tool/*" agent and letting
	// map-iteration order decide the winner. That misroutes a "tool/write"
	// task to a "tool/research" executor whenever both are registered. An
	// exact declaration must instead cover the requirement fully.
	for _, h := range have {
		if strings.TrimSpace(h) == trimmed {
			return 1.0
		}
	}
	var req []string
	for _, part := range strings.Split(required, "/") {
		if part = strings.TrimSpace(part); part != "" {
			req = append(req, part)
		}
	}
	if len(req) == 0 {
		return 0
	}
	matched := 0
	for _, r := range req {
		for _, h := range have {
			if h == r || strings.HasPrefix(h, r+"/") {
				matched++
				break
			}
		}
	}
	return float64(matched) / float64(len(req))
}

// clamp01 bounds a value to [0,1].
func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}
