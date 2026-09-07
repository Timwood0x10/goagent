// Package ares_bootstrap — runtime observability adapters.
//
// Bridges aresrecovery's recording surfaces (EvolutionTracer / FeedbackStore
// / GlobalTracer) to the introspection control plane's provider contracts
// (internal/introspect) so the existing
// /api/evolution/trajectory, /api/evolution/feedback and
// /api/observability/spans endpoints are backed by real components instead of
// returning empty lists.
package ares_bootstrap

import (
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/introspect"
)

// evolutionTrajectoryAdapter adapts *aresrecovery.EvolutionTracer to
// introspect.EvolutionTrajectoryProvider. The adapter also consumes the two
// components that were previously library-only:
//
//   - ChangeAttributor fills each generation change's Impact estimate (equal
//     split of the score delta unless the evolution system supplied one);
//   - FeedbackStore.ForCandidate + CombinedFitness enrich the trajectory with
//     human-rated combined fitness, so the /evolution/trajectory endpoint
//     shows human judgment next to the automatic score.
type evolutionTrajectoryAdapter struct {
	tracer *aresrecovery.EvolutionTracer
	store  *aresrecovery.FeedbackStore
}

// NewEvolutionTrajectoryProvider wraps a tracer (and optional feedback store)
// as the introspect trajectory provider. Returns nil when the tracer is nil
// (endpoint disabled). A nil store disables the feedback enrichment only.
func NewEvolutionTrajectoryProvider(tracer *aresrecovery.EvolutionTracer, store *aresrecovery.FeedbackStore) introspect.EvolutionTrajectoryProvider {
	if tracer == nil {
		return nil
	}
	return &evolutionTrajectoryAdapter{tracer: tracer, store: store}
}

var _ introspect.EvolutionTrajectoryProvider = (*evolutionTrajectoryAdapter)(nil)

// EvolutionTrajectory returns the recorded generations as JSON-friendly
// values (oldest first), or nil when nothing is recorded. Each generation is
// enriched with:
//   - change attribution: every change entry carries its estimated
//     score impact, computed between adjacent generations;
//   - combined fitness: when human feedback exists for a top strategy,
//     the entry gains human_rating / combined_fitness (CombinedFitness blends
//     the generation best score with the human rating) / feedback_approved.
func (a *evolutionTrajectoryAdapter) EvolutionTrajectory() []map[string]any {
	views := a.tracer.TrajectoryViews()
	if len(views) == 0 {
		return nil
	}
	// Change attribution over adjacent generations. TrajectoryViews iterates
	// Snapshot in order, so views[i] aligns with snaps[i].
	if snaps := a.tracer.Snapshot(); len(snaps) > 1 {
		for i := 0; i+1 < len(views); i++ {
			impacts := aresrecovery.NewChangeAttributor().Attribute(&snaps[i], &snaps[i+1])
			if len(impacts) == 0 {
				continue
			}
			changes, _ := views[i+1]["changes"].([]map[string]any)
			for _, c := range changes {
				// Guard every decode: TrajectoryViews owns this shape today,
				// but a malformed entry must not panic the dashboard handler.
				sid, _ := c["strategy_id"].(string)
				if sid == "" {
					continue
				}
				imp, ok := impacts[sid]
				if !ok {
					continue
				}
				// Only fill an unset impact: an explicit attribution recorded
				// by the evolution system wins over the equal-split estimate.
				if existing, _ := c["impact"].(float64); existing != 0 {
					continue
				}
				c["impact"] = imp
			}
		}
	}
	// Human-feedback enrichment on top strategies.
	if a.store != nil {
		for _, v := range views {
			top, _ := v["top_strategies"].([]string)
			if len(top) == 0 {
				continue
			}
			score, _ := v["best_score"].(float64)
			rated := make([]map[string]any, 0, len(top))
			for _, id := range top {
				if fb := a.store.ForCandidate(id); fb != nil {
					rated = append(rated, map[string]any{
						"candidate_id":      id,
						"human_rating":      fb.Rating,
						"combined_fitness":  aresrecovery.CombinedFitness(score, fb.Rating),
						"feedback_approved": fb.Approved,
						"comments":          fb.Comments,
						"at":                fb.At,
					})
				}
			}
			if len(rated) > 0 {
				v["feedback"] = rated
			}
		}
	}
	return views
}

// evolutionFeedbackAdapter adapts *aresrecovery.FeedbackStore to
// introspect.EvolutionFeedbackSink. The introspect EvolutionFeedback payload
// maps directly onto aresrecovery.HumanFeedback.
type evolutionFeedbackAdapter struct {
	store *aresrecovery.FeedbackStore
}

// NewEvolutionFeedbackSink wraps a feedback store as the introspect feedback
// sink. Returns nil when the store is nil (endpoint disabled).
func NewEvolutionFeedbackSink(store *aresrecovery.FeedbackStore) introspect.EvolutionFeedbackSink {
	if store == nil {
		return nil
	}
	return &evolutionFeedbackAdapter{store: store}
}

var _ introspect.EvolutionFeedbackSink = (*evolutionFeedbackAdapter)(nil)

// SubmitFeedback records one human feedback entry.
func (a *evolutionFeedbackAdapter) SubmitFeedback(fb introspect.EvolutionFeedback) error {
	a.store.Add(aresrecovery.HumanFeedback{
		CandidateID: fb.CandidateID,
		Rating:      fb.Rating,
		Comments:    fb.Comments,
		Approved:    fb.Approved,
		Reason:      fb.Reason,
	})
	return nil
}

// globalTracerAdapter adapts *aresrecovery.GlobalTracer to
// introspect.ObservabilitySpansProvider.
type globalTracerAdapter struct {
	tracer *aresrecovery.GlobalTracer
}

// NewObservabilitySpansProvider wraps a global tracer as the introspect
// observability provider. Returns nil when the tracer is nil (endpoint
// disabled).
func NewObservabilitySpansProvider(tracer *aresrecovery.GlobalTracer) introspect.ObservabilitySpansProvider {
	if tracer == nil {
		return nil
	}
	return &globalTracerAdapter{tracer: tracer}
}

var _ introspect.ObservabilitySpansProvider = (*globalTracerAdapter)(nil)

// Spans returns a snapshot of the recorded spans (insertion order) as
// JSON-friendly values, or nil when nothing is recorded.
func (a *globalTracerAdapter) Spans() []map[string]any {
	spans := a.tracer.Spans()
	if len(spans) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(spans))
	for _, s := range spans {
		events := make([]map[string]any, 0, len(s.Events))
		for _, e := range s.Events {
			events = append(events, map[string]any{
				"at":     e.At,
				"name":   e.Name,
				"detail": e.Detail,
			})
		}
		out = append(out, map[string]any{
			"kind":       string(s.Kind),
			"id":         s.ID,
			"started_at": s.StartedAt,
			"ended_at":   s.EndedAt,
			"status":     s.Status,
			"parent_id":  s.ParentID,
			"events":     events,
		})
	}
	return out
}
