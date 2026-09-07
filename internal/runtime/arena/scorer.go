package arena

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// EnsembleScorer combines multiple Scorers with configurable weights.
// The final score is the weighted sum of individual scorer outputs.
// This reduces variance by averaging across different evaluation methods
// (e.g., exact match + LLM judge).
type EnsembleScorer struct {
	scorers []Scorer
	weights []float64
}

// NewEnsembleScorer creates an ensemble scorer from a list of (scorer, weight) pairs.
// Weights are normalized to sum to 1.0 internally.
//
// Args:
//   - pairs: alternating scorer and weight arguments (e.g., scorer1, 0.5, scorer2, 0.5).
//
// Returns:
//   - *EnsembleScorer: the configured ensemble.
//   - error: ErrNilScorer if any scorer is nil, or invalid weight.
func NewEnsembleScorer(pairs ...any) (*EnsembleScorer, error) {
	if len(pairs)%2 != 0 {
		return nil, errors.New("arena: ensemble scorer requires pairs of (scorer, weight)")
	}

	n := len(pairs) / 2
	scorers := make([]Scorer, 0, n)
	weights := make([]float64, 0, n)

	var totalWeight float64
	for i := 0; i < n; i++ {
		s, ok := pairs[i*2].(Scorer)
		if !ok || s == nil {
			return nil, ErrNilScorer
		}
		w, ok := pairs[i*2+1].(float64)
		if !ok || w <= 0 {
			return nil, fmt.Errorf("arena: ensemble scorer weight must be positive, got %v", pairs[i*2+1])
		}
		scorers = append(scorers, s)
		weights = append(weights, w)
		totalWeight += w
	}

	// Normalize weights.
	for i := range weights {
		weights[i] /= totalWeight
	}

	return &EnsembleScorer{scorers: scorers, weights: weights}, nil
}

// Score calls each sub-scorer and returns the weighted average.
// If any sub-scorer fails, the error is returned immediately.
// The context is forwarded to sub-scorers so slow (LLM-backed) scorers
// can observe cancellation (M4).
func (es *EnsembleScorer) Score(ctx context.Context, input any) (float64, error) {
	var weightedSum float64
	for i, s := range es.scorers {
		score, err := s.Score(ctx, input)
		if err != nil {
			return 0, fmt.Errorf("arena: ensemble scorer[%d]: %w", i, err)
		}
		weightedSum += score * es.weights[i]
	}
	// Clamp to [0, 1] for consistency.
	if weightedSum > 1.0 {
		weightedSum = 1.0
	}
	if weightedSum < 0.0 {
		weightedSum = 0.0
	}
	return math.Round(weightedSum*10000) / 10000, nil
}

// ExactMatchScorer returns 1.0 if the output matches expected, 0.0 otherwise.
// This is a Scorer adapter for use with EnsembleScorer.
// The input is expected to be a map[string]string or a struct with
// "actual_output" and "expected_output" fields.
//
// For simplicity, this uses the string representation of the input.
// In production, use the eval.ExactMatchEvaluator for structured evaluation.
type ExactMatchScorer struct {
	Expected func(input any) string
	Actual   func(input any) string
}

// NewExactMatchScorer creates a scorer that compares actual vs expected output.
//
// Args:
//   - expected: function extracting the expected output from the input.
//   - actual: function extracting the actual output from the input.
//
// Returns:
//   - *ExactMatchScorer.
func NewExactMatchScorer(expected, actual func(any) string) *ExactMatchScorer {
	return &ExactMatchScorer{Expected: expected, Actual: actual}
}

// Score returns 1.0 on exact match, 0.0 otherwise.
func (s *ExactMatchScorer) Score(_ context.Context, input any) (float64, error) {
	expected := s.Expected(input)
	actual := s.Actual(input)
	if actual == expected {
		return 1.0, nil
	}
	return 0.0, nil
}

// MapScorer wraps a function as a Scorer.
type MapScorer struct {
	fn func(ctx context.Context, input any) (float64, error)
}

// NewMapScorer creates a scorer from an arbitrary scoring function.
// The wrapped function receives the context so slow scorers can observe
// cancellation (M4).
func NewMapScorer(fn func(ctx context.Context, input any) (float64, error)) *MapScorer {
	return &MapScorer{fn: fn}
}

// Score delegates to the wrapped function.
func (s *MapScorer) Score(ctx context.Context, input any) (float64, error) {
	return s.fn(ctx, input)
}
