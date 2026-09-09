// Package evolution is the DEPRECATED public alias of internal/evoapi (M5).
// New code MUST import internal/evoapi; this package exists only for
// external consumers and is scheduled for removal.
//
// This package exposes the legacy evolution building blocks (Strategy,
// Lineage, DreamCycle, GA Population, Mutator, Promoter); the canonical
// definitions live in internal/evoapi (with its genome and mutation
// subpackages at internal/evoapi/genome and internal/evoapi/mutation).
package evolution

import (
	"github.com/Timwood0x10/ares/internal/evoapi"
)

// Strategy is an evolvable strategy (public evolution domain).
type Strategy = evoapi.Strategy

// Lineage records the parent→child relationship of one mutation step.
type Lineage = evoapi.Lineage

// DreamCycleConfig configures the dream cycle orchestrator.
type DreamCycleConfig = evoapi.DreamCycleConfig

// CallbackData holds data passed to the dream cycle during evolution triggers.
type CallbackData = evoapi.CallbackData

// DreamCycle is the dream cycle orchestrator interface.
type DreamCycle = evoapi.DreamCycle

// PopulationConfig configures the GA population.
type PopulationConfig = evoapi.PopulationConfig

// ScorerFunc scores a strategy to drive population evolution.
type ScorerFunc = evoapi.ScorerFunc

// Population is the GA population interface.
type Population = evoapi.Population

// Agent is one member of a GA population.
type Agent = evoapi.Agent

// MutationConfig configures the public Mutator.
type MutationConfig = evoapi.MutationConfig

// Mutator mutates strategies.
type Mutator = evoapi.Mutator

// PromotionCriteria decides when a strategy is promoted or demoted.
type PromotionCriteria = evoapi.PromotionCriteria

// Promoter evaluates, promotes, and demotes strategies.
type Promoter = evoapi.Promoter

// DefaultDreamCycleConfig returns the default dream cycle configuration.
func DefaultDreamCycleConfig() DreamCycleConfig { return evoapi.DefaultDreamCycleConfig() }

// NewDreamCycle creates a dream cycle from wired internal components.
func NewDreamCycle(scheduler, mutator any, opts ...any) (DreamCycle, error) {
	return evoapi.NewDreamCycle(scheduler, mutator, opts...)
}

// DefaultPopulationConfig returns the default GA population configuration.
func DefaultPopulationConfig() PopulationConfig { return evoapi.DefaultPopulationConfig() }

// NewPopulation creates a GA population seeded from a base strategy.
func NewPopulation(base *Strategy, cfg PopulationConfig) (Population, error) {
	return evoapi.NewPopulation(base, cfg)
}

// NewMutator constructs a Mutator by wrapping the internal mutation engine.
func NewMutator(model string, cfg MutationConfig) (Mutator, error) {
	return evoapi.NewMutator(model, cfg)
}

// DefaultPromotionCriteria returns the default promotion criteria.
func DefaultPromotionCriteria() PromotionCriteria { return evoapi.DefaultPromotionCriteria() }

// NewPromoter creates a Promoter with the given criteria.
func NewPromoter(criteria *PromotionCriteria) Promoter { return evoapi.NewPromoter(criteria) }
