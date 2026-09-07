// Package experience provides types and utilities for managing evolution experiences.
// This file implements the Normalizer interface and DefaultNormalizer for transforming
// raw experience data into normalized, validated, and deduplicated experiences.
package experience

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Normalizer defines the interface for normalizing raw experiences.
// Implementations should handle field unification, type conversion,
// noise filtering, missing value filling, and deduplication.
type Normalizer interface {
	// Normalize converts a single raw experience into a normalized experience.
	// Returns an error for invalid inputs (nil, missing required fields).
	// Returns a filtered experience (IsFiltered=true) for noise/outliers.
	Normalize(ctx context.Context, raw RawExperience) (NormalizedExperience, error)

	// NormalizeBatch converts multiple raw experiences into normalized experiences.
	// Returns an error if the input slice is nil.
	// Experiences are deduplicated based on strategy_id + task_type + timestamp.
	NormalizeBatch(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error)
}

// DefaultNormalizer implements the Normalizer interface with configurable thresholds.
type DefaultNormalizer struct {
	config *NormalizerConfig
}

// NewDefaultNormalizer creates a new DefaultNormalizer with the given configuration.
// If config is nil, default configuration values are used.
func NewDefaultNormalizer(config *NormalizerConfig) *DefaultNormalizer {
	if config == nil {
		config = DefaultNormalizerConfig()
	}
	return &DefaultNormalizer{
		config: config,
	}
}

// Normalize converts a raw experience into a normalized experience.
// It performs field unification, type conversion, noise filtering,
// and missing value filling.
//
// Args:
//   - ctx: context for cancellation and timeout.
//   - raw: the raw experience to normalize.
//
// Returns:
//   - NormalizedExperience: the normalized experience (may be filtered).
//   - error: ErrNilInput if raw is invalid, ErrMissingRequiredField if required fields are missing.
func (n *DefaultNormalizer) Normalize(ctx context.Context, raw RawExperience) (NormalizedExperience, error) {
	// Check for context cancellation
	select {
	case <-ctx.Done():
		return NormalizedExperience{}, ctx.Err()
	default:
	}

	// Validate required fields
	if raw.StrategyID == "" {
		return NormalizedExperience{}, errors.New("missing required field: strategy_id")
	}
	if raw.TaskType == "" {
		return NormalizedExperience{}, errors.New("missing required field: task_type")
	}
	if raw.Timestamp.IsZero() {
		return NormalizedExperience{}, errors.New("missing required field: timestamp")
	}

	// Create normalized experience with defaults
	normalized := NormalizedExperience{
		ID:              uuid.New().String(),
		StrategyID:      raw.StrategyID,
		TaskType:        raw.TaskType,
		CreatedAt:       raw.Timestamp.UTC(),
		Score:           n.config.DefaultScore,
		LatencyMs:       n.config.DefaultLatencyMs,
		WallTimeSeconds: n.config.DefaultWallTimeSeconds,
		ErrorRate:       n.config.DefaultErrorRate,
		Success:         n.config.DefaultSuccess,
		Cost:            n.config.DefaultCost,
		MutationType:    raw.MutationType,
		Metadata:        make(map[string]interface{}),
		IsFiltered:      false,
		FilterReason:    "",
	}

	// Copy and clean metadata
	if raw.Metadata != nil {
		for key, value := range raw.Metadata {
			// Filter out nil values and empty strings
			if value != nil && value != "" {
				normalized.Metadata[key] = value
			}
		}
	}

	// Normalize score
	if raw.Score != nil {
		normalized.Score = n.normalizeScore(raw.Score)
	}

	// Normalize latency (convert to milliseconds)
	if raw.Latency != nil {
		latencyMs, err := n.normalizeLatency(raw.Latency)
		if err != nil {
			normalized.LatencyMs = n.config.DefaultLatencyMs
		} else {
			normalized.LatencyMs = latencyMs
		}
	}

	// Normalize wall time (convert to seconds)
	if raw.WallTime != nil {
		wallTimeSeconds, err := n.normalizeWallTime(raw.WallTime)
		if err != nil {
			normalized.WallTimeSeconds = n.config.DefaultWallTimeSeconds
		} else {
			normalized.WallTimeSeconds = wallTimeSeconds
		}
	}

	// Normalize error rate (convert to fraction [0, 1])
	if raw.ErrorRate != nil {
		errorRate, err := n.normalizeErrorRate(raw.ErrorRate)
		if err != nil {
			normalized.ErrorRate = n.config.DefaultErrorRate
		} else {
			normalized.ErrorRate = errorRate
		}
	}

	// Normalize success
	if raw.Success != nil {
		normalized.Success = n.normalizeSuccess(raw.Success)
	}

	// Normalize cost
	if raw.Cost != nil {
		cost, err := n.normalizeCost(raw.Cost)
		if err != nil {
			normalized.Cost = n.config.DefaultCost
		} else {
			normalized.Cost = cost
		}
	}

	// Apply noise filtering
	if n.isNoise(normalized) {
		normalized.IsFiltered = true
		normalized.FilterReason = n.getFilterReason(normalized)
	}

	return normalized, nil
}

// NormalizeBatch converts multiple raw experiences into normalized experiences.
// It applies deduplication based on strategy_id + task_type + timestamp within
// a configurable time window.
//
// Args:
//   - ctx: context for cancellation and timeout.
//   - raws: the slice of raw experiences to normalize.
//
// Returns:
//   - []NormalizedExperience: the normalized experiences (excluding duplicates).
//   - error: ErrNilInput if raws is nil.
func (n *DefaultNormalizer) NormalizeBatch(ctx context.Context, raws []RawExperience) ([]NormalizedExperience, error) {
	if raws == nil {
		return nil, errors.New("input slice cannot be nil")
	}

	if len(raws) == 0 {
		return []NormalizedExperience{}, nil
	}

	// Normalize all experiences first
	normalized := make([]NormalizedExperience, 0, len(raws))
	for _, raw := range raws {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		norm, err := n.Normalize(ctx, raw)
		if err != nil {
			// Skip invalid experiences but continue processing
			continue
		}
		normalized = append(normalized, norm)
	}

	// Deduplicate based on strategy_id + task_type + timestamp window
	deduped := n.deduplicate(normalized)

	return deduped, nil
}

// normalizeScore converts various score types to float64 in range [0, 1].
// Values > 1.0 are treated as percentages (divided by 100).
func (n *DefaultNormalizer) normalizeScore(value interface{}) float64 {
	switch v := value.(type) {
	case int:
		score := float64(v)
		if score > 1.0 {
			return score / 100.0
		}
		return score
	case int32:
		score := float64(v)
		if score > 1.0 {
			return score / 100.0
		}
		return score
	case int64:
		score := float64(v)
		if score > 1.0 {
			return score / 100.0
		}
		return score
	case float32:
		score := float64(v)
		if score > 1.0 {
			return score / 100.0
		}
		return score
	case float64:
		if v > 1.0 {
			return v / 100.0
		}
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			if f > 1.0 {
				return f / 100.0
			}
			return f
		}
	}
	return n.config.DefaultScore
}

// normalizeLatency converts various latency types to milliseconds.
// Supports: milliseconds (int/float), seconds (with 's' suffix), duration strings.
func (n *DefaultNormalizer) normalizeLatency(value interface{}) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case float32:
		return int64(v), nil
	case float64:
		return int64(v), nil
	case string:
		// Try parsing as duration (e.g., "100ms", "1.5s")
		if duration, err := time.ParseDuration(v); err == nil {
			return duration.Milliseconds(), nil
		}
		// Try parsing as plain number
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return int64(f), nil
		}
		return 0, fmt.Errorf("cannot parse latency: %s", v)
	case time.Duration:
		return v.Milliseconds(), nil
	default:
		return 0, fmt.Errorf("unsupported latency type: %T", value)
	}
}

// normalizeWallTime converts various time types to seconds.
func (n *DefaultNormalizer) normalizeWallTime(value interface{}) (float64, error) {
	switch v := value.(type) {
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case float32:
		return float64(v), nil
	case float64:
		return v, nil
	case string:
		// Try parsing as duration (e.g., "100ms", "1.5s")
		if duration, err := time.ParseDuration(v); err == nil {
			return duration.Seconds(), nil
		}
		// Try parsing as plain number
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, nil
		}
		return 0, fmt.Errorf("cannot parse wall_time: %s", v)
	case time.Duration:
		return v.Seconds(), nil
	default:
		return 0, fmt.Errorf("unsupported wall_time type: %T", value)
	}
}

// normalizeErrorRate converts various error rate types to fraction [0, 1].
// Handles both percentage (0-100) and fraction (0-1) representations.
func (n *DefaultNormalizer) normalizeErrorRate(value interface{}) (float64, error) {
	var rate float64
	switch v := value.(type) {
	case int:
		rate = float64(v)
	case int32:
		rate = float64(v)
	case int64:
		rate = float64(v)
	case float32:
		rate = float64(v)
	case float64:
		rate = v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			rate = f
		} else {
			return 0, fmt.Errorf("cannot parse error_rate: %s", v)
		}
	default:
		return 0, fmt.Errorf("unsupported error_rate type: %T", value)
	}

	// Convert percentage to fraction if > 1
	if rate > 1.0 {
		rate /= 100.0
	}

	// Clamp to [0, 1]
	if rate < 0 {
		rate = 0
	}
	if rate > 1.0 {
		rate = 1.0
	}

	return rate, nil
}

// normalizeSuccess converts various success types to bool.
func (n *DefaultNormalizer) normalizeSuccess(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case int, int32, int64:
		return v != 0
	case float32, float64:
		return v != 0
	case string:
		lower := strings.ToLower(v)
		return lower == "true" || lower == "1" || lower == "yes" || lower == "success"
	default:
		return n.config.DefaultSuccess
	}
}

// normalizeCost converts various cost types to float64.
func (n *DefaultNormalizer) normalizeCost(value interface{}) (float64, error) {
	switch v := value.(type) {
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case float32:
		return float64(v), nil
	case float64:
		return v, nil
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, nil
		}
		return 0, fmt.Errorf("cannot parse cost: %s", v)
	default:
		return 0, fmt.Errorf("unsupported cost type: %T", value)
	}
}

// isNoise checks if the experience should be filtered as noise.
func (n *DefaultNormalizer) isNoise(exp NormalizedExperience) bool {
	// Filter by latency threshold
	if exp.LatencyMs > n.config.MaxLatencyMs {
		return true
	}

	// Filter by error rate threshold
	if exp.ErrorRate > n.config.MaxErrorRate {
		return true
	}

	return false
}

// getFilterReason returns a human-readable reason for filtering.
func (n *DefaultNormalizer) getFilterReason(exp NormalizedExperience) string {
	reasons := make([]string, 0)

	if exp.LatencyMs > n.config.MaxLatencyMs {
		reasons = append(reasons, fmt.Sprintf("latency %dms exceeds threshold %dms", exp.LatencyMs, n.config.MaxLatencyMs))
	}

	if exp.ErrorRate > n.config.MaxErrorRate {
		reasons = append(reasons, fmt.Sprintf("error_rate %.2f exceeds threshold %.2f", exp.ErrorRate, n.config.MaxErrorRate))
	}

	if len(reasons) == 0 {
		return "unknown"
	}

	return strings.Join(reasons, "; ")
}

// deduplicate removes duplicate experiences based on strategy_id + task_type + timestamp.
// Two experiences are considered duplicates if they have the same strategy_id and task_type,
// and their timestamps are within the configured time window.
func (n *DefaultNormalizer) deduplicate(experiences []NormalizedExperience) []NormalizedExperience {
	if len(experiences) == 0 {
		return experiences
	}

	window := time.Duration(n.config.DedupWindowMinutes) * time.Minute

	// Track seen experiences by their dedup key
	seen := make(map[string]int) // key -> index in result
	result := make([]NormalizedExperience, 0, len(experiences))

	for _, exp := range experiences {
		// Skip filtered experiences from deduplication consideration
		if exp.IsFiltered {
			result = append(result, exp)
			continue
		}

		// Create dedup key from strategy_id and task_type
		key := fmt.Sprintf("%s|%s", exp.StrategyID, exp.TaskType)

		// Check if we've seen this key before
		if idx, exists := seen[key]; exists {
			// Check if timestamps are within the dedup window
			existing := result[idx]
			timeDiff := exp.CreatedAt.Sub(existing.CreatedAt)
			if timeDiff < 0 {
				timeDiff = -timeDiff
			}

			// If within window, it's a duplicate - skip it
			if timeDiff <= window {
				continue
			}
		}

		// Not a duplicate, add to result
		seen[key] = len(result)
		result = append(result, exp)
	}

	return result
}
