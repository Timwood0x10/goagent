package flight

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// ── Timeline Tests ─────────────────────────────

func TestTimelineAddAndGet(t *testing.T) {
	tl := NewTimeline()
	if tl.Len() != 0 {
		t.Fatal("expected empty timeline")
	}

	tl.Add(TimelineEvent{
		ID:       "e1",
		AgentID:  "agent-1",
		Type:     EventAgentStart,
		Name:     "start",
		StartAt:  time.Now(),
		Duration: time.Second,
	})

	if tl.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", tl.Len())
	}

	ares_events := tl.Events()
	if ares_events[0].ID != "e1" {
		t.Errorf("expected e1, got %s", ares_events[0].ID)
	}
}

func TestTimelineFilterByAgent(t *testing.T) {
	tl := NewTimeline()
	now := time.Now()

	tl.Add(TimelineEvent{ID: "e1", AgentID: "a1", Type: EventAgentStart, StartAt: now})
	tl.Add(TimelineEvent{ID: "e2", AgentID: "a2", Type: EventAgentStart, StartAt: now})
	tl.Add(TimelineEvent{ID: "e3", AgentID: "a1", Type: EventAgentEnd, StartAt: now})

	filtered := tl.FilterByAgent("a1")
	if len(filtered) != 2 {
		t.Fatalf("expected 2 ares_events for a1, got %d", len(filtered))
	}
}

func TestTimelineFilterByType(t *testing.T) {
	tl := NewTimeline()
	now := time.Now()

	tl.Add(TimelineEvent{ID: "e1", AgentID: "a1", Type: EventToolCall, StartAt: now})
	tl.Add(TimelineEvent{ID: "e2", AgentID: "a1", Type: EventLLMCall, StartAt: now})
	tl.Add(TimelineEvent{ID: "e3", AgentID: "a1", Type: EventToolCall, StartAt: now})

	filtered := tl.FilterByType(EventToolCall)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 tool ares_events, got %d", len(filtered))
	}
}

func TestTimelineSummary(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	tl.Add(TimelineEvent{
		ID: "e1", AgentID: "a1", Type: EventToolCall,
		StartAt: base, EndAt: base.Add(8 * time.Second), Duration: 8 * time.Second,
	})
	tl.Add(TimelineEvent{
		ID: "e2", AgentID: "a1", Type: EventLLMCall,
		StartAt: base.Add(8 * time.Second), EndAt: base.Add(10 * time.Second), Duration: 2 * time.Second,
	})

	summary := tl.Summary()

	if summary.EventCount != 2 {
		t.Errorf("EventCount = %d, want 2", summary.EventCount)
	}
	if summary.ToolDuration != 8*time.Second {
		t.Errorf("ToolDuration = %v, want 8s", summary.ToolDuration)
	}
	if summary.LLMDuration != 2*time.Second {
		t.Errorf("LLMDuration = %v, want 2s", summary.LLMDuration)
	}
	if summary.TotalDuration != 10*time.Second {
		t.Errorf("TotalDuration = %v, want 10s", summary.TotalDuration)
	}
	// 80% tool, 20% LLM.
	if summary.ToolPercent < 79 || summary.ToolPercent > 81 {
		t.Errorf("ToolPercent = %.1f, want ~80", summary.ToolPercent)
	}
}

// TestTimelineSummaryWithZeroEndAt verifies that Summary() handles ares_events
// with zero EndAt (e.g. agent.start ares_events that lack end timestamps).
// The maxEnd calculation must skip EndAt.IsZero() to avoid zero Duration.
func TestTimelineSummaryWithZeroEndAt(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	// Add an agent.start event with no EndAt (zero value).
	tl.Add(TimelineEvent{
		ID: "e1", AgentID: "a1", Type: EventAgentStart,
		StartAt: base, Duration: 0,
	})
	// Add a tool.call event with a proper EndAt.
	tl.Add(TimelineEvent{
		ID: "e2", AgentID: "a1", Type: EventToolCall,
		StartAt: base, EndAt: base.Add(5 * time.Second), Duration: 5 * time.Second,
	})
	// Add another start-only event.
	tl.Add(TimelineEvent{
		ID: "e3", AgentID: "a1", Type: EventAgentStart,
		StartAt: base.Add(2 * time.Second), Duration: 0,
	})

	summary := tl.Summary()

	if summary.EventCount != 3 {
		t.Errorf("EventCount = %d, want 3", summary.EventCount)
	}
	if summary.TotalDuration != 5*time.Second {
		t.Errorf("TotalDuration = %v, want 5s (from tool.call, ignoring zero-EndAt ares_events)", summary.TotalDuration)
	}
	if summary.ToolDuration != 5*time.Second {
		t.Errorf("ToolDuration = %v, want 5s", summary.ToolDuration)
	}
}

func TestTimelineConcurrentAdd(t *testing.T) {
	tl := NewTimeline()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tl.Add(TimelineEvent{
				ID:      fmt.Sprintf("e%d", n),
				AgentID: "a1",
				Type:    EventToolCall,
				StartAt: time.Now(),
			})
		}(i)
	}

	wg.Wait()
	if tl.Len() != 100 {
		t.Errorf("expected 100 ares_events, got %d", tl.Len())
	}
}

// ── Graph Tests ────────────────────────────────

func TestGraphAddNode(t *testing.T) {
	g := NewGraph()

	g.AddNode(&GraphNode{ID: "root", Type: NodeAgent, Name: "Leader", Status: StatusRunning, StartAt: time.Now()})
	g.AddNode(&GraphNode{ID: "child1", ParentID: "root", Type: NodeTool, Name: "Search", Status: StatusCompleted, StartAt: time.Now()})

	root := g.Root()
	if root == nil {
		t.Fatal("expected root node")
	}
	if root.Name != "Leader" {
		t.Errorf("expected Leader, got %s", root.Name)
	}
	if len(root.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(root.Children))
	}
	if root.Children[0].Name != "Search" {
		t.Errorf("expected Search child, got %s", root.Children[0].Name)
	}
}

func TestGraphDepth(t *testing.T) {
	g := NewGraph()

	g.AddNode(&GraphNode{ID: "r", Type: NodeAgent, Name: "Root", StartAt: time.Now()})
	g.AddNode(&GraphNode{ID: "c1", ParentID: "r", Type: NodeAgent, Name: "Child", StartAt: time.Now()})
	g.AddNode(&GraphNode{ID: "c2", ParentID: "c1", Type: NodeTool, Name: "Tool", StartAt: time.Now()})

	if g.Depth() != 2 {
		t.Errorf("depth = %d, want 2", g.Depth())
	}
}

func TestGraphExportMermaid(t *testing.T) {
	g := NewGraph()
	g.AddNode(&GraphNode{ID: "r", Type: NodeAgent, Name: "Leader", Status: StatusRunning, StartAt: time.Now()})

	mermaid := g.ExportMermaid()
	if mermaid == "" {
		t.Fatal("expected non-empty mermaid")
	}
	if mermaid[:7] != "graph L" {
		t.Errorf("expected mermaid to start with 'graph L', got %s", mermaid[:7])
	}
}

func TestGraphExportDOT(t *testing.T) {
	g := NewGraph()
	g.AddNode(&GraphNode{ID: "r", Type: NodeAgent, Name: "Leader", Status: StatusRunning, StartAt: time.Now()})

	dot := g.ExportDOT()
	if dot == "" {
		t.Fatal("expected non-empty DOT")
	}
	if dot[:7] != "digraph" {
		t.Errorf("expected DOT to start with 'digraph', got %s", dot[:7])
	}
}

func TestGraphExportJSON(t *testing.T) {
	g := NewGraph()
	g.AddNode(&GraphNode{ID: "r", Type: NodeAgent, Name: "Leader", Status: StatusCompleted, StartAt: time.Now()})

	data, err := g.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty JSON")
	}
}

func TestGraphEmpty(t *testing.T) {
	g := NewGraph()
	if g.Root() != nil {
		t.Error("expected nil root for empty graph")
	}
	if g.Depth() != 0 {
		t.Errorf("expected depth 0, got %d", g.Depth())
	}
}

// ── Decision Tests ─────────────────────────────

func TestDecisionLogAddAndGet(t *testing.T) {
	log := NewDecisionLog()

	log.Add(Decision{
		ID:         "d1",
		AgentID:    "a1",
		Type:       DecisionToolSelect,
		Candidates: []string{"google", "vector"},
		Selected:   "google",
		Reason:     "query has current ares_events",
		Confidence: 0.92,
		Timestamp:  time.Now(),
	})

	if log.Len() != 1 {
		t.Fatalf("expected 1 decision, got %d", log.Len())
	}

	all := log.All()
	if all[0].Selected != "google" {
		t.Errorf("expected google, got %s", all[0].Selected)
	}
}

func TestDecisionLogFilter(t *testing.T) {
	log := NewDecisionLog()
	now := time.Now()

	log.Add(Decision{ID: "d1", AgentID: "a1", Type: DecisionToolSelect, Timestamp: now})
	log.Add(Decision{ID: "d2", AgentID: "a2", Type: DecisionRouting, Timestamp: now})
	log.Add(Decision{ID: "d3", AgentID: "a1", Type: DecisionRetry, Timestamp: now})

	if len(log.FilterByAgent("a1")) != 2 {
		t.Error("expected 2 decisions for a1")
	}
	if len(log.FilterByType(DecisionRouting)) != 1 {
		t.Error("expected 1 routing decision")
	}
}

// ── Diagnostics Tests ──────────────────────────

func TestDiagnosticsRecordAndDistribution(t *testing.T) {
	e := NewDiagnosticsEngine()

	e.Record(DiagnosticRecord{ID: "f1", Category: DiagToolTimeout})
	e.Record(DiagnosticRecord{ID: "f2", Category: DiagToolTimeout})
	e.Record(DiagnosticRecord{ID: "f3", Category: DiagLLMError})

	if e.Len() != 3 {
		t.Fatalf("expected 3 records, got %d", e.Len())
	}

	dist := e.Distribution()
	if dist.Total != 3 {
		t.Errorf("total = %d, want 3", dist.Total)
	}
	if dist.Categories[DiagToolTimeout] != 2 {
		t.Errorf("tool_timeout count = %d, want 2", dist.Categories[DiagToolTimeout])
	}
	if dist.Percentages[DiagToolTimeout] < 66 || dist.Percentages[DiagToolTimeout] > 67 {
		t.Errorf("tool_timeout percent = %.1f, want ~66.7", dist.Percentages[DiagToolTimeout])
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		msg  string
		want DiagnosticCategory
	}{
		{"context deadline exceeded", DiagToolTimeout},
		{"connection timeout", DiagToolTimeout},
		{"openai api error", DiagLLMError},
		{"failed to parse json", DiagParseError},
		{"unmarshal error", DiagParseError},
		{"session not found", DiagMemoryError},
		{"connection refused", DiagNetworkError},
		{"dial tcp: no such host", DiagNetworkError},
		{"invalid yaml config", DiagConfigError},
		{"something random", DiagUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := ClassifyError(tt.msg)
			if got != tt.want {
				t.Errorf("ClassifyError(%q) = %s, want %s", tt.msg, got, tt.want)
			}
		})
	}
}

func TestSuggestFix(t *testing.T) {
	for _, cat := range []DiagnosticCategory{
		DiagToolTimeout, DiagLLMError, DiagParseError,
		DiagMemoryError, DiagNetworkError, DiagConfigError,
		DiagConcurrencyError, DiagUnknown,
	} {
		suggestions := SuggestFix(cat)
		if len(suggestions) == 0 {
			t.Errorf("SuggestFix(%s) returned empty", cat)
		}
	}
}

func TestAutoDiagnose(t *testing.T) {
	r := AutoDiagnose("agent-1", "task-1", fmt.Errorf("connection timeout"), 5*time.Second)

	if r.AgentID != "agent-1" {
		t.Errorf("AgentID = %s, want agent-1", r.AgentID)
	}
	if r.Category != DiagToolTimeout {
		t.Errorf("Category = %s, want tool_timeout", r.Category)
	}
	if r.Suggestion == "" {
		t.Error("expected non-empty suggestion")
	}
}

// ── Pipeline Tests ─────────────────────────────

func TestMemoryPipeline(t *testing.T) {
	p := NewMemoryPipeline("session-1")

	p.AddStage(PipelineStage{Name: "raw", InputCount: 500, OutputCount: 500, Duration: time.Second})
	p.AddStage(PipelineStage{Name: "distill", InputCount: 500, OutputCount: 32, Duration: 2 * time.Second})
	p.AddStage(PipelineStage{Name: "compress", InputCount: 32, OutputCount: 7, Duration: time.Second})

	if p.SessionID() != "session-1" {
		t.Errorf("SessionID = %s, want session-1", p.SessionID())
	}

	stages := p.Stages()
	if len(stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(stages))
	}

	summary := p.Summary()
	if summary.TotalInput != 500 {
		t.Errorf("TotalInput = %d, want 500", summary.TotalInput)
	}
	if summary.TotalOutput != 7 {
		t.Errorf("TotalOutput = %d, want 7", summary.TotalOutput)
	}
	if summary.CompressionRatio < 0.013 || summary.CompressionRatio > 0.015 {
		t.Errorf("CompressionRatio = %.4f, want ~0.014", summary.CompressionRatio)
	}
	if summary.TotalDuration != 4*time.Second {
		t.Errorf("TotalDuration = %v, want 4s", summary.TotalDuration)
	}
}

// ── Collector Tests ────────────────────────────

func TestCollectorStartStop(t *testing.T) {
	c := NewCollector(CollectorConfig{})
	ctx := context.Background()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	c.Stop()

	if c.Timeline().Len() != 0 {
		t.Error("expected empty timeline")
	}
}

// ── Replay Tests ───────────────────────────────

func TestReplaySessionStep(t *testing.T) {
	store := newMockEventStore()
	ctx := context.Background()

	base := time.Now()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "agent.started", ts: base},
		{typ: "tool.call", ts: base.Add(time.Second)},
		{typ: "tool.result", ts: base.Add(2 * time.Second)},
		{typ: "agent.stopped", ts: base.Add(3 * time.Second)},
	})

	session, err := NewReplaySession(ctx, store, "task-1")
	if err != nil {
		t.Fatalf("NewReplaySession error: %v", err)
	}

	if session.TotalSteps() != 4 {
		t.Fatalf("expected 4 steps, got %d", session.TotalSteps())
	}

	step, err := session.Step()
	if err != nil {
		t.Fatalf("Step error: %v", err)
	}
	if step.EventType != "agent.started" {
		t.Errorf("expected agent.started, got %s", step.EventType)
	}
	if step.StepNum != 0 {
		t.Errorf("expected step 0, got %d", step.StepNum)
	}

	// Jump to step 2.
	step, err = session.StepTo(2)
	if err != nil {
		t.Fatalf("StepTo error: %v", err)
	}
	if step.EventType != "tool.result" {
		t.Errorf("expected tool.result, got %s", step.EventType)
	}
}

func TestReplaySessionSummary(t *testing.T) {
	store := newMockEventStore()
	ctx := context.Background()

	base := time.Now()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "agent.started", ts: base},
		{typ: "agent.stopped", ts: base.Add(time.Second)},
	})

	session, _ := NewReplaySession(ctx, store, "task-1")
	summary := session.Summary()

	if summary.TaskID != "task-1" {
		t.Errorf("TaskID = %s, want task-1", summary.TaskID)
	}
	if summary.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", summary.TotalSteps)
	}
}

func TestReplaySessionOutOfRange(t *testing.T) {
	store := newMockEventStore()
	ctx := context.Background()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "x", ts: time.Now()},
	})

	session, _ := NewReplaySession(ctx, store, "task-1")

	_, err := session.StepTo(-1)
	if err == nil {
		t.Error("expected error for negative step")
	}
	_, err = session.StepTo(100)
	if err == nil {
		t.Error("expected error for out-of-range step")
	}
}

// ── Mock EventStore ────────────────────────────

type mockEventStoreForFlight struct {
	ares_events map[string][]*ares_events.Event
	mu          sync.RWMutex
}

func newMockEventStore() *mockEventStoreForFlight {
	return &mockEventStoreForFlight{
		ares_events: make(map[string][]*ares_events.Event),
	}
}

func (s *mockEventStoreForFlight) addEvents(streamID string, evts []struct {
	typ     string
	ts      time.Time
	payload map[string]any
}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range evts {
		evt := &ares_events.Event{
			ID:        fmt.Sprintf("evt-%d", time.Now().UnixNano()),
			StreamID:  streamID,
			Type:      ares_events.EventType(e.typ),
			Payload:   e.payload,
			Timestamp: e.ts,
		}
		s.ares_events[streamID] = append(s.ares_events[streamID], evt)
	}
}

func (s *mockEventStoreForFlight) Append(_ context.Context, streamID string, evts []*ares_events.Event, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ares_events[streamID] = append(s.ares_events[streamID], evts...)
	return nil
}

func (s *mockEventStoreForFlight) Read(_ context.Context, streamID string, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	evts := s.ares_events[streamID]
	if opts.Limit > 0 && opts.Limit < len(evts) {
		evts = evts[:opts.Limit]
	}
	return evts, nil
}

func (s *mockEventStoreForFlight) ReadAll(_ context.Context, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []*ares_events.Event
	for _, evts := range s.ares_events {
		all = append(all, evts...)
	}
	return all, nil
}

func (s *mockEventStoreForFlight) Subscribe(_ context.Context, _ ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	ch := make(chan *ares_events.Event, 16)
	return ch, nil
}

func (s *mockEventStoreForFlight) StreamVersion(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// ── Graph Edge Cases ───────────────────────────

func TestGraphGetNodeNotFound(t *testing.T) {
	g := NewGraph()
	_, ok := g.GetNode("nonexistent")
	if ok {
		t.Error("expected false for nonexistent node")
	}
}

func TestGraphNodes(t *testing.T) {
	g := NewGraph()
	g.AddNode(&GraphNode{ID: "r", Type: NodeAgent, Name: "Root", StartAt: time.Now()})
	g.AddNode(&GraphNode{ID: "c1", ParentID: "r", Type: NodeTool, Name: "T1", StartAt: time.Now()})
	g.AddNode(&GraphNode{ID: "c2", ParentID: "r", Type: NodeLLM, Name: "L1", StartAt: time.Now()})

	nodes := g.Nodes()
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
}

func TestGraphExportMermaidEmpty(t *testing.T) {
	g := NewGraph()
	mermaid := g.ExportMermaid()
	if mermaid == "" {
		t.Fatal("expected non-empty mermaid for empty graph")
	}
}

func TestGraphExportDOTEmpty(t *testing.T) {
	g := NewGraph()
	dot := g.ExportDOT()
	if dot != "digraph {}" {
		t.Errorf("expected 'digraph {}', got %s", dot)
	}
}

// ── Decision Edge Cases ────────────────────────

func TestDecisionLogConcurrent(t *testing.T) {
	log := NewDecisionLog()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			log.Add(Decision{
				ID:        fmt.Sprintf("d%d", n),
				AgentID:   "a1",
				Type:      DecisionToolSelect,
				Timestamp: time.Now(),
			})
		}(i)
	}

	wg.Wait()
	if log.Len() != 50 {
		t.Errorf("expected 50 decisions, got %d", log.Len())
	}
}

func TestDecisionLogEmpty(t *testing.T) {
	log := NewDecisionLog()
	if log.Len() != 0 {
		t.Error("expected 0")
	}
	if len(log.All()) != 0 {
		t.Error("expected empty All()")
	}
	if len(log.FilterByAgent("x")) != 0 {
		t.Error("expected empty filter")
	}
	if len(log.FilterByType(DecisionToolSelect)) != 0 {
		t.Error("expected empty filter")
	}
}

// ── Diagnostics Edge Cases ─────────────────────

func TestDiagnosticsAllReturnsCopy(t *testing.T) {
	e := NewDiagnosticsEngine()
	e.Record(DiagnosticRecord{ID: "f1", Category: DiagToolTimeout})

	all := e.All()
	all[0].ID = "modified"

	original := e.All()
	if original[0].ID != "f1" {
		t.Error("All() should return a copy, not a reference")
	}
}

func TestDiagnosticsFilterByAgentEmpty(t *testing.T) {
	e := NewDiagnosticsEngine()
	e.Record(DiagnosticRecord{ID: "f1", AgentID: "a1", Category: DiagToolTimeout})

	result := e.FilterByAgent("nonexistent")
	if len(result) != 0 {
		t.Errorf("expected 0, got %d", len(result))
	}
}

func TestDiagnosticsFilterByCategoryEmpty(t *testing.T) {
	e := NewDiagnosticsEngine()
	e.Record(DiagnosticRecord{ID: "f1", Category: DiagToolTimeout})

	result := e.FilterByCategory(DiagLLMError)
	if len(result) != 0 {
		t.Errorf("expected 0, got %d", len(result))
	}
}

func TestDiagnosticsDistributionEmpty(t *testing.T) {
	e := NewDiagnosticsEngine()
	dist := e.Distribution()
	if dist.Total != 0 {
		t.Errorf("expected 0 total, got %d", dist.Total)
	}
}

// ── Pipeline Edge Cases ────────────────────────

func TestMemoryPipelineEmpty(t *testing.T) {
	p := NewMemoryPipeline("s1")
	if p.SessionID() != "s1" {
		t.Error("wrong session ID")
	}
	if len(p.Stages()) != 0 {
		t.Error("expected empty stages")
	}
	summary := p.Summary()
	if summary.TotalInput != 0 {
		t.Error("expected 0 input")
	}
	if summary.CompressionRatio != 0 {
		t.Error("expected 0 ratio")
	}
}

func TestMemoryPipelineStagesReturnsCopy(t *testing.T) {
	p := NewMemoryPipeline("s1")
	p.AddStage(PipelineStage{Name: "raw", InputCount: 100, OutputCount: 100})

	stages := p.Stages()
	stages[0].Name = "modified"

	original := p.Stages()
	if original[0].Name != "raw" {
		t.Error("Stages() should return a copy")
	}
}

// ── Collector with Events ──────────────────────

func TestCollectorProcessEvents(t *testing.T) {
	c := NewCollector(CollectorConfig{})

	// Simulate agent lifecycle ares_events.
	base := time.Now()

	c.processEvent(context.Background(), &ares_events.Event{
		ID: "e1", StreamID: "agent-1", Type: ares_events.EventAgentStarted,
		Timestamp: base, Payload: map[string]any{"type": "leader"},
	})

	if c.Timeline().Len() != 1 {
		t.Fatalf("expected 1 timeline event, got %d", c.Timeline().Len())
	}

	if c.Graph().Root() == nil {
		t.Fatal("expected root node in graph")
	}
	if c.Graph().Root().Name != "agent-1" {
		t.Errorf("expected agent-1, got %s", c.Graph().Root().Name)
	}

	c.processEvent(context.Background(), &ares_events.Event{
		ID: "e2", StreamID: "agent-1", Type: ares_events.EventAgentStopped,
		Timestamp: base.Add(5 * time.Second),
	})

	if c.Timeline().Len() != 2 {
		t.Fatalf("expected 2 timeline ares_events, got %d", c.Timeline().Len())
	}
}

func TestCollectorProcessTaskFailed(t *testing.T) {
	c := NewCollector(CollectorConfig{})

	c.processEvent(context.Background(), &ares_events.Event{
		ID: "f1", StreamID: "agent-1", Type: ares_events.EventTaskFailed,
		Timestamp: time.Now(),
		Payload:   map[string]any{"error": "connection timeout"},
	})

	if c.Diagnostics().Len() != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", c.Diagnostics().Len())
	}

	diag := c.Diagnostics().All()[0]
	if diag.Category != DiagToolTimeout {
		t.Errorf("expected tool_timeout, got %s", diag.Category)
	}
	if diag.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", diag.AgentID)
	}
}

func TestCollectorProcessMemoryDistilled(t *testing.T) {
	c := NewCollector(CollectorConfig{})

	c.processEvent(context.Background(), &ares_events.Event{
		ID: "m1", StreamID: "session-1", Type: ares_events.EventMemoryDistilled,
		Timestamp: time.Now(),
		Payload:   map[string]any{"input_count": float64(500), "output_count": float64(32)},
	})

	p := c.Pipeline("session-1")
	if p == nil {
		t.Fatal("expected pipeline for session-1")
	}
	stages := p.Stages()
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(stages))
	}
	if stages[0].InputCount != 500 {
		t.Errorf("expected 500 input, got %d", stages[0].InputCount)
	}
}

func TestCollectorProcessLLMCall(t *testing.T) {
	c := NewCollector(CollectorConfig{})

	c.processEvent(context.Background(), &ares_events.Event{
		ID: "llm1", StreamID: "agent-1", Type: ares_events.EventLLMCall,
		Timestamp: time.Now(),
	})

	tl := c.Timeline()
	if tl.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", tl.Len())
	}
	if tl.Events()[0].Type != EventLLMCall {
		t.Errorf("expected llm.call, got %s", tl.Events()[0].Type)
	}
}

func TestCollectorProcessNilEvent(t *testing.T) {
	c := NewCollector(CollectorConfig{})
	// Should not panic.
	c.processEvent(context.Background(), nil)
}

func TestCollectorAccessors(t *testing.T) {
	c := NewCollector(CollectorConfig{})

	if c.Timeline() == nil {
		t.Error("Timeline() should not be nil")
	}
	if c.Graph() == nil {
		t.Error("Graph() should not be nil")
	}
	if c.Decisions() == nil {
		t.Error("Decisions() should not be nil")
	}
	if c.Diagnostics() == nil {
		t.Error("Diagnostics() should not be nil")
	}
	if c.Pipeline("nonexistent") != nil {
		t.Error("expected nil for nonexistent pipeline")
	}
}

// ── Replay Edge Cases ──────────────────────────

func TestReplaySessionCurrentBeforeStep(t *testing.T) {
	store := newMockEventStore()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "x", ts: time.Now()},
	})

	session, _ := NewReplaySession(context.Background(), store, "task-1")
	if session.Current() != nil {
		t.Error("expected nil before first step")
	}
}

func TestReplaySessionCurrentAfterStep(t *testing.T) {
	store := newMockEventStore()
	base := time.Now()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "agent.started", ts: base},
		{typ: "agent.stopped", ts: base.Add(time.Second)},
	})

	session, _ := NewReplaySession(context.Background(), store, "task-1")
	_, _ = session.Step()

	current := session.Current()
	if current == nil {
		t.Fatal("expected non-nil current")
	}
	if current.EventType != "agent.started" {
		t.Errorf("expected agent.started, got %s", current.EventType)
	}
}

func TestReplaySessionIsFinished(t *testing.T) {
	store := newMockEventStore()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "x", ts: time.Now()},
	})

	session, _ := NewReplaySession(context.Background(), store, "task-1")
	if session.IsFinished() {
		t.Error("should not be finished before stepping")
	}

	_, _ = session.Step()
	if !session.IsFinished() {
		t.Error("should be finished after last step")
	}
}

func TestReplaySessionStepPastEnd(t *testing.T) {
	store := newMockEventStore()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "x", ts: time.Now()},
	})

	session, _ := NewReplaySession(context.Background(), store, "task-1")
	_, _ = session.Step()

	_, err := session.Step()
	if err == nil {
		t.Error("expected error when stepping past end")
	}
}

func TestReplaySessionReset(t *testing.T) {
	store := newMockEventStore()
	store.addEvents("task-1", []struct {
		typ     string
		ts      time.Time
		payload map[string]any
	}{
		{typ: "x", ts: time.Now()},
	})

	session, _ := NewReplaySession(context.Background(), store, "task-1")
	_, _ = session.Step()
	session.Reset()

	if session.Current() != nil {
		t.Error("expected nil current after reset")
	}

	// Should be able to step again.
	step, err := session.Step()
	if err != nil {
		t.Fatalf("Step after reset error: %v", err)
	}
	if step.StepNum != 0 {
		t.Errorf("expected step 0 after reset, got %d", step.StepNum)
	}
}

// ── FlightRecorder Tests ───────────────────────

func TestFlightRecorderStartStop(t *testing.T) {
	fr := NewFlightRecorder(FlightRecorderConfig{})
	ctx := context.Background()

	if err := fr.Start(ctx); err != nil {
		t.Fatalf("Start error: %v", err)
	}

	// Double start should be fine.
	if err := fr.Start(ctx); err != nil {
		t.Fatalf("Double start error: %v", err)
	}

	fr.Stop()
	fr.Stop() // Double stop should be fine.
}

func TestFlightRecorderAccessors(t *testing.T) {
	fr := NewFlightRecorder(FlightRecorderConfig{})

	if fr.Timeline() == nil {
		t.Error("Timeline() should not be nil")
	}
	if fr.Graph() == nil {
		t.Error("Graph() should not be nil")
	}
	if fr.Decisions() == nil {
		t.Error("Decisions() should not be nil")
	}
	if fr.Diagnostics() == nil {
		t.Error("Diagnostics() should not be nil")
	}
}
