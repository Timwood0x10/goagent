package evolution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func newTestBaseStrategy() *Strategy {
	return &Strategy{
		ID:      "base-strategy",
		Name:    "base",
		Version: 1,
		Params:  map[string]any{"temperature": 0.7, "top_k": 40},
		Score:   50.0,
	}
}

func newMinimalConfig() *SystemConfig {
	return &SystemConfig{
		BaseStrategy:      newTestBaseStrategy(),
		PopulationSize:    10,
		EliteCount:        2,
		SurvivalRate:      0.6,
		MutationRate:      0.2,
		MinMutationRate:   0.05,
		MaxMutationRate:   0.5,
		BreedingPoolRatio: 0.6,
		Generations:       5,
		HistoryMaxSize:    10,
		SelectionStrategy: "tournament",
	}
}

func TestNewService_Validation(t *testing.T) {
	t.Parallel()

	base := newTestBaseStrategy()

	tests := []struct {
		name    string
		cfg     *SystemConfig
		wantErr error
	}{
		{
			name:    "nil_config",
			cfg:     nil,
			wantErr: ErrNilConfig,
		},
		{
			name:    "nil_base_strategy",
			cfg:     &SystemConfig{BaseStrategy: nil},
			wantErr: ErrNilBaseStrategy,
		},
		{
			name:    "survival_rate_too_low",
			cfg:     &SystemConfig{BaseStrategy: base, SurvivalRate: -0.1},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "survival_rate_too_high",
			cfg:     &SystemConfig{BaseStrategy: base, SurvivalRate: 1.5},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "mutation_rate_too_low",
			cfg:     &SystemConfig{BaseStrategy: base, MutationRate: -0.1},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "mutation_rate_too_high",
			cfg:     &SystemConfig{BaseStrategy: base, MutationRate: 1.5},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "min_mutation_rate_too_low",
			cfg:     &SystemConfig{BaseStrategy: base, MinMutationRate: -0.1},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "max_mutation_rate_too_high",
			cfg:     &SystemConfig{BaseStrategy: base, MaxMutationRate: 1.5},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "min_exceeds_max",
			cfg:     &SystemConfig{BaseStrategy: base, MinMutationRate: 0.7, MaxMutationRate: 0.3},
			wantErr: errors.New("min_mutation_rate"),
		},
		{
			name:    "breeding_pool_ratio_too_low",
			cfg:     &SystemConfig{BaseStrategy: base, BreedingPoolRatio: -0.1},
			wantErr: ErrInvalidRate,
		},
		{
			name:    "breeding_pool_ratio_too_high",
			cfg:     &SystemConfig{BaseStrategy: base, BreedingPoolRatio: 1.5},
			wantErr: ErrInvalidRate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, err := NewService(tt.cfg)
			assert.Error(t, err)
			assert.Nil(t, svc)
			assert.ErrorContains(t, err, tt.wantErr.Error())
		})
	}
}

func TestNewService_NonWiredSuccess(t *testing.T) {
	cfg := newMinimalConfig()
	cfg.EnableWiredMode = false

	svc, err := NewService(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
	assert.NotNil(t, svc.population)
	assert.NotNil(t, svc.mutator)
	assert.NotNil(t, svc.crosser)
	assert.Nil(t, svc.wiredSystem)
	assert.Equal(t, cfg, svc.config)
}

func TestNewService_WiredSuccess(t *testing.T) {
	cfg := newMinimalConfig()
	cfg.EnableWiredMode = true

	svc, err := NewService(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)
	assert.NotNil(t, svc.wiredSystem)
	assert.Nil(t, svc.population)
	assert.Equal(t, cfg, svc.config)
}

func TestService_BestStrategy_NotInitialized(t *testing.T) {
	svc := &Service{}
	best, err := svc.BestStrategy()
	assert.ErrorIs(t, err, ErrNotInitialized)
	assert.Nil(t, best)
}

func TestService_Stats_NotInitialized(t *testing.T) {
	svc := &Service{}
	stats, err := svc.Stats()
	assert.ErrorIs(t, err, ErrNotInitialized)
	assert.Nil(t, stats)
}

func TestService_Lineages_NotInitialized(t *testing.T) {
	svc := &Service{}
	lineages, err := svc.Lineages()
	assert.NoError(t, err) // Returns empty slice, not error.
	assert.Empty(t, lineages)
}

func TestService_ReportPath(t *testing.T) {
	svc := &Service{config: &SystemConfig{ReportPath: "var/report.txt"}}
	assert.Equal(t, "var/report.txt", svc.ReportPath())

	svc2 := &Service{config: &SystemConfig{}}
	assert.Equal(t, "", svc2.ReportPath())
}

func TestService_Evolve_NotInitialized(t *testing.T) {
	svc := &Service{}
	_, err := svc.Evolve(context.Background(), 1)
	assert.ErrorIs(t, err, ErrNotInitialized)
}

func TestService_Evolve_ContextCancelled(t *testing.T) {
	cfg := newMinimalConfig()
	cfg.EnableWiredMode = false

	svc, err := NewService(cfg)
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already cancelled.

	result, err := svc.Evolve(ctx, 1)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.TotalGens) // Cancelled before any generation.
}

func TestService_Evolve_ZeroGenerationsUsesDefault(t *testing.T) {
	cfg := newMinimalConfig()
	cfg.EnableWiredMode = false
	cfg.Generations = 3

	svc, err := NewService(cfg)
	assert.NoError(t, err)

	// With zero generations passed, it should default to config.Generations (3).
	result, err := svc.Evolve(context.Background(), 0)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 3, result.TotalGens)
	assert.NotNil(t, result.BestStrategy)
}

func TestService_Shutdown(t *testing.T) {
	cfg := newMinimalConfig()
	svc, err := NewService(cfg)
	assert.NoError(t, err)
	assert.NotNil(t, svc)

	// Shutdown should not panic.
	svc.Shutdown()
}

func TestService_SaveBestStrategy_NotInitialized(t *testing.T) {
	svc := &Service{}
	err := svc.SaveBestStrategy("/tmp/test_strategy.json")
	assert.ErrorIs(t, err, ErrNotInitialized)
}

func TestLoadBestStrategy_EmptyPath(t *testing.T) {
	_, err := LoadBestStrategy("")
	assert.Error(t, err)
}

// ── Utility Functions ─────────────────────────

func TestBestFromStrategies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		agents  []*mutation.Strategy
		wantID  string
		wantNil bool
	}{
		{
			name:    "empty_slice",
			agents:  []*mutation.Strategy{},
			wantNil: true,
		},
		{
			name: "single_agent",
			agents: []*mutation.Strategy{
				{ID: "only", Score: 50},
			},
			wantID: "only",
		},
		{
			name: "picks_highest_score",
			agents: []*mutation.Strategy{
				{ID: "low", Score: 30},
				{ID: "high", Score: 90},
				{ID: "mid", Score: 60},
			},
			wantID: "high",
		},
		{
			name: "picks_first_when_tied",
			agents: []*mutation.Strategy{
				{ID: "first", Score: 50},
				{ID: "second", Score: 50},
			},
			wantID: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bestFromStrategies(tt.agents)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			assert.NotNil(t, got)
			assert.Equal(t, tt.wantID, got.ID)
		})
	}
}

func TestToAPIStrategy(t *testing.T) {
	t.Parallel()

	now := time.Now()
	internal := &mutation.Strategy{
		ID:                   "s1",
		Name:                 "test-strategy",
		Version:              3,
		Params:               map[string]any{"temp": 0.5},
		ParentID:             "parent-1",
		PromptTemplate:       "helpful",
		StrategyMutationType: mutation.MutationCrossover,
		Score:                85.0,
		DimensionScores:      map[string]float64{"accuracy": 90, "speed": 80},
		CreatedAt:            now,
	}

	api := toAPIStrategy(internal)
	assert.NotNil(t, api)
	assert.Equal(t, internal.ID, api.ID)
	assert.Equal(t, internal.Name, api.Name)
	assert.Equal(t, internal.Version, api.Version)
	assert.Equal(t, internal.ParentID, api.ParentID)
	assert.Equal(t, internal.PromptTemplate, api.PromptTemplate)
	assert.Equal(t, "crossover", api.MutationType)
	assert.Equal(t, internal.Score, api.Score)
	assert.Equal(t, internal.DimensionScores, api.DimensionScores)
	assert.Equal(t, internal.CreatedAt, api.CreatedAt)

	// Params should be cloned, not same reference.
	assert.Equal(t, internal.Params, api.Params)
	internal.Params["temp"] = 99.9
	assert.NotEqual(t, internal.Params["temp"], api.Params["temp"])
}

func TestToAPIStrategy_Nil(t *testing.T) {
	assert.Nil(t, toAPIStrategy(nil))
}

func TestToInternalStrategy(t *testing.T) {
	t.Parallel()

	now := time.Now()
	api := &Strategy{
		ID:              "s1",
		Name:            "test-strategy",
		Version:         3,
		Params:          map[string]any{"temp": 0.5},
		ParentID:        "parent-1",
		PromptTemplate:  "helpful",
		MutationType:    "crossover",
		Score:           85.0,
		DimensionScores: map[string]float64{"accuracy": 90, "speed": 80},
		CreatedAt:       now,
	}

	internal := toInternalStrategy(api)
	assert.NotNil(t, internal)
	assert.Equal(t, api.ID, internal.ID)
	assert.Equal(t, api.Name, internal.Name)
	assert.Equal(t, api.Version, internal.Version)
	assert.Equal(t, api.ParentID, internal.ParentID)
	assert.Equal(t, api.PromptTemplate, internal.PromptTemplate)
	assert.Equal(t, api.Score, internal.Score)
	assert.Equal(t, api.DimensionScores, internal.DimensionScores)
	assert.Equal(t, api.CreatedAt, internal.CreatedAt)

	// Params should be cloned.
	assert.Equal(t, api.Params, internal.Params)
	api.Params["temp"] = 99.9
	assert.NotEqual(t, api.Params["temp"], internal.Params["temp"])
}

func TestToInternalStrategy_Nil(t *testing.T) {
	assert.Nil(t, toInternalStrategy(nil))
}

func TestCloneDimensionScores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input map[string]float64
	}{
		{name: "nil_input", input: nil},
		{name: "empty_map", input: map[string]float64{}},
		{name: "populated_map", input: map[string]float64{"accuracy": 90, "speed": 80}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cloneDimensionScores(tt.input)
			assert.Equal(t, tt.input, got)

			if len(tt.input) > 0 {
				// Ensure it's a deep copy.
				got["accuracy"] = 0
				assert.NotEqual(t, tt.input["accuracy"], got["accuracy"])
			}
		})
	}
}
