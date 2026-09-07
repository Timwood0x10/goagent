package evolution

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// mockFlightRecorder implements FlightRecorder for testing.
type mockFlightRecorder struct {
	diagnostics *mockDiagnosticsAccessor
	subscriber  *mockEventStoreSubscriber
}

func newMockFlightRecorder() *mockFlightRecorder {
	return &mockFlightRecorder{
		diagnostics: &mockDiagnosticsAccessor{},
		subscriber:  &mockEventStoreSubscriber{},
	}
}

func (m *mockFlightRecorder) Diagnostics() DiagnosticsAccessor {
	return m.diagnostics
}

func (m *mockFlightRecorder) EventStore() EventStoreSubscriber {
	return m.subscriber
}

// mockDiagnosticsAccessor implements DiagnosticsAccessor for testing.
type mockDiagnosticsAccessor struct {
	mu      sync.RWMutex
	reports map[string]*DiagnosticsReport
}

func (m *mockDiagnosticsAccessor) SetReport(agentID string, report *DiagnosticsReport) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reports == nil {
		m.reports = make(map[string]*DiagnosticsReport)
	}
	m.reports[agentID] = report
}

func (m *mockDiagnosticsAccessor) Get(agentID string) *DiagnosticsReport {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.reports[agentID]
}

// mockEventStoreSubscriber implements EventStoreSubscriber for testing.
type mockEventStoreSubscriber struct {
	ares_events []*ares_events.Event
	err         error
}

func (m *mockEventStoreSubscriber) Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan *ares_events.Event, len(m.ares_events))
	for _, evt := range m.ares_events {
		ch <- evt
	}
	close(ch)
	return ch, nil
}

// mockExperienceRepository implements ExperienceRepository for testing.
type mockExperienceRepository struct {
	mu          sync.Mutex
	experiences []*Experience
	createErr   error
}

func (m *mockExperienceRepository) Create(ctx context.Context, exp *Experience) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	m.experiences = append(m.experiences, exp)
	return nil
}

func (m *mockExperienceRepository) GetExperiences() []*Experience {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.experiences
}

// TestNewFlightToExperienceAdapter tests constructor with valid dependencies.
func TestNewFlightToExperienceAdapter(t *testing.T) {
	flight := newMockFlightRecorder()
	repo := &mockExperienceRepository{}

	adapter := NewFlightToExperienceAdapter(flight, repo)
	if adapter == nil {
		t.Fatal("expected non-nil adapter")
	}
	if adapter.flight == nil {
		t.Error("expected flight recorder to be set")
	}
	if adapter.expRepo == nil {
		t.Error("expected experience repo to be set")
	}
}

// TestAdapterRun_NilDependencies tests that Run returns error when dependencies are nil.
func TestAdapterRun_NilDependencies(t *testing.T) {
	tests := []struct {
		name   string
		flight FlightRecorder
		repo   ExperienceRepository
	}{
		{"nil flight", nil, &mockExperienceRepository{}},
		{"nil repo", newMockFlightRecorder(), nil},
		{"both nil", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewFlightToExperienceAdapter(tt.flight, tt.repo)
			err := adapter.Run(context.Background())
			if err == nil {
				t.Error("expected error for nil dependency")
			}
		})
	}
}

// TestAdapterRun_SubscribeError tests handling of subscription errors.
func TestAdapterRun_SubscribeError(t *testing.T) {
	flight := newMockFlightRecorder()
	flight.subscriber.err = errors.New("subscription failed")
	repo := &mockExperienceRepository{}

	adapter := NewFlightToExperienceAdapter(flight, repo)
	err := adapter.Run(context.Background())
	if err == nil {
		t.Error("expected subscription error")
	}
}

// TestAdapterRun_ProcessesEvents tests that ares_events are processed correctly.
func TestAdapterRun_ProcessesEvents(t *testing.T) {
	defer discardLogs()()
	flight := newMockFlightRecorder()

	// Set up diagnostics for agent-1
	flight.diagnostics.SetReport("agent-1", &DiagnosticsReport{
		AgentID:   "agent-1",
		HasIssues: true,
		Records: []DiagnosticRecord{
			{
				ID:         "diag-1",
				AgentID:    "agent-1",
				TaskID:     "task-123",
				Category:   "tool_timeout",
				RootCause:  "connection timed out",
				Suggestion: "increase timeout to 30s",
				Severity:   7,
			},
		},
	})

	// Set up event
	flight.subscriber.ares_events = []*ares_events.Event{
		{
			ID:       "evt-1",
			StreamID: "agent-1",
			Type:     ares_events.EventTaskFailed,
			Payload:  map[string]any{"task_id": "task-123"},
		},
	}

	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(flight, repo)

	err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exps := repo.GetExperiences()
	if len(exps) != 1 {
		t.Fatalf("expected 1 experience, got %d", len(exps))
	}

	exp := exps[0]
	if exp.Type != TypeFailure {
		t.Errorf("expected type %s, got %s", TypeFailure, exp.Type)
	}
	if exp.AgentID != "agent-1" {
		t.Errorf("expected agent_id agent-1, got %s", exp.AgentID)
	}
	if exp.Score <= 0 || exp.Score > 1 {
		t.Errorf("expected score between 0 and 1, got %f", exp.Score)
	}
}

// TestBuildExperience_SkipsLowSeverity tests that low severity records are skipped.
func TestBuildExperience_SkipsLowSeverity(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:        "diag-low",
		Severity:  2, // Below threshold of 3
		Category:  "minor_issue",
		RootCause: "something minor",
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp != nil {
		t.Error("expected nil for low severity record")
	}
}

// TestBuildExperience_SkipsEmptyRecord tests that empty records are skipped.
func TestBuildExperience_SkipsEmptyRecord(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:        "diag-empty",
		Severity:  5,
		Category:  "",
		RootCause: "",
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp != nil {
		t.Error("expected nil for empty record")
	}
}

// TestBuildExperience_ValidRecord tests conversion of valid diagnostic record.
func TestBuildExperience_ValidRecord(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:         "diag-valid",
		AgentID:    "agent-1",
		TaskID:     "task-456",
		Category:   "llm_error",
		RootCause:  "API rate limit exceeded",
		Suggestion: "implement exponential backoff",
		Severity:   8,
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp == nil {
		t.Fatal("expected non-nil experience for valid record")
	}

	if exp.Type != TypeFailure {
		t.Errorf("expected type %s, got %s", TypeFailure, exp.Type)
	}
	if exp.Problem != "[llm_error] API rate limit exceeded" {
		t.Errorf("unexpected problem: %s", exp.Problem)
	}
	if exp.Solution != "implement exponential backoff" {
		t.Errorf("unexpected solution: %s", exp.Solution)
	}
	// Severity 8 should give score (11-8)/10 = 0.3
	expectedScore := 0.3
	if exp.Score != expectedScore {
		t.Errorf("expected score %f, got %f", expectedScore, exp.Score)
	}
}

// TestBuildExperience_GeneratesSolutionWhenMissing tests solution generation.
func TestBuildExperience_GeneratesSolutionWhenMissing(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:         "diag-no-suggestion",
		Severity:   5,
		Category:   "network_error",
		RootCause:  "connection refused",
		Suggestion: "", // Empty suggestion
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp == nil {
		t.Fatal("expected non-nil experience")
	}

	if exp.Solution == "" {
		t.Error("expected generated solution")
	}
}

// TestSeverityToScore tests the severity-to-score mapping function.
func TestSeverityToScore(t *testing.T) {
	tests := []struct {
		name     string
		severity int
		expected float64
	}{
		{"zero severity", 0, 1.0},
		{"low severity", 1, 1.0},
		{"medium low", 3, 0.8},
		{"medium", 5, 0.6},
		{"medium high", 7, 0.4},
		{"high", 9, 0.2},
		{"max severity", 10, 0.1},
		{"above max", 15, 0.1}, // Clamped to max
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := severityToScore(tt.severity)
			if result != tt.expected {
				t.Errorf("severity %d: expected %f, got %f", tt.severity, tt.expected, result)
			}
		})
	}
}

// TestExtractTaskID tests task ID extraction from event payload.
func TestExtractTaskID(t *testing.T) {
	tests := []struct {
		name     string
		event    *ares_events.Event
		expected string
	}{
		{
			name: "valid task ID",
			event: &ares_events.Event{
				Payload: map[string]any{"task_id": "task-123"},
			},
			expected: "task-123",
		},
		{
			name:     "nil event",
			event:    nil,
			expected: "",
		},
		{
			name: "nil payload",
			event: &ares_events.Event{
				Payload: nil,
			},
			expected: "",
		},
		{
			name: "missing key",
			event: &ares_events.Event{
				Payload: map[string]any{"other_key": "value"},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTaskID(tt.event)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

// TestAdapterRun_CreateError tests handling of create errors.
func TestAdapterRun_CreateError(t *testing.T) {
	defer discardLogs()()
	flight := newMockFlightRecorder()
	flight.diagnostics.SetReport("agent-1", &DiagnosticsReport{
		AgentID:   "agent-1",
		HasIssues: true,
		Records: []DiagnosticRecord{
			{
				ID:         "diag-err",
				AgentID:    "agent-1",
				TaskID:     "task-err",
				Category:   "error_cat",
				RootCause:  "root cause",
				Suggestion: "suggestion",
				Severity:   5,
			},
		},
	})

	flight.subscriber.ares_events = []*ares_events.Event{
		{
			ID:       "evt-err",
			StreamID: "agent-1",
			Type:     ares_events.EventTaskFailed,
			Payload:  map[string]any{"task_id": "task-err"},
		},
	}

	repo := &mockExperienceRepository{createErr: errors.New("db error")}
	adapter := NewFlightToExperienceAdapter(flight, repo)

	// Should not return error; errors are logged but not propagated
	err := adapter.Run(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestAdapterRun_NilEvent tests that nil ares_events are skipped gracefully.
func TestAdapterRun_NilEvent(t *testing.T) {
	defer discardLogs()()
	flight := newMockFlightRecorder()
	flight.subscriber.ares_events = []*ares_events.Event{nil} // Include a nil event

	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(flight, repo)

	err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No experiences should be created from nil ares_events
	exps := repo.GetExperiences()
	if len(exps) != 0 {
		t.Errorf("expected 0 experiences for nil event, got %d", len(exps))
	}
}

// TestAdapterRun_EmptyStreamID tests that ares_events without agent ID are skipped.
func TestAdapterRun_EmptyStreamID(t *testing.T) {
	defer discardLogs()()
	flight := newMockFlightRecorder()
	flight.subscriber.ares_events = []*ares_events.Event{
		{
			ID:       "evt-no-agent",
			StreamID: "", // Empty agent ID
			Type:     ares_events.EventTaskFailed,
			Payload:  map[string]any{"task_id": "task-123"},
		},
	}

	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(flight, repo)

	err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exps := repo.GetExperiences()
	if len(exps) != 0 {
		t.Errorf("expected 0 experiences for empty stream ID, got %d", len(exps))
	}
}

// TestAdapterRun_NoDiagnostics tests handling when no diagnostics exist for agent.
func TestAdapterRun_NoDiagnostics(t *testing.T) {
	defer discardLogs()()
	flight := newMockFlightRecorder()
	// Don't set any diagnostics report
	flight.subscriber.ares_events = []*ares_events.Event{
		{
			ID:       "evt-no-diag",
			StreamID: "agent-unknown",
			Type:     ares_events.EventTaskFailed,
			Payload:  map[string]any{"task_id": "task-789"},
		},
	}

	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(flight, repo)

	err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exps := repo.GetExperiences()
	if len(exps) != 0 {
		t.Errorf("expected 0 experiences when no diagnostics exist, got %d", len(exps))
	}
}

// TestBuildExperience_ThresholdBoundary tests severity exactly at threshold (3).
func TestBuildExperience_ThresholdBoundary(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:        "diag-threshold",
		Severity:  3, // Exactly at threshold
		Category:  "boundary_issue",
		RootCause: "boundary root cause",
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp == nil {
		t.Error("expected non-nil experience for severity exactly at threshold")
	}
}

// TestBuildExperience_MaxScore tests that maximum severity gives minimum score.
func TestBuildExperience_MaxScore(t *testing.T) {
	repo := &mockExperienceRepository{}
	adapter := NewFlightToExperienceAdapter(newMockFlightRecorder(), repo)

	record := DiagnosticRecord{
		ID:        "diag-max",
		Severity:  10, // Maximum severity
		Category:  "critical",
		RootCause: "critical failure",
	}

	exp := adapter.buildExperience(record, "agent-1")
	if exp == nil {
		t.Fatal("expected non-nil experience for max severity")
	}
	if exp.Score != 0.1 {
		t.Errorf("expected score 0.1 for max severity, got %f", exp.Score)
	}
}
