package taskfabric

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// TestFabricEventsPersistToStore verifies P2-C: with an event store attached,
// every task lifecycle transition is published as a task.* event on the
// task's stream, and the final state can be rebuilt from the store alone
// (cross-restart rebuild — Evidence-Driven).
func TestFabricEventsPersistToStore(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Complete("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Rebuild state from the store alone (cross-restart).
	events, err := store.Read(context.Background(), "t1", ares_events.ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) < 4 {
		t.Fatalf("want >=4 persisted events, got %d", len(events))
	}
	state := StateReady
	owner := ""
	for _, ev := range events {
		switch ev.Type {
		case ares_events.EventTaskCreated:
			state = StateReady
		case ares_events.EventTaskAcquired:
			state = StateLeased
			owner, _ = ev.Payload["agent_id"].(string)
		case ares_events.EventTaskStarted:
			state = StateRunning
		case ares_events.EventTaskCompleted:
			state = StateCompleted
		}
	}
	if state != StateCompleted || owner != "agent-a" {
		t.Fatalf("store rebuild must end COMPLETED by agent-a, got state=%s owner=%q", state, owner)
	}
}

// TestFabricNoStoreKeepsInMemoryLog verifies the fabric stays zero-value
// usable without a store: the in-memory log is the only sink.
func TestFabricNoStoreKeepsInMemoryLog(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(f.Events()) != 1 || f.Events()[0].Type != EventTaskCreated {
		t.Fatalf("want 1 in-memory created event, got %v", f.Events())
	}
}

// TestTaskEventTypeMapping verifies every fabric event type maps to a
// non-empty ares_events task.* event type.
func TestTaskEventTypeMapping(t *testing.T) {
	types := []EventType{
		EventTaskCreated, EventTaskReady, EventTaskAcquired, EventTaskStarted,
		EventTaskYielded, EventTaskCheckpointed, EventTaskPreempted,
		EventTaskReleased, EventTaskCompleted, EventTaskFailed,
		EventTaskExpired, EventTaskStolen,
	}
	for _, typ := range types {
		if taskEventType(typ) == "" {
			t.Fatalf("event type %s must map to a task.* event", typ)
		}
	}
}

// recordingEventStore wraps MemoryEventStore and records the ARRIVAL order of
// every Append so a test can assert the durable write order.
type recordingEventStore struct {
	inner *ares_events.MemoryEventStore
	mu    sync.Mutex
	order []ares_events.EventType
}

func newRecordingEventStore() *recordingEventStore {
	return &recordingEventStore{inner: ares_events.NewMemoryEventStore()}
}

func (s *recordingEventStore) Append(ctx context.Context, streamID string, events []*ares_events.Event, expectedVersion int64) error {
	s.mu.Lock()
	for _, e := range events {
		s.order = append(s.order, e.Type)
	}
	s.mu.Unlock()
	return s.inner.Append(ctx, streamID, events, expectedVersion)
}

func (s *recordingEventStore) Read(ctx context.Context, streamID string, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return s.inner.Read(ctx, streamID, opts)
}

func (s *recordingEventStore) ReadAll(ctx context.Context, opts ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return s.inner.ReadAll(ctx, opts)
}

func (s *recordingEventStore) Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	return s.inner.Subscribe(ctx, filter)
}

func (s *recordingEventStore) StreamVersion(ctx context.Context, streamID string) (int64, error) {
	return s.inner.StreamVersion(ctx, streamID)
}

// TestFabricConcurrentFlushPreservesCausalOrder locks the N7 contract: when a
// later-recorded durable event reaches flushAppends FIRST (its goroutine won
// the race to the flush point), the seq gate must still force it to wait for
// the earlier-recorded event — so the store's arrival order always matches the
// causal order, never the goroutine arrival order.
func TestFabricConcurrentFlushPreservesCausalOrder(t *testing.T) {
	store := newRecordingEventStore()
	f := NewFabric()
	f.store = store

	const rounds = 100
	for i := 0; i < rounds; i++ {
		// Production assigns seq from the fabric's monotonically increasing
		// flushSeq counter, so each round must use FRESH increasing values:
		// reusing seq 1/2 every round would let the gate condition
		// (p.seq > flushedSeq+1) go permanently false after round one and
		// stop testing anything.
		started := &pendingAppend{
			store: store, typ: EventTaskStarted, taskID: "t1",
			event: &ares_events.Event{Type: ares_events.EventTaskStarted, StreamID: "t1", ModuleName: "taskfabric"},
			seq:   uint64(2*i + 1),
		}
		completed := &pendingAppend{
			store: store, typ: EventTaskCompleted, taskID: "t1",
			event: &ares_events.Event{Type: ares_events.EventTaskCompleted, StreamID: "t1", ModuleName: "taskfabric"},
			seq:   uint64(2*i + 2),
		}

		var wg sync.WaitGroup
		wg.Add(2)
		// Launch the LATER-recorded event's flush first: without the seq
		// gate this inverts the stream on a regular basis; with the gate the
		// outcome is deterministic regardless of scheduling.
		go func() { defer wg.Done(); f.flushAppends(&[]*pendingAppend{completed}) }()
		go func() { defer wg.Done(); f.flushAppends(&[]*pendingAppend{started}) }()
		wg.Wait()
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Equal(t, 2*rounds, len(store.order))
	for i := 0; i < rounds; i++ {
		if store.order[2*i] != ares_events.EventTaskStarted {
			t.Fatalf("round %d: started must be appended before completed; got %s then %s",
				i, store.order[2*i], store.order[2*i+1])
		}
		if store.order[2*i+1] != ares_events.EventTaskCompleted {
			t.Fatalf("round %d: completed must follow started", i)
		}
	}
}

// TestFabricConcurrentLifecyclesStoreOrder exercises the seq gate under real
// multi-stream concurrency: every goroutine runs a full task lifecycle on its
// own stream, and each stream must come out of the store in causal order with
// contiguous versions (rebuild-from-store-alone replay correctness).
func TestFabricConcurrentLifecyclesStoreOrder(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	const cycles = 40
	var wg sync.WaitGroup
	errs := make([]error, cycles)
	for i := 0; i < cycles; i++ {
		id := fmt.Sprintf("cyc-%d", i)
		wg.Add(1)
		go func(idx int, taskID string) {
			defer wg.Done()
			if e := f.Create(&Task{ID: taskID, Capability: "rust"}); e != nil {
				errs[idx] = fmt.Errorf("Create %s: %w", taskID, e)
				return
			}
			epoch, e := f.Acquire(taskID, "agent-a", time.Minute)
			if e != nil {
				errs[idx] = fmt.Errorf("Acquire %s: %w", taskID, e)
				return
			}
			if e := f.Start(taskID, "agent-a", epoch); e != nil {
				errs[idx] = fmt.Errorf("Start %s: %w", taskID, e)
				return
			}
			if e := f.Complete(taskID, "agent-a", epoch); e != nil {
				errs[idx] = fmt.Errorf("Complete %s: %w", taskID, e)
			}
		}(i, id)
	}
	wg.Wait()

	for i := 0; i < cycles; i++ {
		require.NoError(t, errs[i])
		id := fmt.Sprintf("cyc-%d", i)
		events, err := store.Read(context.Background(), id, ares_events.ReadOptions{})
		require.NoError(t, err, "read %s", id)
		require.Len(t, events, 4, "stream %s must have exactly 4 durable events", id)
		want := []ares_events.EventType{
			ares_events.EventTaskCreated,
			ares_events.EventTaskAcquired,
			ares_events.EventTaskStarted,
			ares_events.EventTaskCompleted,
		}
		for j, ev := range events {
			require.Equal(t, want[j], ev.Type, "stream %s event %d order", id, j)
			require.Equal(t, int64(j+1), ev.Version, "stream %s version contiguity", id)
		}
	}
}
