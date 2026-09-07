// Package genome provides population management for genetic algorithm evolution.
package genome

import "fmt"

// PopulationOption is a functional option for configuring Population creation.
type PopulationOption func(*PopulationConfig) error

func WithPopulationSize(size int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if size <= 0 {
			return fmt.Errorf("%w: got %d", ErrInvalidPopulationSize, size)
		}
		cfg.Size = size
		return nil
	}
}

func WithSurvivalRate(rate float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if rate < 0 || rate > 1 {
			return fmt.Errorf("%w: got %v", ErrInvalidSurvivalRate, rate)
		}
		cfg.SurvivalRate = rate
		return nil
	}
}

func WithMutationRate(rate float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if rate < 0 || rate > 1 {
			return fmt.Errorf("%w: got %v", ErrInvalidMutationRate, rate)
		}
		cfg.MutationRate = rate
		return nil
	}
}

func WithEliteCount(count int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if count < 0 {
			return fmt.Errorf("%w: got %d", ErrInvalidEliteCount, count)
		}
		cfg.EliteCount = count
		return nil
	}
}

func WithPopulationSeed(seed int64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		cfg.Seed = seed
		return nil
	}
}

func WithBreedingPoolRatio(ratio float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if ratio < 0 || ratio > 1 {
			return fmt.Errorf("%w: breeding pool ratio must be between 0 and 1, got %v", ErrInvalidBreedingPoolRatio, ratio)
		}
		cfg.BreedingPoolRatio = ratio
		return nil
	}
}

func WithMinMutationRate(rate float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if rate < 0 || rate > 1 {
			return fmt.Errorf("%w: got %v", ErrInvalidMinMutationRate, rate)
		}
		cfg.MinMutationRate = rate
		return nil
	}
}

func WithMaxMutationRate(rate float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if rate < 0 || rate > 1 {
			return fmt.Errorf("%w: got %v", ErrInvalidMaxMutationRate, rate)
		}
		cfg.MaxMutationRate = rate
		return nil
	}
}

func WithMaxStagnantGenerations(n int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if n < 0 {
			return fmt.Errorf("%w: got %d", ErrInvalidMaxStagnantGenerations, n)
		}
		cfg.MaxStagnantGenerations = n
		return nil
	}
}

func WithDiversityThreshold(threshold float64) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if threshold < 0 || threshold > 1 {
			return fmt.Errorf("%w: got %v", ErrInvalidDiversityThreshold, threshold)
		}
		cfg.DiversityThreshold = threshold
		return nil
	}
}

func WithTournamentSelection(size int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if size < 2 {
			return fmt.Errorf("%w: got %d", ErrInvalidTournamentSize, size)
		}
		cfg.SelectionStrategy = "tournament"
		cfg.TournamentSize = size
		return nil
	}
}

var validSelectionStrategies = map[string]bool{
	"": true, "random": true, "tournament": true,
	"rank": true, "sus": true, "roulette": true,
	"truncation": true, "lineage_rank": true, "nsga2": true, "nondominated": true,
}

func WithSelectionStrategy(strategy string) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if validSelectionStrategies[strategy] {
			cfg.SelectionStrategy = strategy
			return nil
		}
		return fmt.Errorf("unknown selection strategy: %q (valid: tournament, rank, sus, roulette, truncation, lineage_rank, random)", strategy)
	}
}

func WithDiversityWeights(w DiversityWeightConfig) PopulationOption {
	return func(cfg *PopulationConfig) error {
		cfg.DiversityWeights = w
		return nil
	}
}

func WithFitnessSharingSampling(limit, size int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if limit < 0 {
			return fmt.Errorf("%w: limit must be >= 0", ErrInvalidMutationRate)
		}
		if size < 0 {
			return fmt.Errorf("%w: size must be >= 0", ErrInvalidMutationRate)
		}
		if limit > 0 && size >= limit {
			return fmt.Errorf("%w: size (%d) must be < limit (%d)", ErrInvalidMutationRate, size, limit)
		}
		cfg.FitnessSharingSampleLimit = limit
		cfg.FitnessSharingSampleSize = size
		return nil
	}
}

func WithHistoryEnabled(maxSize int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if maxSize < 0 {
			return fmt.Errorf("history max size must be >= 0, got %d", maxSize)
		}
		cfg.HistoryMaxSize = maxSize
		return nil
	}
}

func WithPerLineageElites(enabled bool) PopulationOption {
	return func(cfg *PopulationConfig) error {
		cfg.PerLineageElites = enabled
		return nil
	}
}

func WithPerLineageEliteCount(count int) PopulationOption {
	return func(cfg *PopulationConfig) error {
		if count < 1 {
			return fmt.Errorf("per-lineage elite count must be at least 1, got %d", count)
		}
		cfg.PerLineageEliteCount = count
		return nil
	}
}

func WithAdaptiveConfig(ac *AdaptiveConfig) PopulationOption {
	return func(cfg *PopulationConfig) error {
		cfg.AdaptiveConfig = ac
		return nil
	}
}
