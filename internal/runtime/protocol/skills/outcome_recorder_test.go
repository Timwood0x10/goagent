package ares_skills

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// emitSubTaskResult publishes an EventSubTaskResult through the memory store.
func emitSubTaskResult(t *testing.T, store ares_events.EventStore, task *models.Task, success bool) {
	t.Helper()
	payload := map[string]any{
		"task_id":  task.TaskID,
		"task":     task,
		"success":  success,
		"agent_id": "test_sub",
	}
	if !ares_events.Emit(context.Background(), store, "test_sub", ares_events.EventSubTaskResult, "test", payload) {
		t.Fatal("emit EventSubTaskResult failed")
	}
}

// pollRecorded waits until recorder.Recorded() reaches want, or fails after a
// bounded time.
func pollRecorded(t *testing.T, recorder *SkillOutcomeRecorder, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.Recorded() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recorder reached %d recorded, want %d (lastErr=%v)", recorder.Recorded(), want, recorder.LastErr())
}

// pollSkipped waits until recorder.Skipped() reaches want, or fails.
func pollSkipped(t *testing.T, recorder *SkillOutcomeRecorder, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.Skipped() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("recorder reached %d skipped, want %d", recorder.Skipped(), want)
}

// TestSkillOutcomeRecorderRecordsSuccess verifies a successful task whose
// UsedExperienceID names a skill persists a success-rate 1.0 prior into the
// catalog's Experience store, and the skill_experience tool can query it back
// (the record side of the design §11 loop).
func TestSkillOutcomeRecorderRecordsSuccess(t *testing.T) {
	cat := buildTestCatalog(t)
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	rec := NewSkillOutcomeRecorder(cat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rec.Start(ctx, store); err != nil {
		t.Fatalf("Start: %v", err)
	}

	task := &models.Task{
		TaskID:           "task-1",
		AgentType:        models.AgentTypeTop,
		UsedExperienceID: "audit", // the skill ID from the test catalog
		Payload:          map[string]any{"subAgentID": "code_01"},
	}
	emitSubTaskResult(t, store, task, true)
	pollRecorded(t, rec, 1)

	if rec.Skipped() != 0 {
		t.Fatalf("no events should be skipped, got %d", rec.Skipped())
	}
	// Verify the prior was recorded with the coarse task pattern (agent type).
	best, ok := cat.Experience().BestMatch("agent_top")
	if !ok {
		t.Fatal("experience must have a match for task type \"agent_top\"")
	}
	if best.Skill != "audit" || best.SuccessRate != 1.0 {
		t.Fatalf("unexpected prior: %+v", best)
	}
}

// TestSkillOutcomeRecorderRecordsFailure verifies a failed task records
// success-rate 0.0.
func TestSkillOutcomeRecorderRecordsFailure(t *testing.T) {
	cat := buildTestCatalog(t)
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	rec := NewSkillOutcomeRecorder(cat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rec.Start(ctx, store); err != nil {
		t.Fatalf("Start: %v", err)
	}

	task := &models.Task{
		TaskID:           "task-2",
		AgentType:        models.AgentTypeTop,
		UsedExperienceID: "audit",
	}
	emitSubTaskResult(t, store, task, false)
	pollRecorded(t, rec, 1)

	best, ok := cat.Experience().BestMatch("agent_top")
	if !ok {
		t.Fatal("experience must have a match")
	}
	if best.SuccessRate != 0.0 {
		t.Fatalf("failed task must record 0.0, got %+v", best)
	}
}

// TestSkillOutcomeRecorderSkipsUnassociated verifies events whose task has no
// UsedExperienceID (or a malformed payload) are skipped without error and
// without touching the Experience store.
func TestSkillOutcomeRecorderSkipsUnassociated(t *testing.T) {
	cat := buildTestCatalog(t)
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	rec := NewSkillOutcomeRecorder(cat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rec.Start(ctx, store); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// No UsedExperienceID.
	task := &models.Task{TaskID: "task-3", AgentType: models.AgentTypeTop}
	emitSubTaskResult(t, store, task, true)
	// Malformed payload (no task).
	ares_events.Emit(context.Background(), store, "test_sub", ares_events.EventSubTaskResult, "test",
		map[string]any{"task_id": "task-4"})

	pollSkipped(t, rec, 2)

	if rec.Recorded() != 0 {
		t.Fatalf("no outcome should be recorded, got %d", rec.Recorded())
	}
	if cat.Experience().Count() != 0 {
		t.Fatalf("experience must stay empty, got %d records", cat.Experience().Count())
	}
}

// TestSkillOutcomeRecorderOffline verifies nil catalog / nil store are no-ops.
func TestSkillOutcomeRecorderOffline(t *testing.T) {
	rec := NewSkillOutcomeRecorder(nil)
	if err := rec.Start(context.Background(), nil); err != nil {
		t.Fatalf("offline Start must be a no-op, got %v", err)
	}
}

// TestSkillOutcomeRecorderConcurrent verifies concurrent consumption is safe
// under -race: producers and the consumer race on the recorder and Experience
// store. Emits are paced in small batches because MemoryEventStore delivers to
// subscribers non-blockingly into a small channel buffer — an unbounded burst
// would be dropped before the consumer drains it, which is store semantics,
// not recorder behavior.
func TestSkillOutcomeRecorderConcurrent(t *testing.T) {
	cat := buildTestCatalog(t)
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	rec := NewSkillOutcomeRecorder(cat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rec.Start(ctx, store); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const (
		total = 40 // below the store's 64-event subscriber buffer
		batch = 10
	)
	for i := 0; i < total; i++ {
		task := &models.Task{
			TaskID:           "task-c" + string(rune('0'+i%10)) + itoaForTest(i),
			AgentType:        models.AgentTypeTop,
			UsedExperienceID: "audit",
		}
		ares_events.Emit(context.Background(), store, "test_sub", ares_events.EventSubTaskResult, "test",
			map[string]any{"task_id": task.TaskID, "task": task, "success": i%2 == 0})
		if (i+1)%batch == 0 {
			pollRecorded(t, rec, int64(i+1)) // let the consumer drain before the next burst
		}
	}
	if rec.Recorded() != total || rec.Skipped() != 0 {
		t.Fatalf("want %d recorded and 0 skipped, got recorded=%d skipped=%d", total, rec.Recorded(), rec.Skipped())
	}
}

// itoaForTest formats an int without fmt (keeps this test file dependency-light).
func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestSkillOutcomeRecorderUsesPrecisePattern verifies the precise task
// description (task.Payload["task_desc"], stored by the planner) is used as
// the experience pattern instead of the coarse agent type — BestMatch on the
// precise description must hit.
func TestSkillOutcomeRecorderUsesPrecisePattern(t *testing.T) {
	cat := buildTestCatalog(t)
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	rec := NewSkillOutcomeRecorder(cat)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rec.Start(ctx, store); err != nil {
		t.Fatalf("Start: %v", err)
	}

	task := &models.Task{
		TaskID:           "task-precise",
		AgentType:        models.AgentTypeTop,
		UsedExperienceID: "audit",
		Payload:          map[string]any{"task_desc": "audit the OWASP login flow"},
	}
	emitSubTaskResult(t, store, task, true)
	pollRecorded(t, rec, 1)

	best, ok := cat.Experience().BestMatch("audit the OWASP login flow")
	if !ok {
		t.Fatal("precise task_desc pattern must be matchable via BestMatch")
	}
	if best.Skill != "audit" || best.SuccessRate != 1.0 {
		t.Fatalf("unexpected prior: %+v", best)
	}
}

// TestSkillTaskPatternPreciseFirstFallbackChain verifies the derivation
// order: task_desc → agent type → subAgentID → "default".
func TestSkillTaskPatternPreciseFirstFallbackChain(t *testing.T) {
	// Precise task_desc wins (trimmed).
	if got := skillTaskPattern(&models.Task{
		AgentType: models.AgentTypeTop,
		Payload:   map[string]any{"task_desc": "  precise desc  "},
	}); got != "precise desc" {
		t.Fatalf("want precise desc, got %q", got)
	}
	// Blank task_desc falls back to the agent type.
	if got := skillTaskPattern(&models.Task{
		AgentType: models.AgentTypeTop,
		Payload:   map[string]any{"task_desc": "  ", "subAgentID": "code_01"},
	}); got != string(models.AgentTypeTop) {
		t.Fatalf("want agent-type fallback, got %q", got)
	}
	// No agent type → subAgentID.
	if got := skillTaskPattern(&models.Task{
		Payload: map[string]any{"subAgentID": "code_01"},
	}); got != "code_01" {
		t.Fatalf("want subAgentID fallback, got %q", got)
	}
	// Nothing → default.
	if got := skillTaskPattern(&models.Task{}); got != "default" {
		t.Fatalf("want default, got %q", got)
	}
}

// TestSkillTaskPatternTruncatesTaskDesc verifies the persisted pattern never
// carries more than maxTaskPatternLen bytes of the original user input — full
// task descriptions must not be written verbatim to the Experience JSON file.
func TestSkillTaskPatternTruncatesTaskDesc(t *testing.T) {
	long := strings.Repeat("x", maxPatternLength+100) + "SENSITIVE-END"
	got := skillTaskPattern(&models.Task{
		AgentType: models.AgentTypeTop,
		Payload:   map[string]any{"task_desc": long},
	})
	if len(got) != maxPatternLength {
		t.Fatalf("want pattern truncated to %d bytes, got %d", maxPatternLength, len(got))
	}
	if strings.Contains(got, "SENSITIVE-END") {
		t.Fatal("truncation must not leak the tail of the original input")
	}
	// Short descriptions pass through intact.
	short := skillTaskPattern(&models.Task{
		Payload: map[string]any{"task_desc": "audit login"},
	})
	if short != "audit login" {
		t.Fatalf("short description must pass through, got %q", short)
	}
}
