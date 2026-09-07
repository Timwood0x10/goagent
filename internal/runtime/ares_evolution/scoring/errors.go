// Package scoring ...
package scoring

import "errors"

var (
	ErrNilCache            = errors.New("cache must not be nil")
	ErrNilUnderlyingScorer = errors.New("underlying scorer must not be nil")
	ErrNilStrategy         = errors.New("strategy must not be nil")
	ErrNilTieredCache      = errors.New("cache must not be nil")
	ErrNilBudget           = errors.New("budget must not be nil")
	ErrNilHeuristicScorer  = errors.New("heuristic scorer must not be nil")
	ErrInvalidBudgetLimit  = errors.New("max LLM calls must be > 0")
)
