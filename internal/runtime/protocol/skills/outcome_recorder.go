package ares_skills

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// outcomeRecordWindow bounds how many task outcomes are retained in memory for
// the String summary (mirrors FeedbackRecorder's bounded outcome list).
const outcomeRecordWindow = 1000

// outcomeRecord is one observed task outcome associated with a skill.
type outcomeRecord struct {
	skillID string
	pattern string
	success bool
}

// SkillOutcomeRecorder closes the design §11 feedback loop on the record side:
// it consumes EventSubTaskResult events and persists a {skill, task_pattern,
// success} outcome into the catalog's Experience store. It is deliberately
// decoupled from the agent code — no leader/sub/planner change is needed:
//
//   - The planner's ExperienceLocator (wired in serve) pre-fills
//     task.UsedExperienceID with the best-matching skill ID, so the result
//     event already carries the skill association.
//   - This recorder only observes the existing EventSubTaskResult stream.
//
// Failure policy (low-risk, stable): recording is best-effort and never blocks
// or fails the task path. A nil catalog or nil event store is a no-op (offline
// mode, same convention as FeedbackRecorder). Events for tasks whose
// UsedExperienceID is empty are skipped silently.
type SkillOutcomeRecorder struct {
	catalog *Catalog

	// mu guards outcomes.
	mu sync.Mutex
	// outcomes holds the most recent outcomes for the String summary.
	outcomes []outcomeRecord
	// recorded counts total consumed outcomes (for tests/debug).
	recorded atomic.Int64
	// skipped counts events ignored because no skill was associated.
	skipped atomic.Int64
	// lastErr is the most recent non-fatal recording error, if any.
	lastErr atomic.Value // *string
}

// NewSkillOutcomeRecorder creates a recorder over a catalog. A nil catalog
// makes every consume a no-op (offline mode).
//
// Args:
//   - catalog: the built catalog whose Experience store receives outcomes.
//
// Returns:
//   - *SkillOutcomeRecorder: ready to consume events.
func NewSkillOutcomeRecorder(catalog *Catalog) *SkillOutcomeRecorder {
	return &SkillOutcomeRecorder{catalog: catalog, outcomes: make([]outcomeRecord, 0, 64)}
}

// Start subscribes to EventSubTaskResult events on store and consumes them
// until ctx is done, store closes, or Start returns an error. The consumption
// goroutine is started and runs detached (the recorder has no stop handle;
// cancelling ctx stops it). This mirrors the event-driven consumers in
// sdk/distill_events.go and internal/ares_memory/distillation.
//
// Args:
//   - ctx: lifetime of the subscription (cancelling stops consumption).
//   - store: the event store to subscribe to (nil is a no-op).
//
// Returns:
//   - error: subscription failure, or nil (nil store, or running).
func (r *SkillOutcomeRecorder) Start(ctx context.Context, store ares_events.EventStore) error {
	if r.catalog == nil || store == nil {
		return nil
	}
	ch, err := store.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{ares_events.EventSubTaskResult},
	})
	if err != nil {
		return fmt.Errorf("skill outcome recorder: subscribe: %w", err)
	}
	go r.consume(ctx, ch)
	return nil
}

// consume reads events and records outcomes until ctx is done or the channel
// closes. Panics from individual records are recovered so one bad event can
// never take down the consumer goroutine.
func (r *SkillOutcomeRecorder) consume(ctx context.Context, ch <-chan *ares_events.Event) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev == nil {
				continue
			}
			func() {
				defer func() {
					_ = recover() // never let a malformed event panic the consumer
				}()
				r.consumeOne(ev)
			}()
		case <-ctx.Done():
			return
		}
	}
}

// consumeOne records a single task outcome. Failures are logged and non-fatal.
func (r *SkillOutcomeRecorder) consumeOne(ev *ares_events.Event) {
	task, ok := ev.Payload["task"].(*models.Task)
	if !ok || task == nil || task.UsedExperienceID == "" {
		r.skipped.Add(1)
		return
	}
	success := false
	if s, ok := ev.Payload["success"].(bool); ok {
		success = s
	}

	skillID := task.UsedExperienceID
	pattern := skillTaskPattern(task)

	rate := 0.0
	if success {
		rate = 1.0
	}

	// The task outcome updates the relevance prior. A failure here must never
	// propagate — Experience.Record only errors on empty args, and both skill
	// ID and pattern are guaranteed non-empty below.
	if err := r.catalog.Experience().Record(skillID, pattern, rate); err != nil {
		r.setLastErr(err.Error())
		return
	}

	r.recorded.Add(1)
	r.appendOutcome(outcomeRecord{skillID: skillID, pattern: pattern, success: success})
}

// skillTaskPattern derives the task pattern for the experience prior. A
// precise description (the planner stores the original task input under
// "task_desc") is preferred — it gives BestMatch a far higher hit rate than
// the coarse agent type. It is truncated via capPatternLength (the single
// maxPatternLength standard, 256 runes) so full user input never lands in the
// Experience store. Falls back to the agent type (e.g. "top"), then the
// sub-agent ID, then "default". It never returns "".
func skillTaskPattern(task *models.Task) string {
	if desc, ok := task.Payload["task_desc"].(string); ok {
		desc = strings.TrimSpace(desc)
		if desc != "" {
			return capPatternLength(desc)
		}
	}
	if task.AgentType != "" {
		return string(task.AgentType)
	}
	if id, ok := task.Payload["subAgentID"].(string); ok && id != "" {
		return id
	}
	return "default"
}

// appendOutcome keeps a bounded history for the String summary.
func (r *SkillOutcomeRecorder) appendOutcome(rec outcomeRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, rec)
	if len(r.outcomes) > outcomeRecordWindow {
		r.outcomes = r.outcomes[len(r.outcomes)-outcomeRecordWindow:]
	}
}

// setLastErr records the most recent non-fatal error (string) atomically.
func (r *SkillOutcomeRecorder) setLastErr(msg string) {
	r.lastErr.Store(&msg)
}

// LastErr returns the most recent non-fatal recording error, if any.
//
// Returns:
//   - error: the last recording error, or nil when none occurred.
func (r *SkillOutcomeRecorder) LastErr() error {
	v := r.lastErr.Load()
	if v == nil {
		return nil
	}
	return fmt.Errorf("skill outcome recorder: %s", *(v.(*string)))
}

// Recorded returns the number of outcomes successfully recorded.
//
// Returns:
//   - int64: total consumed outcomes with a non-empty skill association.
func (r *SkillOutcomeRecorder) Recorded() int64 {
	return r.recorded.Load()
}

// Skipped returns the number of events skipped because no skill was associated
// (empty or unknown UsedExperienceID).
//
// Returns:
//   - int64: skipped event count.
func (r *SkillOutcomeRecorder) Skipped() int64 {
	return r.skipped.Load()
}

// String returns a human-readable summary of consumed outcomes.
//
// Returns:
//   - string: summary with counts and the most recent entries.
func (r *SkillOutcomeRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(r.outcomes)
	if total == 0 {
		return "SkillOutcomeRecorder: no outcomes recorded"
	}
	successes := 0
	for _, o := range r.outcomes {
		if o.success {
			successes++
		}
	}
	start := 0
	if total > 5 {
		start = total - 5
	}
	recent := r.outcomes[start:]
	out := make([]string, 0, len(recent))
	for _, o := range recent {
		status := "FAIL"
		if o.success {
			status = "OK"
		}
		out = append(out, status+" "+o.skillID+" "+o.pattern)
	}
	return fmt.Sprintf("SkillOutcomeRecorder: %d outcomes, %d%% success, recent: [%s]",
		total, successes*100/total, strings.Join(out, ", "))
}
