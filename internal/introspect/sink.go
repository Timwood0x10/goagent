package introspect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Feed severity levels (dot color in the activity strip) and event kinds.
const (
	feedOK     = "ok"
	feedInfo   = "info"
	feedWarn   = "warn"
	feedDanger = "danger"

	kindAgent    = "agent"
	kindTask     = "task"
	kindRecovery = "recovery"
)

// Sink tails the shared EventStore and distills lifecycle events into the
// panel's activity feed — the "who died, who took work" strip (#panel
// feedback). It is strictly read-only: one Subscribe, no publishes.
//
// The intelligence engine (health/anomalies/insights) is fed by a separate
// subscription in cmd/ares (setupServeControlPlane) rather than here, keeping
// the two consumers independent.
type Sink struct {
	store *Store
	// collab is the optional collaboration-edge producer. When
	// set, lifecycle/task events are projected into agent→task edges so the
	// panel's Collab tab shows the real work distribution instead of an
	// always-empty graph.
	collab *CollabReporter
}

// NewSink builds a sink feeding the given store.
func NewSink(store *Store) *Sink {
	return &Sink{store: store}
}

// WithCollab attaches the collaboration-edge producer. Returns the sink for
// chaining. Nil is a no-op (Collab stays empty).
func (s *Sink) WithCollab(c *CollabReporter) *Sink {
	s.collab = c
	return s
}

// recordCollab projects one event into a collaboration edge: the acting
// agent (payload agent_id) connected to the affected task (payload task_id).
// Events without an agent_id are not edges (system ticks, store internals).
func (s *Sink) recordCollab(evt *ares_events.Event) {
	if s.collab == nil || evt == nil {
		return
	}
	agentID := str(evt.Payload["agent_id"])
	if agentID == "" {
		return
	}
	s.collab.Record(CollabEdge{
		From:   agentID,
		To:     str(evt.Payload["task_id"]),
		Topic:  string(evt.Type),
		TaskID: str(evt.Payload["task_id"]),
		TS:     evt.Timestamp,
	})
}

// Run subscribes and maps events until ctx is cancelled. Intended for
// Components.GoBackground.
func (s *Sink) Run(ctx context.Context, eventStore ares_events.EventStore) error {
	if eventStore == nil {
		return nil
	}
	ch, err := eventStore.Subscribe(ctx, ares_events.EventFilter{})
	if err != nil {
		return fmt.Errorf("introspect: subscribe: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-ch:
			if !ok {
				// The event store closed the subscription (shutdown). Stop
				// rather than busy-spin on a closed channel returning nil.
				return nil
			}
			if entry, mapped := MapTimelineEvent(evt); mapped {
				s.store.PushEvent(entry)
			}
			s.recordCollab(evt)
		}
	}
}

// MapTimelineEvent distills one bus event into a feed row. Returns false for
// event types the feed does not surface (noise control: the feed tells the
// lifecycle story, not every internal tick).
func MapTimelineEvent(evt *ares_events.Event) (TimelineEntry, bool) {
	if evt == nil {
		return TimelineEntry{}, false
	}
	e := TimelineEntry{
		TS:      evt.Timestamp,
		Type:    string(evt.Type),
		AgentID: str(evt.Payload["agent_id"]),
		TaskID:  str(evt.Payload["task_id"]),
	}
	switch evt.Type {
	case ares_events.EventAgentStopped:
		e.Kind, e.Level = kindAgent, feedDanger
		reason := str(evt.Payload["reason"])
		e.Text = fmt.Sprintf("%s died", idOr(e.AgentID, "agent"))
		if reason != "" {
			e.Text += " (" + reason + ")"
		}
	case ares_events.EventStepRecoveryCompleted:
		e.Kind, e.Level = kindRecovery, feedOK
		e.Text = fmt.Sprintf("recovered %s", idOr(e.TaskID, e.AgentID, "work"))
	case ares_events.EventStepRecoveryStarted:
		e.Kind, e.Level = kindRecovery, feedWarn
		e.Text = "recovery started"
	case ares_events.EventStepRecoveryFailed:
		e.Kind, e.Level = kindRecovery, feedDanger
		e.Text = "recovery FAILED"
	case ares_events.EventTaskCreated, ares_events.EventTaskReady:
		e.Kind, e.Level = kindTask, feedInfo
		e.Text = fmt.Sprintf("%s ready", idOr(e.TaskID, "task"))
	case ares_events.EventTaskAcquired, ares_events.EventTaskDispatched, ares_events.EventTaskStarted:
		e.Kind, e.Level = kindTask, feedInfo
		e.Text = fmt.Sprintf("%s → %s", idOr(e.TaskID, "task"), idOr(e.AgentID, "?"))
	case ares_events.EventTaskYielded, ares_events.EventTaskCheckpointed:
		e.Kind, e.Level = kindTask, feedInfo
		e.Text = fmt.Sprintf("%s yielded at quantum", idOr(e.TaskID, "task"))
	case ares_events.EventTaskPreempted, ares_events.EventTaskExpired:
		e.Kind, e.Level = kindTask, feedWarn
		e.Text = fmt.Sprintf("%s preempted", idOr(e.TaskID, "task"))
	case ares_events.EventTaskReleased:
		e.Kind, e.Level = kindTask, feedInfo
		e.Text = fmt.Sprintf("%s released", idOr(e.TaskID, "task"))
	case ares_events.EventTaskCompleted:
		e.Kind, e.Level = kindTask, feedOK
		e.Text = fmt.Sprintf("%s done", idOr(e.TaskID, "task"))
	case ares_events.EventTaskFailed:
		e.Kind, e.Level = kindTask, feedDanger
		e.Text = fmt.Sprintf("%s failed", idOr(e.TaskID, "task"))
	default:
		return TimelineEntry{}, false
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	return e, true
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func idOr(ids ...string) string {
	for _, s := range ids {
		if s != "" && s != "?" {
			return s
		}
	}
	if len(ids) > 0 {
		return ids[len(ids)-1]
	}
	return ""
}
