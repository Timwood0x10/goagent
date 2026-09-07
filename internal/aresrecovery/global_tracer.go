package aresrecovery

import (
	"sync"
	"time"
)

// Cross-Fabric tracing: a global tracer that follows a Task
// from creation to completion, an Agent's full execution trajectory, and an
// IPC message's route (by correlation id). Spans are recorded by the runtime
// hooks (Task Fabric transitions, Agent lifecycle, IPC bus) and queried for
// debugging/observability.

// SpanKind classifies what a TraceSpan tracks.
type SpanKind string

const (
	// SpanTask tracks a Task's lifecycle (create → acquire → run → complete/fail).
	SpanTask SpanKind = "task"
	// SpanAgent tracks an Agent's execution trajectory.
	SpanAgent SpanKind = "agent"
	// SpanMessage tracks an IPC message route by correlation id.
	SpanMessage SpanKind = "message"
)

// SpanEvent is one timestamped step inside a span (e.g. "acquired",
// "checkpointed", "replied").
type SpanEvent struct {
	// At is when the event happened.
	At time.Time `json:"at"`
	// Name is the step name (e.g. "acquired", "yielded", "replied").
	Name string `json:"name"`
	// Detail is optional structured detail (e.g. agent id, epoch).
	Detail map[string]any `json:"detail,omitempty"`
}

// TraceSpan is one traced unit: a task, an agent, or a message route.
type TraceSpan struct {
	// Kind is what this span tracks.
	Kind SpanKind `json:"kind"`
	// ID is the tracked entity id (task id / agent id / correlation id).
	ID string `json:"id"`
	// StartedAt is when the span was opened.
	StartedAt time.Time `json:"started_at"`
	// EndedAt is when the span was closed (zero while open).
	EndedAt time.Time `json:"ended_at,omitempty"`
	// Status is the terminal status ("" while open, e.g. "completed").
	Status string `json:"status,omitempty"`
	// ParentID links a child span to its cause (e.g. a message's parent task).
	ParentID string `json:"parent_id,omitempty"`
	// Events is the ordered step log of this span.
	Events []SpanEvent `json:"events"`

	// mu guards mutable fields (EndedAt, Status, Events) so a span can be
	// updated concurrently by runtime hooks while being queried.
	mu sync.Mutex `json:"-"`
}

// GlobalTracer records and serves cross-Fabric spans.
// Thread-safe; span history is capped by WithMaxSpans.
type GlobalTracer struct {
	mu    sync.Mutex
	spans map[string]*TraceSpan
	order []string // insertion order of span ids (for capped eviction)
	max   int      // 0 = unlimited
	now   func() time.Time
}

// NewGlobalTracer creates an empty tracer.
func NewGlobalTracer() *GlobalTracer {
	return &GlobalTracer{
		spans: make(map[string]*TraceSpan),
		now:   time.Now,
	}
}

// WithClock injects a controllable clock for deterministic tests.
func (t *GlobalTracer) WithClock(now func() time.Time) *GlobalTracer {
	t.now = now
	return t
}

// WithMaxSpans caps the retained span count (0 = unlimited). Returns the
// tracer for chaining.
func (t *GlobalTracer) WithMaxSpans(n int) *GlobalTracer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.max = n
	t.evictLocked()
	return t
}

// TraceTask opens (or returns an existing) task span and records an event.
//
// Args:
//   - taskID: the tracked task.
//   - event: step name (e.g. "created", "acquired", "run", "completed").
//   - detail: optional step detail.
//
// Returns:
//   - *TraceSpan: the task's span (callers may read it; mutation is internal).
func (t *GlobalTracer) TraceTask(taskID, event string, detail map[string]any) *TraceSpan {
	return t.trace(SpanTask, taskID, event, detail)
}

// TraceAgent opens (or returns an existing) agent span and records an event.
func (t *GlobalTracer) TraceAgent(agentID, event string, detail map[string]any) *TraceSpan {
	return t.trace(SpanAgent, agentID, event, detail)
}

// TraceMessage opens (or returns an existing) message span by correlation id
// and records an event. parentID links the message to its cause (e.g. the
// task it serves).
func (t *GlobalTracer) TraceMessage(correlationID, event string, parentID string, detail map[string]any) *TraceSpan {
	t.mu.Lock()
	span, ok := t.spans[correlationID]
	if !ok {
		span = &TraceSpan{Kind: SpanMessage, ID: correlationID, StartedAt: t.now(), ParentID: parentID}
		t.putLocked(correlationID, span)
	} else if span.ParentID == "" && parentID != "" {
		span.ParentID = parentID
	}
	t.mu.Unlock()
	span.mu.Lock()
	span.Events = append(span.Events, SpanEvent{At: t.now(), Name: event, Detail: detail})
	span.mu.Unlock()
	return span
}

// trace opens/updates a span for any kind. task/agent spans are keyed by
// their own id; a missing span opens a new one.
func (t *GlobalTracer) trace(kind SpanKind, id, event string, detail map[string]any) *TraceSpan {
	t.mu.Lock()
	span, ok := t.spans[id]
	if !ok {
		span = &TraceSpan{Kind: kind, ID: id, StartedAt: t.now()}
		t.putLocked(id, span)
	}
	t.mu.Unlock()
	span.mu.Lock()
	span.Events = append(span.Events, SpanEvent{At: t.now(), Name: event, Detail: detail})
	span.mu.Unlock()
	return span
}

// putLocked inserts a span and enforces the cap. Caller must hold t.mu.
func (t *GlobalTracer) putLocked(id string, span *TraceSpan) {
	t.spans[id] = span
	t.order = append(t.order, id)
	t.evictLocked()
}

// evictLocked drops the oldest spans beyond the cap. Caller must hold t.mu.
func (t *GlobalTracer) evictLocked() {
	if t.max <= 0 {
		return
	}
	for len(t.order) > t.max {
		oldest := t.order[0]
		t.order = t.order[1:]
		delete(t.spans, oldest)
	}
}

// Close marks a span terminal with a status (e.g. "completed", "failed").
//
// Args:
//   - id: the span id.
//   - status: the terminal status.
//
// Returns:
//   - *TraceSpan: the closed span, or nil when unknown.
func (t *GlobalTracer) Close(id, status string) *TraceSpan {
	t.mu.Lock()
	span, ok := t.spans[id]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	span.mu.Lock()
	defer span.mu.Unlock()
	span.Status = status
	span.EndedAt = t.now()
	return span
}

// Span returns a snapshot of one span, or nil when unknown.
func (t *GlobalTracer) Span(id string) *TraceSpan {
	t.mu.Lock()
	span, ok := t.spans[id]
	t.mu.Unlock()
	if !ok {
		return nil
	}
	return cloneSpan(span)
}

// Spans returns a copy of all spans (insertion order).
func (t *GlobalTracer) Spans() []*TraceSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*TraceSpan, 0, len(t.order))
	for _, id := range t.order {
		if span, ok := t.spans[id]; ok {
			out = append(out, cloneSpan(span))
		}
	}
	return out
}

// ByKind returns the spans of one kind (e.g. all task spans).
func (t *GlobalTracer) ByKind(kind SpanKind) []*TraceSpan {
	all := t.Spans()
	out := make([]*TraceSpan, 0, len(all))
	for _, s := range all {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

// Count returns the number of recorded spans.
func (t *GlobalTracer) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.spans)
}

// cloneSpan deep-copies a span so callers never mutate tracer state. The
// fields are copied explicitly (never by struct assignment) because TraceSpan
// embeds a sync.Mutex, which must not be copied (copylocks).
func cloneSpan(s *TraceSpan) *TraceSpan {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := TraceSpan{
		Kind:      s.Kind,
		ID:        s.ID,
		StartedAt: s.StartedAt,
		EndedAt:   s.EndedAt,
		Status:    s.Status,
		ParentID:  s.ParentID,
		Events:    append([]SpanEvent(nil), s.Events...),
	}
	return &out
}
