package ares_bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The events-table retention wiring decision: only a PG event store with an
// explicitly positive retention gets a cleaner — the default (0 = keep
// forever) and every non-PG store register nothing, because deleting events
// narrows the task fabric's cross-restart restore window.

func TestEventsRetentionCleanerFor(t *testing.T) {
	t.Run("zero_retention_never_registers", func(t *testing.T) {
		_, ok := eventsRetentionCleanerFor(&ares_events.PostgresEventStore{}, 0)
		assert.False(t, ok, "0 days = keep forever (the default)")
		_, ok = eventsRetentionCleanerFor(&ares_events.PostgresEventStore{}, -1)
		assert.False(t, ok, "negative retention is invalid, not inverted")
	})

	t.Run("non_pg_store_never_registers", func(t *testing.T) {
		_, ok := eventsRetentionCleanerFor(ares_events.NewMemoryEventStore(), 30)
		assert.False(t, ok, "memory mode owns its archive+trim lifecycle")
	})

	t.Run("nil_store_never_registers", func(t *testing.T) {
		_, ok := eventsRetentionCleanerFor(nil, 30)
		assert.False(t, ok)
	})

	t.Run("pg_store_with_retention_registers", func(t *testing.T) {
		cleaner, ok := eventsRetentionCleanerFor(&ares_events.PostgresEventStore{}, 30)
		require.True(t, ok)
		assert.Equal(t, "events", cleaner.Name)
		require.NotNil(t, cleaner.Cleaner)
	})
}

// The regression-gate bootstrap contract: the gate is opt-in and fails
// closed — enabled without a suite or an LLM client is a configuration
// error, not a silent skip. (The full gate behavior is covered in
// ares_evolution/regression_gate_test.go; this pins the config surface.)
func TestRegressionGateConfigSurface(t *testing.T) {
	// The YAML knobs exist and default to disabled/harmless zero values.
	var cfg ares_config.EvolutionGateConfig
	assert.False(t, cfg.RegressionEnabled, "regression gate must be opt-in")
	assert.Equal(t, 0, cfg.RegressionRuns, "0 = gate default (5)")
	assert.Equal(t, 0.0, cfg.RegressionMinWinRate, "0 = gate default (0.55)")
}
