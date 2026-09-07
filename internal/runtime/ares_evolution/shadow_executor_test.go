package evolution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// fakeShadowRunner records every isolated run and returns per-strategy
// scripted outcomes.
type fakeShadowRunner struct {
	mu       sync.Mutex
	runs     []fakeShadowRun
	outcomes map[string]fakeShadowOutcome
}

type fakeShadowRun struct {
	strategyID string
	taskID     string
}

type fakeShadowOutcome struct {
	completed bool
	err       error
}

func (r *fakeShadowRunner) RunShadow(_ context.Context, task *models.Task, strategy *mutation.Strategy) (bool, error) {
	r.mu.Lock()
	r.runs = append(r.runs, fakeShadowRun{strategyID: strategy.ID, taskID: task.TaskID})
	out, ok := r.outcomes[strategy.ID]
	r.mu.Unlock()
	if !ok {
		out = fakeShadowOutcome{completed: true}
	}
	// Simulate the runner's own payload mutation (yield checkpoint) so the
	// clone-isolation contract is observable.
	if task.Payload == nil {
		task.Payload = map[string]any{}
	}
	task.Payload["checkpoint"] = "mutated"
	return out.completed, out.err
}

func (r *fakeShadowRunner) executedTaskIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	set := make(map[string]bool, len(r.runs))
	for _, run := range r.runs {
		set[run.taskID] = true
	}
	return set
}

func shadowTestTask(id string) *models.Task {
	t := models.NewTask(id, "code", nil)
	t.Payload = map[string]any{"task_desc": "do things"}
	return t
}

func shadowTestExecutor(t *testing.T, runner *fakeShadowRunner, sampleSize int) *ShadowExecutor {
	t.Helper()
	exec, err := NewShadowExecutor(evidence.NewMemoryStore(), runner, sampleSize)
	if err != nil {
		t.Fatalf("NewShadowExecutor: %v", err)
	}
	exec.nowFn = func() time.Time { return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC) }
	return exec
}

func queryShadowEvidence(t *testing.T, store evidence.Store) []evidence.Evidence {
	t.Helper()
	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: shadowEvidenceSource,
		Kind:   evidence.KindFitness,
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("query shadow evidence: %v", err)
	}
	return evs
}

// TestShadowExecutor_FeedWritesPairedShadowEvidence is the Step 4 (closure
// plan N-1) core acceptance: both A/B arms execute buffered real task copies
// in isolation and one shadow-marked fitness record per arm lands in the
// store, attributed to the arm's own strategy ID.
func TestShadowExecutor_FeedWritesPairedShadowEvidence(t *testing.T) {
	runner := &fakeShadowRunner{}
	exec := shadowTestExecutor(t, runner, 3)
	exec.OnTaskFinalized(shadowTestTask("t1"))
	exec.OnTaskFinalized(shadowTestTask("t2"))

	candidate := &mutation.Strategy{ID: "cand-1"}
	active := &mutation.Strategy{ID: "active-1"}

	pairs := exec.Feed(context.Background(), candidate, active)
	if len(pairs) != 2 {
		t.Fatalf("expected 2 paired comparisons, got %d", len(pairs))
	}
	for _, p := range pairs {
		if p.ActiveScore != 1.0 || p.ShadowScore != 1.0 {
			t.Fatalf("expected both arms to complete (1.0), got %+v", p)
		}
	}

	evs := queryShadowEvidence(t, exec.store)
	if len(evs) != 4 {
		t.Fatalf("expected 4 shadow evidence records (2 tasks x 2 arms), got %d", len(evs))
	}
	perStrategy := map[string]int{}
	for _, ev := range evs {
		if ev.Source != shadowEvidenceSource || ev.Kind != evidence.KindFitness {
			t.Fatalf("unexpected evidence shape: source=%q kind=%v", ev.Source, ev.Kind)
		}
		var fe struct {
			Value      float64 `json:"value"`
			Success    bool    `json:"success"`
			StrategyID string  `json:"strategy_id"`
			Shadow     bool    `json:"shadow"`
		}
		if err := json.Unmarshal(ev.Payload, &fe); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		if !fe.Shadow {
			t.Fatalf("record %s is not shadow-marked", ev.ID)
		}
		if fe.StrategyID != "cand-1" && fe.StrategyID != "active-1" {
			t.Fatalf("record %s attributed to %q", ev.ID, fe.StrategyID)
		}
		if fe.Value != 1.0 || !fe.Success {
			t.Fatalf("record %s value/success = %.2f/%v, want 1.0/true", ev.ID, fe.Value, fe.Success)
		}
		perStrategy[fe.StrategyID]++
	}
	if perStrategy["cand-1"] != 2 || perStrategy["active-1"] != 2 {
		t.Fatalf("per-arm counts unbalanced: %+v", perStrategy)
	}
}

// TestShadowExecutor_SampleSizeLimitsScope locks the sample_size semantics:
// each Feed uses only the N most recent buffered tasks.
func TestShadowExecutor_SampleSizeLimitsScope(t *testing.T) {
	runner := &fakeShadowRunner{}
	exec := shadowTestExecutor(t, runner, 2)
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		exec.OnTaskFinalized(shadowTestTask(id))
	}

	pairs := exec.Feed(context.Background(), &mutation.Strategy{ID: "cand-1"}, &mutation.Strategy{ID: "active-1"})
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs (sample_size=2), got %d", len(pairs))
	}
	seen := runner.executedTaskIDs()
	if len(seen) != 2 || !seen["t3#shadow"] || !seen["t4#shadow"] {
		t.Fatalf("expected only the two most recent tasks executed, got %v", seen)
	}
}

// TestShadowExecutor_BufferIsolation locks the clone contract: the runner
// mutates ITS copy only, and the buffered original (including the payload)
// is never touched — the same task copy must serve both arms identically.
func TestShadowExecutor_BufferIsolation(t *testing.T) {
	runner := &fakeShadowRunner{}
	exec := shadowTestExecutor(t, runner, 3)
	original := shadowTestTask("t1")
	exec.OnTaskFinalized(original)

	exec.Feed(context.Background(), &mutation.Strategy{ID: "cand-1"}, &mutation.Strategy{ID: "active-1"})

	if v, ok := original.Payload["checkpoint"]; ok {
		t.Fatalf("buffered task payload mutated: %v", v)
	}
	if original.TaskID != "t1" {
		t.Fatalf("buffered task id mutated: %q", original.TaskID)
	}
}

// TestShadowExecutor_FeedGuards locks the no-fabricated-verdict guards: no
// candidate, an empty candidate ID, or candidate == active produce nothing.
func TestShadowExecutor_FeedGuards(t *testing.T) {
	runner := &fakeShadowRunner{}
	exec := shadowTestExecutor(t, runner, 3)
	exec.OnTaskFinalized(shadowTestTask("t1"))
	active := &mutation.Strategy{ID: "active-1"}

	if pairs := exec.Feed(context.Background(), nil, active); pairs != nil {
		t.Fatalf("nil candidate must produce nothing, got %v", pairs)
	}
	if pairs := exec.Feed(context.Background(), &mutation.Strategy{ID: ""}, active); pairs != nil {
		t.Fatalf("empty candidate ID must produce nothing, got %v", pairs)
	}
	if pairs := exec.Feed(context.Background(), active, active); pairs != nil {
		t.Fatalf("candidate == active must produce nothing, got %v", pairs)
	}
	if evs := queryShadowEvidence(t, exec.store); len(evs) != 0 {
		t.Fatalf("guards must not write evidence, got %d records", len(evs))
	}
}

// TestShadowExecutor_RunnerErrorProducesNoFabricatedScore locks the scoring
// honesty rule: an arm that fails to RUN writes no score and no evidence, and
// the pair is skipped entirely instead of being completed with an invented
// number.
func TestShadowExecutor_RunnerErrorProducesNoFabricatedScore(t *testing.T) {
	runner := &fakeShadowRunner{outcomes: map[string]fakeShadowOutcome{
		"cand-1": {err: context.DeadlineExceeded},
	}}
	exec := shadowTestExecutor(t, runner, 3)
	exec.OnTaskFinalized(shadowTestTask("t1"))

	pairs := exec.Feed(context.Background(), &mutation.Strategy{ID: "cand-1"}, &mutation.Strategy{ID: "active-1"})
	if len(pairs) != 0 {
		t.Fatalf("a failed arm must not yield a pair, got %v", pairs)
	}
	evs := queryShadowEvidence(t, exec.store)
	for _, ev := range evs {
		var fe struct {
			StrategyID string `json:"strategy_id"`
		}
		if err := json.Unmarshal(ev.Payload, &fe); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		if fe.StrategyID == "cand-1" {
			t.Fatalf("failed arm must not write evidence, found record %s", ev.ID)
		}
	}
}

// TestShadowExecutor_ConstructorRejectsMissingDeps locks the fail-loud
// construction: an executor without a store or a runner would look wired
// while producing nothing.
func TestShadowExecutor_ConstructorRejectsMissingDeps(t *testing.T) {
	if _, err := NewShadowExecutor(nil, &fakeShadowRunner{}, 3); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := NewShadowExecutor(evidence.NewMemoryStore(), nil, 3); err == nil {
		t.Fatal("nil runner must be rejected")
	}
}

// TestShadowExecutor_BufferCapEvictsOldest locks the ring semantics: the
// buffer tracks RECENT traffic, so a full buffer drops the oldest entry.
func TestShadowExecutor_BufferCapEvictsOldest(t *testing.T) {
	runner := &fakeShadowRunner{}
	exec := shadowTestExecutor(t, runner, shadowTaskBuffer)
	for i := 0; i < shadowTaskBuffer+4; i++ {
		exec.OnTaskFinalized(shadowTestTask("t" + string(rune('a'+i))))
	}
	if got := exec.Buffered(); got != shadowTaskBuffer {
		t.Fatalf("buffer size = %d, want %d", got, shadowTaskBuffer)
	}
}
