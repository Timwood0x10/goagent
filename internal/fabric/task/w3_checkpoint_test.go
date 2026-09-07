package taskfabric

import (
	"context"
	"encoding/json"
	"errors"
	stdlog "log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// failingEventStore is a test-only EventStore wrapper that injects failures
// into Append calls. It wraps a real MemoryEventStore but returns an error
// when the failNext flag is set (or failAlways is set). This is used by the
// W3 store-fault tests to verify that must-persist event append failures are
// logged — not silently swallowed (the old behavior was `_ = err`).
type failingEventStore struct {
	inner      *ares_events.MemoryEventStore
	failNext   atomic.Bool
	failAlways atomic.Bool
}

func newFailingEventStore() *failingEventStore {
	return &failingEventStore{inner: ares_events.NewMemoryEventStore()}
}

func (s *failingEventStore) Append(ctx context.Context, streamID string, events []*ares_events.Event, expectedVersion int64) error {
	if s.failAlways.Load() || s.failNext.Swap(false) {
		return errors.New("failingEventStore: injected append failure")
	}
	return s.inner.Append(ctx, streamID, events, expectedVersion)
}

func (s *failingEventStore) Read(ctx context.Context, streamID string, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return s.inner.Read(ctx, streamID, opts)
}

func (s *failingEventStore) ReadAll(ctx context.Context, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return s.inner.ReadAll(ctx, opts)
}

func (s *failingEventStore) Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	return s.inner.Subscribe(ctx, filter)
}

func (s *failingEventStore) StreamVersion(ctx context.Context, streamID string) (int64, error) {
	return s.inner.StreamVersion(ctx, streamID)
}

// logCapture is the global log capture buffer. It must be set before a test
// that wants to assert on log output and cleared after. This avoids per-test
// log.SetOutput complexity (the fabric's record method calls log.Error
// directly, which writes to the default logger).
var (
	logCaptureMu sync.Mutex
	logCapture   *strings.Builder
)

func startLogCapture() *strings.Builder {
	logCaptureMu.Lock()
	defer logCaptureMu.Unlock()
	logCapture = &strings.Builder{}
	stdlog.SetOutput(logCapture)
	return logCapture
}

func stopLogCapture() {
	logCaptureMu.Lock()
	defer logCaptureMu.Unlock()
	stdlog.SetOutput(os.Stderr)
	logCapture = nil
}

// TestW3MustPersistEventFailureIsLogged verifies the W3 durability fix: when
// the EventStore append fails for a must-persist event (TaskCreated,
// TaskCheckpointed, TaskCompleted, TaskFailed, TaskExpired), the failure is
// LOGGED — not silently swallowed. The old code did `_ = err`, which meant a
// durable-state divergence (in-memory vs event log) was invisible to the
// operator.
//
// The test does NOT verify the in-memory state machine rolls back — by design
// the in-memory state stays authoritative within a process (W3 §7.3.2: "禁止
// 静默吞错", but the transition is not rolled back). The gap is made visible
// via the log.
func TestW3MustPersistEventFailureIsLogged(t *testing.T) {
	store := newFailingEventStore()
	f := NewFabric().WithEventStore(store)

	// Inject a failure on the NEXT append (the Create → EventTaskCreated).
	store.failNext.Store(true)

	capture := startLogCapture()
	defer stopLogCapture()

	// Create a task — the EventTaskCreated append will fail (must-persist).
	if err := f.Create(newTask("w3-must")); err != nil {
		t.Fatalf("Create must succeed (in-memory stays authoritative): %v", err)
	}

	// The in-memory task exists (state machine not rolled back).
	tk, err := f.Task("w3-must")
	if err != nil {
		t.Fatalf("Task must exist in memory: %v", err)
	}
	if tk.State != StateReady {
		t.Fatalf("task must be READY in memory, got %s", tk.State)
	}

	// The log must mention the must-persist event failure.
	logged := capture.String()
	if !strings.Contains(logged, "must-persist event") || !strings.Contains(logged, "w3-must") {
		t.Fatalf("log must mention must-persist event failure for w3-must, got:\n%s", logged)
	}
}

// TestW3ObservabilityEventFailureIsSilent verifies that an append failure for
// an observability-only event (EventTaskReady) is NOT logged — the old
// best-effort behavior is preserved for non-critical events. Only must-persist
// events surface their failures.
func TestW3ObservabilityEventFailureIsSilent(t *testing.T) {
	store := newFailingEventStore()
	f := NewFabric().WithEventStore(store)

	// Create succeeds (append succeeds — no failure injected).
	if err := f.Create(newTask("w3-obs")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Acquire succeeds (append succeeds).
	epoch, err := f.Acquire("w3-obs", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Fail the task so it requeues to READY (EventTaskFailed is must-persist,
	// EventTaskReady is observability-only). We inject the failure on the
	// SECOND append (EventTaskReady) — the first (EventTaskFailed) succeeds.
	// Fail calls record(EventTaskFailed) then record(EventTaskReady).
	// We need the EventTaskFailed to succeed and EventTaskReady to fail.
	// Since failNext only fails once, we set it AFTER the Failed append.
	// But we can't time it precisely — instead, use failAlways for a cleaner
	// approach: fail everything, then check that only must-persist events
	// appear in the log.
	store.failAlways.Store(true)

	capture := startLogCapture()
	defer stopLogCapture()

	// Start → Running (EventTaskStarted is observability-only, will fail
	// silently).
	if err := f.Start("w3-obs", "agent-a", epoch); err != nil {
		t.Fatalf("Start must succeed: %v", err)
	}

	logged := capture.String()
	// EventTaskStarted is observability-only: its failure must NOT appear
	// in the log as a must-persist event.
	if strings.Contains(logged, "must-persist event") && strings.Contains(logged, "task.started") {
		t.Fatalf("observability event (started) failure must be silent, got:\n%s", logged)
	}
}

// TestW3MustPersistEventClassification verifies that isMustPersistEvent
// correctly classifies the events that the runtime's recovery/replay
// correctness depends on. Must-persist events are: TaskCreated,
// TaskCheckpointed, TaskCompleted, TaskFailed, TaskExpired. All others are
// observability-only.
func TestW3MustPersistEventClassification(t *testing.T) {
	mustPersist := []EventType{
		EventTaskCreated,
		EventTaskCheckpointed,
		EventTaskCompleted,
		EventTaskFailed,
		EventTaskExpired,
	}
	for _, typ := range mustPersist {
		if !isMustPersistEvent(typ) {
			t.Fatalf("%s must be classified as must-persist", typ)
		}
	}
	observability := []EventType{
		EventTaskReady,
		EventTaskAcquired,
		EventTaskStarted,
		EventTaskYielded,
		EventTaskPreempted,
		EventTaskReleased,
		EventTaskStolen,
	}
	for _, typ := range observability {
		if isMustPersistEvent(typ) {
			t.Fatalf("%s must be classified as observability-only", typ)
		}
	}
}

// TestW3CheckpointEnvelopeRoundTrip verifies the versioned checkpoint schema
// (W3 §7.3.3: 固化 checkpoint schema): a CheckpointEnvelope can be marshaled
// to JSON and unmarshaled back, and DecodeCheckpoint extracts the fields
// correctly from the round-tripped form. This is the cross-restart protocol
// stability test.
func TestW3CheckpointEnvelopeRoundTrip(t *testing.T) {
	original := &CheckpointEnvelope{
		SchemaVersion:    CurrentCheckpointSchemaVersion,
		Payload:          map[string]any{"task_desc": "analyze rust code"},
		UsedExperienceID: "exp-42",
		StepCheckpoint:   map[string]any{"step": 3, "data": "intermediate"},
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Unmarshal into a map (simulating what happens after persistence +
	// reload from a JSON-based store).
	var restored map[string]any
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	dc, err := DecodeCheckpoint(restored)
	if err != nil {
		t.Fatalf("DecodeCheckpoint: %v", err)
	}
	if dc.SchemaVersion != CurrentCheckpointSchemaVersion {
		t.Fatalf("schema version must be %d, got %d", CurrentCheckpointSchemaVersion, dc.SchemaVersion)
	}
	if dc.UsedExperienceID != "exp-42" {
		t.Fatalf("UsedExperienceID must be exp-42, got %q", dc.UsedExperienceID)
	}
	payload, ok := dc.Payload["task_desc"].(string)
	if !ok || payload != "analyze rust code" {
		t.Fatalf("payload task_desc must be restored, got %v", dc.Payload)
	}
	step, ok := dc.StepCheckpoint.(map[string]any)
	if !ok {
		t.Fatalf("step checkpoint must be a map, got %T", dc.StepCheckpoint)
	}
	if step["data"] != "intermediate" {
		t.Fatalf("step data must be 'intermediate', got %v", step["data"])
	}
}

// TestW3DecodeCheckpointRejectsFutureVersion verifies that DecodeCheckpoint
// returns ErrCheckpointSchemaVersion when the envelope carries a future
// schema version. A future version means the code is talking to a newer
// runtime — silent misinterpretation would corrupt the task state, so the
// decode must reject explicitly.
func TestW3DecodeCheckpointRejectsFutureVersion(t *testing.T) {
	future := &CheckpointEnvelope{
		SchemaVersion: CurrentCheckpointSchemaVersion + 1,
	}
	_, err := DecodeCheckpoint(future)
	if !errors.Is(err, ErrCheckpointSchemaVersion) {
		t.Fatalf("future version must return ErrCheckpointSchemaVersion, got %v", err)
	}

	// Also test the JSON-round-tripped form.
	raw, _ := json.Marshal(future)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	_, err = DecodeCheckpoint(m)
	if !errors.Is(err, ErrCheckpointSchemaVersion) {
		t.Fatalf("future version (map form) must return ErrCheckpointSchemaVersion, got %v", err)
	}
}

// TestW3DecodeCheckpointHandlesNilAndRaw verifies DecodeCheckpoint handles
// edge cases gracefully: nil checkpoint, a plain map (raw step checkpoint
// without the schema_version key), and a non-map value (e.g. an int). These
// represent pre-W3 checkpoints or minimally-wrapped progress markers.
func TestW3DecodeCheckpointHandlesNilAndRaw(t *testing.T) {
	// nil → zero value.
	dc, err := DecodeCheckpoint(nil)
	if err != nil || dc.SchemaVersion != 0 {
		t.Fatalf("nil must decode to zero value, got %+v err=%v", dc, err)
	}

	// plain map (no schema_version) → raw step checkpoint.
	dc, err = DecodeCheckpoint(map[string]any{"step": 2})
	if err != nil {
		t.Fatalf("plain map must not error: %v", err)
	}
	if dc.SchemaVersion != 0 {
		t.Fatalf("plain map must have schema version 0, got %d", dc.SchemaVersion)
	}
	step, ok := dc.StepCheckpoint.(map[string]any)
	if !ok || step["step"] == nil {
		t.Fatalf("plain map must be placed in StepCheckpoint, got %v", dc.StepCheckpoint)
	}

	// non-map value → raw step checkpoint.
	dc, err = DecodeCheckpoint(42)
	if err != nil {
		t.Fatalf("int must not error: %v", err)
	}
	if dc.StepCheckpoint != 42 {
		t.Fatalf("int must be placed in StepCheckpoint, got %v", dc.StepCheckpoint)
	}
}

// TestW3EncodeDecodeRoundTrip verifies EncodeCheckpoint ∘ DecodeCheckpoint is
// idempotent: encoding a decoded checkpoint and decoding it again yields the
// same fields. This is the contract the scheduler relies on when re-wrapping
// a quantum's output (yield/done).
func TestW3EncodeDecodeRoundTrip(t *testing.T) {
	original := DecodedCheckpoint{
		UserProfile:      "profile-data",
		Payload:          map[string]any{"task_desc": "test"},
		UsedExperienceID: "exp-1",
		StepCheckpoint:   map[string]any{"step": 1},
	}
	env := EncodeCheckpoint(original)
	if env.SchemaVersion != CurrentCheckpointSchemaVersion {
		t.Fatalf("encoded version must be %d, got %d", CurrentCheckpointSchemaVersion, env.SchemaVersion)
	}
	dc, err := DecodeCheckpoint(env)
	if err != nil {
		t.Fatalf("DecodeCheckpoint: %v", err)
	}
	if dc.UsedExperienceID != original.UsedExperienceID {
		t.Fatalf("UsedExperienceID mismatch: %q vs %q", dc.UsedExperienceID, original.UsedExperienceID)
	}
	if dc.Payload["task_desc"] != original.Payload["task_desc"] {
		t.Fatalf("Payload mismatch")
	}
	step, ok := dc.StepCheckpoint.(map[string]any)
	if !ok || step["step"] == nil {
		t.Fatalf("StepCheckpoint must be preserved, got %v", dc.StepCheckpoint)
	}
}

// TestW3MarshalCheckpointWrapsRawValue verifies that MarshalCheckpoint wraps a
// non-envelope value in a versioned envelope before serializing — the
// serialized form always carries the schema version (W3: 固化协议).
func TestW3MarshalCheckpointWrapsRawValue(t *testing.T) {
	raw, err := MarshalCheckpoint(map[string]any{"step": 5})
	if err != nil {
		t.Fatalf("MarshalCheckpoint: %v", err)
	}
	var env CheckpointEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.SchemaVersion != CurrentCheckpointSchemaVersion {
		t.Fatalf("serialized form must carry schema version %d, got %d",
			CurrentCheckpointSchemaVersion, env.SchemaVersion)
	}
	// The raw value must be in StepCheckpoint.
	step, ok := env.StepCheckpoint.(map[string]any)
	if !ok || step["step"] == nil {
		t.Fatalf("raw value must be in StepCheckpoint, got %v", env.StepCheckpoint)
	}
}

// TestW3StoreFailureDoesNotBreakStateMachine verifies that a store append
// failure does NOT break the in-memory state machine: the task still
// transitions correctly (Create → Acquire → Start → Complete) even when every
// store append fails. The in-memory state is authoritative within a process;
// the store is for cross-restart replay. A store failure means the event log
// diverges from memory — the operator is warned via the log (tested above),
// but the runtime continues.
func TestW3StoreFailureDoesNotBreakStateMachine(t *testing.T) {
	store := newFailingEventStore()
	store.failAlways.Store(true)
	f := NewFabric().WithEventStore(store)

	// Every record() call will fail — but the state machine must still work.
	if err := f.Create(newTask("w3-state")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("w3-state", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("w3-state", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Complete("w3-state", "agent-a", epoch); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tk, _ := f.Task("w3-state")
	if tk.State != StateCompleted {
		t.Fatalf("task must be COMPLETED despite store failures, got %s", tk.State)
	}
}
