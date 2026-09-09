// Package evolution provides the public API for strategy evolution, including
// the DreamCycle orchestrator, GA Population, mutation, and promotion subsystems.
package evoapi

import (
	"context"
	"fmt"
	"time"

	pubmutation "github.com/Timwood0x10/ares/internal/evoapi/mutation"
	evolve "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	internalmutation "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/promotion"
)

const paramKeyTemperature = "temperature"

// ---------------------------------------------------------------------------
// Strategy & Lineage
// ---------------------------------------------------------------------------

type Strategy struct {
	ID             string
	Version        int
	Score          float64
	ParentID       string
	PromptTemplate string
	Params         map[string]any
	MutationType   string
}

type Lineage struct {
	ParentID         string
	ChildID          string
	MutationType     string
	WinRate          float64
	ScoreImprovement float64
}

// ---------------------------------------------------------------------------
// DreamCycle
// ---------------------------------------------------------------------------

type DreamCycleConfig struct {
	Enabled              bool
	MinTasksBeforeEvolve int
	MinScoreDrop         float64
	MaxMutations         int
	MinWinRate           float64
	Cooldown             time.Duration
	TaskSampleSize       int
	QuickRejectRuns      int
}

// CallbackData holds data passed to the dream cycle during evolution triggers.
type CallbackData struct {
	AgentID string
}

func DefaultDreamCycleConfig() DreamCycleConfig {
	return DreamCycleConfig{
		Enabled:              false,
		MinTasksBeforeEvolve: 10,
		MinScoreDrop:         0.15,
		MaxMutations:         3,
		MinWinRate:           0.55,
		Cooldown:             5 * time.Minute,
		TaskSampleSize:       50,
		QuickRejectRuns:      5,
	}
}

type DreamCycle interface {
	Run(ctx context.Context, data CallbackData) error
	SetEnabled(enabled bool)
	IsEnabled() bool
	TaskCount() int64
}

type dreamCycleAdapter struct {
	inner *evolve.DreamCycle
}

func (d *dreamCycleAdapter) Run(ctx context.Context, data CallbackData) error {
	return d.inner.Run(ctx, evolve.CallbackData{AgentID: data.AgentID})
}
func (d *dreamCycleAdapter) SetEnabled(enabled bool) {
	d.inner.SetEnabled(enabled)
}
func (d *dreamCycleAdapter) IsEnabled() bool {
	return d.inner.IsEnabled()
}
func (d *dreamCycleAdapter) TaskCount() int64 {
	return d.inner.TaskCount()
}

func NewDreamCycle(scheduler, mutator any, opts ...any) (DreamCycle, error) {
	// Caller provides wired internal components.
	sched, ok := scheduler.(*evolve.EvolutionScheduler)
	if !ok {
		return nil, fmt.Errorf("NewDreamCycle: scheduler must be *evolve.EvolutionScheduler, got %T", scheduler)
	}
	mut, ok := mutator.(evolve.MutatorInterface)
	if !ok {
		return nil, fmt.Errorf("NewDreamCycle: mutator must be evolve.MutatorInterface, got %T", mutator)
	}
	// Forward option functions (e.g. WithDreamCycleConfig, WithDreamCycleTester)
	// so configuration/tester are no longer silently discarded. opts is untyped
	// because internal option types cannot be exposed through the public API.
	innerOpts := make([]evolve.DreamCycleOption, 0, len(opts))
	for i, opt := range opts {
		o, ok := opt.(evolve.DreamCycleOption)
		if !ok {
			return nil, fmt.Errorf("NewDreamCycle: opts[%d] must be evolve.DreamCycleOption, got %T", i, opt)
		}
		innerOpts = append(innerOpts, o)
	}
	inner, err := evolve.NewDreamCycle(sched, mut, nil, nil, innerOpts...)
	if err != nil {
		return nil, err
	}
	return &dreamCycleAdapter{inner: inner}, nil
}

// ---------------------------------------------------------------------------
// Genome (GA Population)
// ---------------------------------------------------------------------------

type PopulationConfig struct {
	Size              int
	EliteCount        int
	MutationRate      float64
	SurvivalRate      float64
	SelectionStrategy string
	TournamentSize    int
	CrossoverType     string
}

func DefaultPopulationConfig() PopulationConfig {
	return PopulationConfig{
		Size:              20,
		EliteCount:        3,
		MutationRate:      0.2,
		SurvivalRate:      0.6,
		SelectionStrategy: "tournament",
		TournamentSize:    3,
		CrossoverType:     "uniform",
	}
}

// ScorerFunc scores a strategy to drive population evolution.
// External callers implement this to plug their own evaluator (LLM judge,
// benchmark harness, success rate counter) into the public Population.
type ScorerFunc func(agent *Strategy) float64

type Population interface {
	Agents() []Agent
	Size() int
	CurrentGeneration() int
	BestScore() float64
	BestStrategy() *Strategy
	// ScoreAgents scores every agent in the population using the provided scorer.
	// Must be called before Evolve — agents with unevaluated score (-1) are
	// rejected by Evolve's pre-validation.
	ScoreAgents(scorer ScorerFunc)
	Evolve(ctx context.Context) error
}

type Agent struct {
	ID     string
	Score  float64
	Params map[string]any
}

type populationAdapter struct {
	inner *genome.Population
	cfg   PopulationConfig
}

func (p *populationAdapter) Agents() []Agent {
	agents, _ := p.inner.Snapshot()
	out := make([]Agent, len(agents))
	for i, a := range agents {
		out[i] = Agent{ID: a.ID, Score: a.Score, Params: a.Params}
	}
	return out
}
func (p *populationAdapter) Size() int              { return p.inner.Size }
func (p *populationAdapter) CurrentGeneration() int { return p.inner.CurrentGeneration() }
func (p *populationAdapter) BestScore() float64     { return p.inner.BestEverScore() }
func (p *populationAdapter) BestStrategy() *Strategy {
	best := p.inner.BestStrategy()
	if best == nil {
		return nil
	}
	return &Strategy{
		ID:             best.ID,
		Score:          best.Score,
		Params:         best.Params,
		PromptTemplate: best.PromptTemplate,
	}
}

// ScoreAgents scores every agent via the public ScorerFunc, bridging to
// the internal genome.Population.ScoreAgents which accepts an internal
// mutation.Strategy scorer. We snapshot internal agents, call the public
// scorer on each (converted to public Strategy), and write scores back.
func (p *populationAdapter) ScoreAgents(scorer ScorerFunc) {
	if scorer == nil {
		return
	}
	inner := func(s *internalmutation.Strategy) float64 {
		pub := &Strategy{
			ID:             s.ID,
			Version:        s.Version,
			Score:          s.Score,
			ParentID:       s.ParentID,
			PromptTemplate: s.PromptTemplate,
			Params:         s.Params,
		}
		return scorer(pub)
	}
	p.inner.ScoreAgents(inner)
}

func (p *populationAdapter) Evolve(ctx context.Context) error {
	// Create a default mutator with basic parameter ranges.
	mut, err := internalmutation.NewMutator(
		internalmutation.WithParamRanges(defaultParamRanges()),
	)
	if err != nil {
		return fmt.Errorf("create mutator: %w", err)
	}
	// Create a crossover using the configured crossover type.
	crossType := parseCrossoverType(p.cfg.CrossoverType)
	crosser, err := genome.NewCrossover(
		genome.WithSeed(42),
		genome.WithCrossoverType(crossType),
	)
	if err != nil {
		return fmt.Errorf("create crossover: %w", err)
	}
	return p.inner.Evolve(ctx, mut, crosser)
}

// parseCrossoverType converts a string to the corresponding genome.CrossoverType.
func parseCrossoverType(s string) genome.CrossoverType {
	switch s {
	case "two_point":
		return genome.CrossoverTwoPoint
	case "segment":
		return genome.CrossoverSegment
	default:
		return genome.CrossoverUniform
	}
}

// defaultParamRanges returns basic parameter ranges for public API users.
func defaultParamRanges() map[string]internalmutation.ParamRange {
	return map[string]internalmutation.ParamRange{
		paramKeyTemperature: {Values: []any{0.1, 0.3, 0.5, 0.7, 0.9}},
		"top_k":             {Values: []any{10, 20, 40, 60, 80, 100}},
		"max_tokens":        {Values: []any{1024, 2048, 4096, 8192}},
	}
}

func NewPopulation(base *Strategy, cfg PopulationConfig) (Population, error) {
	// Convert public Strategy to internal mutation.Strategy
	s := &internalmutation.Strategy{
		ID:     base.ID,
		Score:  base.Score,
		Params: base.Params,
	}

	// Create a default mutator so the population has a mutation operator
	// from the start (not nil).
	mut, err := internalmutation.NewMutator(
		internalmutation.WithParamRanges(defaultParamRanges()),
	)
	if err != nil {
		return nil, fmt.Errorf("create mutator: %w", err)
	}

	// Build options from config.
	opts := []genome.PopulationOption{
		genome.WithPopulationSize(cfg.Size),
		genome.WithEliteCount(cfg.EliteCount),
		genome.WithMutationRate(cfg.MutationRate),
		genome.WithSurvivalRate(cfg.SurvivalRate),
		genome.WithSelectionStrategy(cfg.SelectionStrategy),
		genome.WithTournamentSelection(cfg.TournamentSize),
	}

	inner, err := genome.NewPopulation(context.Background(), s, mut, opts...)
	if err != nil {
		return nil, err
	}
	return &populationAdapter{inner: inner, cfg: cfg}, nil
}

// ---------------------------------------------------------------------------
// Mutation
// ---------------------------------------------------------------------------

type MutationConfig struct {
	ParamMutationProb  float64
	PromptMutationProb float64
}

type Mutator interface {
	Mutate(ctx context.Context, parent *Strategy) (*Strategy, error)
}

// NewMutator constructs a public Mutator by wrapping the internal mutation engine.
// The model parameter is reserved for future LLM-guided mutation and may be empty.
// If cfg is zero-valued, sensible defaults are used.
func NewMutator(model string, cfg MutationConfig) (Mutator, error) {
	paramRanges := map[string][]any{
		"temperature":        {0.1, 0.3, 0.5, 0.7, 0.9},
		"top_k":              {10, 20, 40, 80},
		"max_steps":          {5, 10, 15, 20},
		"memory_limit":       {3, 5, 10},
		"conflict_threshold": {0.85, 0.90, 0.95},
	}

	mutCfg := pubmutation.MutatorConfig{
		ParamRanges:        paramRanges,
		ParamMutationProb:  cfg.ParamMutationProb,
		PromptMutationProb: cfg.PromptMutationProb,
	}

	if mutCfg.ParamMutationProb <= 0 {
		mutCfg.ParamMutationProb = 0.3
	}
	if mutCfg.PromptMutationProb <= 0 {
		mutCfg.PromptMutationProb = 0.3
	}

	inner, err := pubmutation.NewMutator(mutCfg)
	if err != nil {
		return nil, fmt.Errorf("new mutator: %w", err)
	}

	return &mutatorAdapter{inner: inner}, nil
}

// mutatorAdapter wraps the public mutation.Mutator to implement the local Mutator interface.
type mutatorAdapter struct {
	inner *pubmutation.Mutator
}

func (a *mutatorAdapter) Mutate(ctx context.Context, parent *Strategy) (*Strategy, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent strategy must not be nil")
	}

	pubStrat := &pubmutation.Strategy{
		ID:             parent.ID,
		Version:        parent.Version,
		Score:          parent.Score,
		ParentID:       parent.ParentID,
		PromptTemplate: parent.PromptTemplate,
		Params:         parent.Params,
	}

	child, err := a.inner.Mutate(ctx, pubStrat)
	if err != nil {
		return nil, fmt.Errorf("mutate: %w", err)
	}

	return &Strategy{
		ID:             child.ID,
		Version:        child.Version,
		Score:          child.Score,
		ParentID:       child.ParentID,
		PromptTemplate: child.PromptTemplate,
		Params:         child.Params,
		MutationType:   string(child.MutationType),
	}, nil
}

// ---------------------------------------------------------------------------
// Promotion
// ---------------------------------------------------------------------------

type PromotionCriteria struct {
	MinSampleCount     int
	MinSuccessRate     float64
	MinConfidence      float64
	ChampionHoldPeriod int
	DemotionThreshold  float64
	MaxChampionTenure  int
}

func DefaultPromotionCriteria() PromotionCriteria {
	return PromotionCriteria{
		MinSampleCount:     100,
		MinSuccessRate:     0.85,
		MinConfidence:      0.70,
		ChampionHoldPeriod: 5,
		DemotionThreshold:  0.30,
		MaxChampionTenure:  20,
	}
}

type Promoter interface {
	Evaluate(ctx context.Context, strategyID string, successRate, confidence float64) (string, error)
	Promote(ctx context.Context, strategyID string) error
	Demote(ctx context.Context, strategyID string) error
}

type promoterAdapter struct {
	inner *promotion.DefaultPromoter
}

func (p *promoterAdapter) Evaluate(ctx context.Context, strategyID string, successRate, confidence float64) (string, error) {
	ev := experience.Evidence{
		StrategyID:  strategyID,
		SuccessRate: successRate,
		Confidence:  confidence,
		ErrorRate:   1.0 - successRate,
		SampleCount: 1,
		LastUpdated: time.Now(),
	}

	state, reason, err := p.inner.Evaluate(ctx, strategyID, ev)
	if err != nil {
		return "", fmt.Errorf("promoter evaluate: %w", err)
	}
	return fmt.Sprintf("%s: %s", state, reason), nil
}
func (p *promoterAdapter) Promote(ctx context.Context, strategyID string) error {
	return p.inner.Promote(ctx, strategyID)
}
func (p *promoterAdapter) Demote(ctx context.Context, strategyID string) error {
	return p.inner.Demote(ctx, strategyID, "demoted by public API")
}

func NewPromoter(criteria *PromotionCriteria) Promoter {
	ic := promotion.DefaultPromotionCriteria()
	if criteria != nil {
		ic.MinSampleCount = criteria.MinSampleCount
		ic.MinSuccessRate = criteria.MinSuccessRate
		ic.MinConfidence = criteria.MinConfidence
		ic.ChampionHoldPeriod = criteria.ChampionHoldPeriod
		ic.DemotionThreshold = criteria.DemotionThreshold
		ic.MaxChampionTenure = criteria.MaxChampionTenure
	}
	return &promoterAdapter{inner: promotion.NewDefaultPromoter(ic)}
}
