package evolution

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// mockAdapterForScheduler implements FlightToExperienceAdapter for testing scheduler.
type mockAdapterForScheduler struct {
	mu          sync.Mutex
	runCount    int
	runErr      error
	runComplete chan struct{} // Channel to signal when Run() has completed.
}

func newMockAdapterForScheduler() *mockAdapterForScheduler {
	return &mockAdapterForScheduler{
		runComplete: make(chan struct{}, 1),
	}
}

func (m *mockAdapterForScheduler) Run(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runCount++
	err := m.runErr
	// Signal that Run() has completed for deterministic synchronization in tests.
	select {
	case m.runComplete <- struct{}{}:
	default:
	}
	return err
}

// TestNewEvolutionScheduler tests constructor with default values.
func TestNewEvolutionScheduler(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter)
	if scheduler == nil {
		t.Fatal("expected non-nil scheduler")
	}
	if scheduler.minInterval != 5*time.Minute {
		t.Errorf("expected default minInterval 5m, got %v", scheduler.minInterval)
	}
	if scheduler.trigger != TriggerOnIdle {
		t.Errorf("expected default trigger TriggerOnIdle, got %v", scheduler.trigger)
	}
	if scheduler.enabled.Load() {
		t.Error("expected disabled by default")
	}
}

// TestNewEvolutionScheduler_WithOptions tests constructor with custom options.
func TestNewEvolutionScheduler_WithOptions(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter,
		WithMinInterval(10*time.Second),
		WithTrigger(TriggerOnThreshold),
		WithEnabled(true),
	)

	if scheduler.minInterval != 10*time.Second {
		t.Errorf("expected minInterval 10s, got %v", scheduler.minInterval)
	}
	if scheduler.trigger != TriggerOnThreshold {
		t.Errorf("expected trigger TriggerOnThreshold, got %v", scheduler.trigger)
	}
	if !scheduler.enabled.Load() {
		t.Error("expected enabled")
	}
}

// TestOnAgentEnd_Disabled tests that OnAgentEnd is a no-op when disabled.
func TestOnAgentEnd_Disabled(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter,
		WithEnabled(false), // Explicitly disabled
	)

	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-1"})

	if adapter.runCount != 0 {
		t.Errorf("expected 0 adapter runs when disabled, got %d", adapter.runCount)
	}
}

// TestOnAgentEnd_NilAdapter tests that OnAgentEnd handles nil adapter gracefully.
func TestOnAgentEnd_NilAdapter(t *testing.T) {
	defer discardLogs()()
	reg := newMockCallbackRegistrarForTest()

	scheduler := NewEvolutionScheduler(reg, nil,
		WithEnabled(true),
	)

	// Should not panic
	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-1"})
}

// TestOnAgentEnd_Enabled tests that OnAgentEnd triggers adapter when enabled.
func TestOnAgentEnd_Enabled(t *testing.T) {
	defer discardLogs()()
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter,
		WithEnabled(true),
		WithMinInterval(time.Nanosecond), // Very short interval for testing
	)

	// TriggerOnIdle requires either score degradation (drop >= 15%) or
	// scoreCount >= 100 for periodic exploration. Since scoreWindowSize=50
	// caps the sliding window at 50 entries, we use degradation:
	//   - 40 scores of 1.0 fill most of the window
	//   - 10 scores of 0.0 create recent avg = 0.0 vs overall avg = 0.8
	//   - drop = (0.8-0.0)/0.8 = 100% >> 15% threshold
	for i := 0; i < 40; i++ {
		scheduler.RecordScore(1.0)
	}
	for i := 0; i < 10; i++ {
		scheduler.RecordScore(0.0)
	}

	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-1"})

	// Wait for the adapter Run() to complete deterministically.
	<-adapter.runComplete

	if adapter.runCount == 0 {
		t.Error("expected at least 1 adapter run when enabled")
	}
}

// TestOnAgentEnd_MinIntervalProtection tests that minimum interval is respected.
func TestOnAgentEnd_MinIntervalProtection(t *testing.T) {
	defer discardLogs()()
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter,
		WithEnabled(true),
		WithMinInterval(1*time.Hour), // Long interval
	)

	// Populate scores to pass shouldEvolve in TriggerOnIdle mode.
	for i := 0; i < 40; i++ {
		scheduler.RecordScore(1.0)
	}
	for i := 0; i < 10; i++ {
		scheduler.RecordScore(0.0)
	}

	// First call should succeed
	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-1"})
	<-adapter.runComplete
	firstRun := adapter.runCount

	// Second call should be blocked by interval
	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-2"})

	if secondRun := adapter.runCount; secondRun != firstRun {
		t.Errorf("expected run count to stay at %d after min interval block, got %d", firstRun, secondRun)
	}
}

// TestRegister_SubscribesToAgentStopped tests that Register subscribes to the
// EventStore for agent stopped events.
func TestRegister_SubscribesToAgentStopped(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter)
	defer scheduler.Shutdown()

	scheduler.Register()

	filters := reg.subscriptionFilters()
	if len(filters) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(filters))
	}
	want := []ares_events.EventType{
		ares_events.EventAgentStopped,
		ares_events.EventTaskCompleted,
		ares_events.EventTaskFailed,
	}
	got := filters[0].Types
	if len(got) != len(want) {
		t.Errorf("expected subscription types %v, got %v", want, got)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("expected subscription types %v, got %v", want, got)
			break
		}
	}
}

// TestRegister_NilSubscriber tests that Register handles a nil subscriber gracefully.
func TestRegister_NilSubscriber(t *testing.T) {
	defer discardLogs()()
	adapter := newMockAdapterForScheduler()
	scheduler := NewEvolutionScheduler(nil, adapter)

	// Should not panic
	scheduler.Register()
}

// TestShouldEvolve_Defaults tests shouldEvolve returns true when preconditions are met.
func TestShouldEvolve_Defaults(t *testing.T) {
	defer discardLogs()()
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()

	scheduler := NewEvolutionScheduler(reg, adapter,
		WithEnabled(true),
	)

	// TriggerOnIdle needs score degradation or 100+ scores (impossible
	// due to scoreWindowSize=50). Provide degradation data.
	for i := 0; i < 40; i++ {
		scheduler.RecordScore(1.0)
	}
	for i := 0; i < 10; i++ {
		scheduler.RecordScore(0.0)
	}

	result := scheduler.shouldEvolve(context.Background(), CallbackData{AgentID: "agent-1"})
	if !result {
		t.Error("expected shouldEvolve=true with enabled scheduler and score degradation")
	}
}

// TestSetEnabled_IsEnabled tests enable/disable functionality.
func TestSetEnabled_IsEnabled(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	scheduler := NewEvolutionScheduler(reg, nil)

	if scheduler.IsEnabled() {
		t.Error("expected disabled by default")
	}

	scheduler.SetEnabled(true)
	if !scheduler.IsEnabled() {
		t.Error("expected enabled after SetEnabled(true)")
	}

	scheduler.SetEnabled(false)
	if scheduler.IsEnabled() {
		t.Error("expected disabled after SetEnabled(false)")
	}
}

// TestLastRunTime tests last run time tracking.
func TestLastRunTime(t *testing.T) {
	defer discardLogs()()
	reg := newMockCallbackRegistrarForTest()
	adapter := newMockAdapterForScheduler()
	scheduler := NewEvolutionScheduler(reg, adapter,
		WithEnabled(true),
		WithMinInterval(time.Nanosecond),
	)

	initialTime := scheduler.LastRunTime()
	if !initialTime.IsZero() {
		t.Error("expected zero LastRunTime initially")
	}

	// TriggerOnIdle requires score degradation (scoreWindowSize=50 caps
	// the sliding window, so scoreCount can never reach 100).
	for i := 0; i < 40; i++ {
		scheduler.RecordScore(1.0)
	}
	for i := 0; i < 10; i++ {
		scheduler.RecordScore(0.0)
	}

	scheduler.OnAgentEnd(context.Background(), CallbackData{AgentID: "agent-1"})
	<-adapter.runComplete

	lastRun := scheduler.LastRunTime()
	if lastRun.IsZero() {
		t.Error("expected non-zero LastRunTime after run")
	}
	if lastRun.Before(initialTime) {
		t.Error("expected LastRunTime to be after initial time")
	}
}

// TestEvolutionTrigger_Constants tests that trigger constants have correct values.
func TestEvolutionTrigger_Constants(t *testing.T) {
	if TriggerOnIdle != 1 {
		t.Errorf("expected TriggerOnIdle = 1, got %d", TriggerOnIdle)
	}
	if TriggerOnThreshold != 2 {
		t.Errorf("expected TriggerOnThreshold = 2, got %d", TriggerOnThreshold)
	}
	if TriggerOnDemand != 3 {
		t.Errorf("expected TriggerOnDemand = 3, got %d", TriggerOnDemand)
	}
}

// TestWithMinInterval_ZeroValue tests that zero min interval is rejected.
func TestWithMinInterval_ZeroValue(t *testing.T) {
	reg := newMockCallbackRegistrarForTest()
	scheduler := NewEvolutionScheduler(reg, nil,
		WithMinInterval(0), // Zero value should be ignored
	)

	if scheduler.minInterval == 0 {
		t.Error("expected default value when zero is passed")
	}
}
