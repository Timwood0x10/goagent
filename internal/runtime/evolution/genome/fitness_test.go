package genome

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// appendFitness appends a fitness evidence record under the given source and
// value to the in-memory store. It is the test-side mirror of what producers
// (flight collector, memory retriever, knowledge runtime) emit at runtime.
func appendFitness(t *testing.T, s *evidence.MemoryStore, source string, value float64) {
	t.Helper()
	err := s.Append(context.Background(), evidence.NewEvidence(
		source,
		evidence.KindFitness,
		map[string]any{"value": value},
	))
	require.NoError(t, err)
}

// ── avgFitnessValue ──────────────────────────

func TestAvgFitnessValue_NoEvidence(t *testing.T) {
	s := evidence.NewMemoryStore()
	score, err := avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.ErrorIs(t, err, errNoEvidence)
	assert.Zero(t, score)
}

func TestAvgFitnessValue_NilStore(t *testing.T) {
	score, err := avgFitnessValue(context.Background(), nil, "recovery", 0, 50)
	require.ErrorIs(t, err, errNoEvidence)
	assert.Zero(t, score)
}

func TestAvgFitnessValue_AveragesValues(t *testing.T) {
	s := evidence.NewMemoryStore()
	appendFitness(t, s, "recovery", 0.0)
	appendFitness(t, s, "recovery", 1.0)
	appendFitness(t, s, "recovery", 0.5)

	score, err := avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, score, 0.0001)
}

func TestAvgFitnessValue_IgnoresOtherSources(t *testing.T) {
	s := evidence.NewMemoryStore()
	appendFitness(t, s, "recovery", 1.0)
	appendFitness(t, s, "workflow", 0.0)

	// Only "recovery" records count, so the mean is 1.0, not 0.5.
	score, err := avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, score, 0.0001)
}

func TestAvgFitnessValue_SkipsOutOfRangeValues(t *testing.T) {
	s := evidence.NewMemoryStore()
	// 1.5 is outside [0,1]; it must be skipped, leaving only the valid 0.5.
	err := s.Append(context.Background(), evidence.NewEvidence(
		"recovery", evidence.KindFitness, map[string]any{"value": 1.5},
	))
	require.NoError(t, err)
	appendFitness(t, s, "recovery", 0.5)

	score, err := avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, score, 0.0001)
}

func TestAvgFitnessValue_SkipsNonNumericPayload(t *testing.T) {
	s := evidence.NewMemoryStore()
	err := s.Append(context.Background(), evidence.NewEvidence(
		"recovery", evidence.KindFitness, map[string]any{"value": "not-a-number"},
	))
	require.NoError(t, err)
	// No usable records → errNoEvidence (genome maps it to 0.5 fitness).
	_, err = avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.ErrorIs(t, err, errNoEvidence)
}

func TestAvgFitnessValue_EmptyPayloadSkipped(t *testing.T) {
	s := evidence.NewMemoryStore()
	// A nil payload produces an empty raw message (len(Payload) == 0), which
	// avgFitnessValue skips outright.
	err := s.Append(context.Background(), evidence.NewEvidence(
		"recovery", evidence.KindFitness, nil,
	))
	require.NoError(t, err)
	_, err = avgFitnessValue(context.Background(), s, "recovery", 0, 50)
	require.ErrorIs(t, err, errNoEvidence)
}

func TestAvgFitnessValue_WindowFiltersOldRecords(t *testing.T) {
	s := evidence.NewMemoryStore()

	// A record timestamped one minute ago is excluded by a 10s window.
	rec := evidence.Evidence{
		ID:        "old",
		Source:    "recovery",
		Kind:      evidence.KindFitness,
		Payload:   []byte(`{"value": 1.0}`),
		Timestamp: time.Now().Add(-1 * time.Minute),
	}
	err := s.Append(context.Background(), rec)
	require.NoError(t, err)

	// A 10-second window excludes the 1-minute-old record → no evidence.
	score, err := avgFitnessValue(context.Background(), s, "recovery", 10*time.Second, 50)
	require.ErrorIs(t, err, errNoEvidence)
	assert.Zero(t, score)
}

func TestAvgFitnessValue_RespectsLimit(t *testing.T) {
	s := evidence.NewMemoryStore()
	appendFitness(t, s, "recovery", 0.0)
	appendFitness(t, s, "recovery", 1.0)

	// Limit 1 keeps only the newest record (1.0).
	score, err := avgFitnessValue(context.Background(), s, "recovery", 0, 1)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, score, 0.0001)
}
