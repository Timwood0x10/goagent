// Package genome provides population management for genetic algorithm evolution.
package genome

import "errors"

// Population validation errors.
var (
	ErrNilBaseStrategy               = errors.New("base strategy must not be nil")
	ErrNilMutator                    = errors.New("mutator must not be nil")
	ErrNilCrosser                    = errors.New("crosser must not be nil")
	ErrInvalidPopulationSize         = errors.New("population size must be positive")
	ErrInvalidSurvivalRate           = errors.New("survival rate must be between 0 and 1")
	ErrInvalidMutationRate           = errors.New("mutation rate must be between 0 and 1")
	ErrInvalidEliteCount             = errors.New("elite count must be non-negative and <= population size")
	ErrInvalidBreedingPoolRatio      = errors.New("breeding pool ratio must be between 0 and 1")
	ErrInvalidMinMutationRate        = errors.New("min mutation rate must be between 0 and 1")
	ErrInvalidMaxMutationRate        = errors.New("max mutation rate must be between 0 and 1")
	ErrInvalidMaxStagnantGenerations = errors.New("max stagnant generations must be non-negative")
	ErrInvalidDiversityThreshold     = errors.New("diversity threshold must be between 0 and 1")
)

// Selection errors.
var (
	ErrSelectionEmptyPopulation = errors.New("selection: population must not be empty")
	ErrInvalidSelectionSize     = errors.New("selection size must be positive")
	ErrInvalidTournamentSize    = errors.New("tournament size must be at least 2")
	ErrNoSelectorNeeded         = errors.New("no selector needed for random selection")
)
