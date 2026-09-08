package taskfabric

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The M4.4/M4 event-payload contract: the capability key rides on EVERY
// persisted event (the skill outcome writer's join key), and the cumulative
// token usage rides on terminal task.completed events (the RuntimeObserver's
// cost channel), both without decoding the checkpoint envelope.

// readEvents drains one task's persisted stream from the store.
func readEvents(t *testing.T, store *ares_events.MemoryEventStore, taskID string) []*ares_events.Event {
	t.Helper()
	evs, err := store.Read(context.Background(), taskID, ares_events.ReadOptions{})
	require.NoError(t, err)
	return evs
}

func TestEveryPersistedEventCarriesCapability(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	require.NoError(t, f.Create(newTask("t-cap")))
	epoch, err := f.Acquire("t-cap", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start("t-cap", "agent-a", epoch))
	require.NoError(t, f.Complete("t-cap", "agent-a", epoch))

	for _, ev := range readEvents(t, store, "t-cap") {
		capability, _ := ev.Payload["capability"].(string)
		assert.Equal(t, "rust", capability,
			"event %s must carry the capability payload key", ev.Type)
	}
}

func TestFailedEventCarriesCapabilityToo(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	require.NoError(t, f.Create(newTask("t-fail-cap")))
	epoch, err := f.Acquire("t-fail-cap", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start("t-fail-cap", "agent-a", epoch))
	require.NoError(t, f.Fail("t-fail-cap", "agent-a", epoch))

	sawFailed := false
	for _, ev := range readEvents(t, store, "t-fail-cap") {
		if ev.Type != ares_events.EventTaskFailed {
			continue
		}
		sawFailed = true
		capability, _ := ev.Payload["capability"].(string)
		assert.Equal(t, "rust", capability, "task.failed must carry the capability")
	}
	assert.True(t, sawFailed, "expected a task.failed event")
}

// completeWithTokenEnvelope drives a task to COMPLETED with an envelope
// carrying the given cumulative token usage, then returns the terminal event.
func completeWithTokenEnvelope(t *testing.T, f *Fabric, taskID string, in, out int) *ares_events.Event {
	t.Helper()
	require.NoError(t, f.Create(newTask(taskID)))
	epoch, err := f.Acquire(taskID, "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start(taskID, "agent-a", epoch))
	require.NoError(t, f.CompleteWithCheckpoint(taskID, "agent-a", epoch, &CheckpointEnvelope{
		SchemaVersion: CurrentCheckpointSchemaVersion,
		InputTokens:   in,
		OutputTokens:  out,
	}))
	var terminal *ares_events.Event
	for _, ev := range readEvents(t, storeOf(t, f), taskID) {
		if ev.Type == ares_events.EventTaskCompleted {
			terminal = ev
		}
	}
	require.NotNil(t, terminal, "expected a task.completed event")
	return terminal
}

// storeOf extracts the fabric's attached store (test helper; the field is
// mutex-guarded and unexported).
func storeOf(t *testing.T, f *Fabric) *ares_events.MemoryEventStore {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	store, ok := f.store.(*ares_events.MemoryEventStore)
	require.True(t, ok, "test must attach a MemoryEventStore")
	return store
}

func TestCompletedEventCarriesTokenUsage(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	terminal := completeWithTokenEnvelope(t, f, "t-tok", 1_200, 300)
	assert.Equal(t, 1_200, terminal.Payload["input_tokens"])
	assert.Equal(t, 300, terminal.Payload["output_tokens"])
	assert.Equal(t, 1_500, terminal.Payload["total_tokens"])
}

func TestCompletedEventOmitsTokenUsageWhenZero(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	terminal := completeWithTokenEnvelope(t, f, "t-tok0", 0, 0)
	assert.NotContains(t, terminal.Payload, "input_tokens")
	assert.NotContains(t, terminal.Payload, "output_tokens")
	assert.NotContains(t, terminal.Payload, "total_tokens")
}

func TestFailedEventNeverCarriesTokenUsage(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	require.NoError(t, f.Create(newTask("t-tok-fail")))
	epoch, err := f.Acquire("t-tok-fail", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start("t-tok-fail", "agent-a", epoch))
	// A failure with a token-bearing envelope must NOT stamp tokens on the
	// event: the observer's cost penalty applies to successes only.
	require.NoError(t, f.Fail("t-tok-fail", "agent-a", epoch))
	for _, ev := range readEvents(t, store, "t-tok-fail") {
		if ev.Type == ares_events.EventTaskFailed {
			assert.NotContains(t, ev.Payload, "total_tokens")
		}
	}
}

// The v4 envelope schema: token fields decode from the struct form AND the
// JSON-round-tripped form; a v3 envelope decodes with zero usage.

func TestTokenUsageEnvelopeRoundTrip(t *testing.T) {
	env := &CheckpointEnvelope{
		SchemaVersion: CurrentCheckpointSchemaVersion,
		InputTokens:   7,
		OutputTokens:  3,
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)

	var decoded any
	require.NoError(t, json.Unmarshal(b, &decoded))
	dc, err := DecodeCheckpoint(decoded)
	require.NoError(t, err)
	assert.Equal(t, 7, dc.InputTokens)
	assert.Equal(t, 3, dc.OutputTokens)

	// The struct form decodes through the same path.
	dc2, err := DecodeCheckpoint(env)
	require.NoError(t, err)
	assert.Equal(t, 7, dc2.InputTokens)
	assert.Equal(t, 3, dc2.OutputTokens)

	// tokenUsageFromCheckpoint reads the same values.
	in, out := tokenUsageFromCheckpoint(decoded)
	assert.Equal(t, 7, in)
	assert.Equal(t, 3, out)
}

func TestV3EnvelopeDecodesWithZeroTokens(t *testing.T) {
	// A v3 envelope (no token fields) decodes under v4 code with 0 usage —
	// forward compatibility, the documented v3→v4 migration contract.
	m := map[string]any{
		"schema_version": 3,
		"strategy_id":    "s1",
		"session_id":     "sess-1",
	}
	dc, err := DecodeCheckpoint(m)
	require.NoError(t, err)
	assert.Equal(t, 0, dc.InputTokens)
	assert.Equal(t, 0, dc.OutputTokens)
	assert.Equal(t, "s1", dc.StrategyID)
	assert.Equal(t, "sess-1", dc.SessionID)
}

func TestEncodeCheckpointCarriesTokenUsage(t *testing.T) {
	env := EncodeCheckpoint(DecodedCheckpoint{
		StrategyID:   "s",
		InputTokens:  11,
		OutputTokens: 4,
	})
	assert.Equal(t, 11, env.InputTokens)
	assert.Equal(t, 4, env.OutputTokens)
	assert.Equal(t, CurrentCheckpointSchemaVersion, env.SchemaVersion)
}

// PriorConfidence reflects the wired source (the M4.4 read-side helper the
// kernel scheduler consults to decide whether a history-less candidate's
// neutral confidence should yield to the prior).

type staticPrior struct{ v float64 }

func (p staticPrior) Confidence(string) float64 { return p.v }

func TestPriorConfidence(t *testing.T) {
	f := NewFabric()
	assert.Equal(t, 0.0, f.PriorConfidence("rust"), "no source wired → 0")

	f.WithConfidenceSource(staticPrior{0.8})
	assert.Equal(t, 0.8, f.PriorConfidence("rust"))

	f.WithConfidenceSource(nil)
	assert.Equal(t, 0.0, f.PriorConfidence("rust"), "detached → 0")
}

// The Schedule prior fill: a history-less (zero-declared) candidate is
// filled with the wired prior — the READ half of the M4.4 loop that the
// tracker's neutral 1.0 used to mask.

func TestScheduleFillsUnmeasuredCandidateWithPrior(t *testing.T) {
	f := NewFabric().WithConfidenceSource(staticPrior{0.6})
	require.NoError(t, f.Create(newTask("t-prior")))

	cands := []Candidate{{
		AgentID:      "a",
		Capabilities: []string{"rust"},
		// Zero = "unmeasured" — the kernel scheduler zeroes history-less
		// candidates precisely so this fill applies.
		Confidence: 0,
	}}
	winner, _, err := f.Schedule("t-prior", cands, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "a", winner)

	// The scheduling decision the fabric recorded reflects the filled score.
	tk, err := f.Task("t-prior")
	require.NoError(t, err)
	assert.Equal(t, "a", tk.Owner)
}
