// Package experience provides unit tests for the normalizer implementation.
package experience

import (
	"context"
	"testing"
	"time"
)

// TestNormalizeNormalCase tests the normal normalization flow.
func TestNormalizeNormalCase(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()
	raw := RawExperience{
		StrategyID:   "strategy-001",
		TaskType:     "code_generation",
		Timestamp:    now,
		Score:        85.5,
		Latency:      "500ms",
		WallTime:     2.5,
		ErrorRate:    0.05,
		Success:      true,
		Cost:         10.0,
		MutationType: "adaptive",
		Metadata: map[string]interface{}{
			"key1": "value1",
			"key2": 123,
		},
	}

	normalized, err := normalizer.Normalize(ctx, raw)
	if err != nil {
		t.Fatalf("Normalize() returned unexpected error: %v", err)
	}

	// Verify basic fields
	if normalized.StrategyID != raw.StrategyID {
		t.Errorf("StrategyID mismatch: got %s, want %s", normalized.StrategyID, raw.StrategyID)
	}
	if normalized.TaskType != raw.TaskType {
		t.Errorf("TaskType mismatch: got %s, want %s", normalized.TaskType, raw.TaskType)
	}
	if normalized.CreatedAt != raw.Timestamp.UTC() {
		t.Errorf("CreatedAt mismatch: got %v, want %v", normalized.CreatedAt, raw.Timestamp.UTC())
	}

	// Verify normalized values (score divided by 100 from percentage scale)
	if normalized.Score != 0.855 {
		t.Errorf("Score mismatch: got %f, want 0.855", normalized.Score)
	}
	if normalized.LatencyMs != 500 {
		t.Errorf("LatencyMs mismatch: got %d, want 500", normalized.LatencyMs)
	}
	if normalized.WallTimeSeconds != 2.5 {
		t.Errorf("WallTimeSeconds mismatch: got %f, want 2.5", normalized.WallTimeSeconds)
	}
	if normalized.ErrorRate != 0.05 {
		t.Errorf("ErrorRate mismatch: got %f, want 0.05", normalized.ErrorRate)
	}
	if normalized.Success != true {
		t.Errorf("Success mismatch: got %v, want true", normalized.Success)
	}
	if normalized.Cost != 10.0 {
		t.Errorf("Cost mismatch: got %f, want 10.0", normalized.Cost)
	}
	if normalized.MutationType != "adaptive" {
		t.Errorf("MutationType mismatch: got %s, want adaptive", normalized.MutationType)
	}

	// Verify metadata
	if len(normalized.Metadata) != 2 {
		t.Errorf("Metadata count mismatch: got %d, want 2", len(normalized.Metadata))
	}

	// Verify not filtered
	if normalized.IsFiltered {
		t.Errorf("IsFiltered should be false for valid experience")
	}
	if normalized.FilterReason != "" {
		t.Errorf("FilterReason should be empty for valid experience")
	}
}

// TestNormalizeMissingFields tests normalization with missing required fields.
func TestNormalizeMissingFields(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	tests := []struct {
		name        string
		raw         RawExperience
		expectedErr string
	}{
		{
			name: "missing strategy_id",
			raw: RawExperience{
				TaskType:  "code_generation",
				Timestamp: time.Now(),
			},
			expectedErr: "missing required field: strategy_id",
		},
		{
			name: "missing task_type",
			raw: RawExperience{
				StrategyID: "strategy-001",
				Timestamp:  time.Now(),
			},
			expectedErr: "missing required field: task_type",
		},
		{
			name: "missing timestamp",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
			},
			expectedErr: "missing required field: timestamp",
		},
		{
			name:        "all fields missing",
			raw:         RawExperience{},
			expectedErr: "missing required field: strategy_id",
		},
	}

	for _, test := range tests {
		test := test // capture range variable
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizer.Normalize(ctx, test.raw)
			if err == nil {
				t.Fatalf("Normalize() should return error for %s", test.name)
			}
			if err.Error() != test.expectedErr {
				t.Errorf("Error mismatch: got %s, want %s", err.Error(), test.expectedErr)
			}
		})
	}
}

// TestNormalizeOutliers tests filtering of outlier experiences.
func TestNormalizeOutliers(t *testing.T) {
	t.Parallel()

	// Create config with stricter thresholds
	config := &NormalizerConfig{
		MaxLatencyMs:           5000, // 5 seconds
		MaxErrorRate:           0.5,  // 50%
		DedupWindowMinutes:     1,
		DefaultScore:           0.0,
		DefaultLatencyMs:       0,
		DefaultWallTimeSeconds: 0.0,
		DefaultErrorRate:       0.0,
		DefaultSuccess:         false,
		DefaultCost:            0.0,
	}
	normalizer := NewDefaultNormalizer(config)
	ctx := context.Background()

	now := time.Now()

	tests := []struct {
		name           string
		raw            RawExperience
		expectFiltered bool
		expectedReason string
	}{
		{
			name: "latency exceeds threshold",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    "15s", // 15000ms > 5000ms threshold
			},
			expectFiltered: true,
			expectedReason: "latency 15000ms exceeds threshold 5000ms",
		},
		{
			name: "error_rate exceeds threshold",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  0.75, // 75% > 50% threshold
			},
			expectFiltered: true,
			expectedReason: "error_rate 0.75 exceeds threshold 0.50",
		},
		{
			name: "both exceed thresholds",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    "10s",
				ErrorRate:  0.8,
			},
			expectFiltered: true,
			expectedReason: "latency 10000ms exceeds threshold 5000ms; error_rate 0.80 exceeds threshold 0.50",
		},
		{
			name: "within thresholds",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    "3s",
				ErrorRate:  0.3,
			},
			expectFiltered: false,
			expectedReason: "",
		},
	}

	for _, test := range tests {
		test := test // capture range variable
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := normalizer.Normalize(ctx, test.raw)
			if err != nil {
				t.Fatalf("Normalize() returned unexpected error: %v", err)
			}

			if normalized.IsFiltered != test.expectFiltered {
				t.Errorf("IsFiltered mismatch: got %v, want %v", normalized.IsFiltered, test.expectFiltered)
			}

			if normalized.FilterReason != test.expectedReason {
				t.Errorf("FilterReason mismatch: got %s, want %s", normalized.FilterReason, test.expectedReason)
			}
		})
	}
}

// TestNormalizeInvalidInputs tests handling of invalid input values.
func TestNormalizeInvalidInputs(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()

	tests := []struct {
		name           string
		raw            RawExperience
		checkFunc      func(NormalizedExperience) bool
		expectedResult bool
	}{
		{
			name: "invalid latency string",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    "invalid",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.LatencyMs == 0 // should use default
			},
			expectedResult: true,
		},
		{
			name: "invalid wall_time string",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				WallTime:   "invalid",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.WallTimeSeconds == 0.0 // should use default
			},
			expectedResult: true,
		},
		{
			name: "invalid error_rate string",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  "invalid",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.ErrorRate == 0.0 // should use default
			},
			expectedResult: true,
		},
		{
			name: "invalid cost string",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Cost:       "invalid",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.Cost == 0.0 // should use default
			},
			expectedResult: true,
		},
		{
			name: "nil metadata",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Metadata:   nil,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return len(n.Metadata) == 0
			},
			expectedResult: true,
		},
		{
			name: "metadata with nil values",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Metadata: map[string]interface{}{
					"valid": "value",
					"nil":   nil,
					"empty": "",
				},
			},
			checkFunc: func(n NormalizedExperience) bool {
				return len(n.Metadata) == 1 && n.Metadata["valid"] == "value"
			},
			expectedResult: true,
		},
	}

	for _, test := range tests {
		test := test // capture range variable
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := normalizer.Normalize(ctx, test.raw)
			if err != nil {
				t.Fatalf("Normalize() returned unexpected error: %v", err)
			}

			if !test.checkFunc(normalized) {
				t.Errorf("Check function failed for test %s", test.name)
			}
		})
	}
}

// TestNormalizeTypeConversion tests various type conversion scenarios.
func TestNormalizeTypeConversion(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()

	tests := []struct {
		name      string
		raw       RawExperience
		checkFunc func(NormalizedExperience) bool
	}{
		{
			name: "int latency",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    1000,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.LatencyMs == 1000
			},
		},
		{
			name: "duration latency",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    time.Duration(2) * time.Second,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.LatencyMs == 2000
			},
		},
		{
			name: "string duration latency",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Latency:    "1.5s",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.LatencyMs == 1500
			},
		},
		{
			name: "percentage error_rate",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  25, // 25% -> 0.25
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.ErrorRate == 0.25
			},
		},
		{
			name: "fraction error_rate",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  0.15,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.ErrorRate == 0.15
			},
		},
		{
			name: "negative error_rate clamped",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  -0.5,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.ErrorRate == 0.0
			},
		},
		{
			name: "large error_rate clamped",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				ErrorRate:  150, // 150% -> 1.5 -> clamped to 1.0
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.ErrorRate == 1.0
			},
		},
		{
			name: "string success true",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Success:    "true",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.Success == true
			},
		},
		{
			name: "string success yes",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Success:    "yes",
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.Success == true
			},
		},
		{
			name: "int success",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Success:    1,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.Success == true
			},
		},
		{
			name: "int zero success",
			raw: RawExperience{
				StrategyID: "strategy-001",
				TaskType:   "code_generation",
				Timestamp:  now,
				Success:    0,
			},
			checkFunc: func(n NormalizedExperience) bool {
				return n.Success == false
			},
		},
	}

	for _, test := range tests {
		test := test // capture range variable
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			normalized, err := normalizer.Normalize(ctx, test.raw)
			if err != nil {
				t.Fatalf("Normalize() returned unexpected error: %v", err)
			}

			if !test.checkFunc(normalized) {
				t.Errorf("Check function failed for test %s", test.name)
			}
		})
	}
}

// TestNormalizeBatchNormalCase tests batch normalization with valid inputs.
func TestNormalizeBatchNormalCase(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()
	raws := []RawExperience{
		{
			StrategyID: "strategy-001",
			TaskType:   "code_generation",
			Timestamp:  now,
			Score:      85.0,
		},
		{
			StrategyID: "strategy-002",
			TaskType:   "analysis",
			Timestamp:  now.Add(1 * time.Minute),
			Score:      90.0,
		},
		{
			StrategyID: "strategy-003",
			TaskType:   "bug_fix",
			Timestamp:  now.Add(2 * time.Minute),
			Score:      75.0,
		},
	}

	normalized, err := normalizer.NormalizeBatch(ctx, raws)
	if err != nil {
		t.Fatalf("NormalizeBatch() returned unexpected error: %v", err)
	}

	if len(normalized) != 3 {
		t.Errorf("Normalized count mismatch: got %d, want 3", len(normalized))
	}

	// Verify each experience
	for i, norm := range normalized {
		if norm.StrategyID != raws[i].StrategyID {
			t.Errorf("StrategyID mismatch at index %d: got %s, want %s", i, norm.StrategyID, raws[i].StrategyID)
		}
		if norm.TaskType != raws[i].TaskType {
			t.Errorf("TaskType mismatch at index %d: got %s, want %s", i, norm.TaskType, raws[i].TaskType)
		}
	}
}

// TestNormalizeBatchDeduplication tests deduplication in batch normalization.
func TestNormalizeBatchDeduplication(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()

	// Create experiences with duplicates (same strategy_id + task_type within 1 minute)
	raws := []RawExperience{
		{
			StrategyID: "strategy-001",
			TaskType:   "code_generation",
			Timestamp:  now,
			Score:      85.0,
		},
		{
			StrategyID: "strategy-001",            // Duplicate
			TaskType:   "code_generation",         // Duplicate
			Timestamp:  now.Add(30 * time.Second), // Within 1 minute window
			Score:      90.0,
		},
		{
			StrategyID: "strategy-001", // Same strategy_id + task_type
			TaskType:   "code_generation",
			Timestamp:  now.Add(2 * time.Minute), // Outside 1 minute window
			Score:      80.0,
		},
		{
			StrategyID: "strategy-002",
			TaskType:   "code_generation", // Different strategy_id
			Timestamp:  now,
			Score:      75.0,
		},
	}

	normalized, err := normalizer.NormalizeBatch(ctx, raws)
	if err != nil {
		t.Fatalf("NormalizeBatch() returned unexpected error: %v", err)
	}

	// Should have 3 experiences (one duplicate removed)
	if len(normalized) != 3 {
		t.Errorf("Normalized count mismatch: got %d, want 3 (one duplicate should be removed)", len(normalized))
	}
}

// TestNormalizeBatchWithInvalid tests batch normalization with some invalid inputs.
func TestNormalizeBatchWithInvalid(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	now := time.Now()

	raws := []RawExperience{
		{
			StrategyID: "strategy-001",
			TaskType:   "code_generation",
			Timestamp:  now,
			Score:      85.0,
		},
		{
			// Invalid: missing strategy_id
			TaskType:  "analysis",
			Timestamp: now,
		},
		{
			StrategyID: "strategy-003",
			TaskType:   "bug_fix",
			Timestamp:  now,
			Score:      75.0,
		},
	}

	normalized, err := normalizer.NormalizeBatch(ctx, raws)
	if err != nil {
		t.Fatalf("NormalizeBatch() returned unexpected error: %v", err)
	}

	// Should have 2 experiences (invalid one skipped)
	if len(normalized) != 2 {
		t.Errorf("Normalized count mismatch: got %d, want 2 (invalid one should be skipped)", len(normalized))
	}
}

// TestNormalizeBatchNilInput tests batch normalization with nil input.
func TestNormalizeBatchNilInput(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	_, err := normalizer.NormalizeBatch(ctx, nil)
	if err == nil {
		t.Fatal("NormalizeBatch() should return error for nil input")
	}
	if err.Error() != "input slice cannot be nil" {
		t.Errorf("Error mismatch: got %s, want 'input slice cannot be nil'", err.Error())
	}
}

// TestNormalizeBatchEmptyInput tests batch normalization with empty input.
func TestNormalizeBatchEmptyInput(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx := context.Background()

	normalized, err := normalizer.NormalizeBatch(ctx, []RawExperience{})
	if err != nil {
		t.Fatalf("NormalizeBatch() returned unexpected error: %v", err)
	}

	if len(normalized) != 0 {
		t.Errorf("Normalized count mismatch: got %d, want 0", len(normalized))
	}
}

// TestNormalizeContextCancellation tests handling of context cancellation.
func TestNormalizeContextCancellation(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	now := time.Now()
	raw := RawExperience{
		StrategyID: "strategy-001",
		TaskType:   "code_generation",
		Timestamp:  now,
	}

	_, err := normalizer.Normalize(ctx, raw)
	if err == nil {
		t.Fatal("Normalize() should return error for cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("Error mismatch: got %v, want context.Canceled", err)
	}
}

// TestNormalizeBatchContextCancellation tests handling of context cancellation in batch.
func TestNormalizeBatchContextCancellation(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	now := time.Now()
	raws := []RawExperience{
		{
			StrategyID: "strategy-001",
			TaskType:   "code_generation",
			Timestamp:  now,
		},
	}

	_, err := normalizer.NormalizeBatch(ctx, raws)
	if err == nil {
		t.Fatal("NormalizeBatch() should return error for cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("Error mismatch: got %v, want context.Canceled", err)
	}
}

// TestNewDefaultNormalizerWithNilConfig tests constructor with nil config.
func TestNewDefaultNormalizerWithNilConfig(t *testing.T) {
	t.Parallel()

	normalizer := NewDefaultNormalizer(nil)
	if normalizer == nil {
		t.Fatal("NewDefaultNormalizer() should not return nil")
	}

	// Verify default config is applied
	if normalizer.config.MaxLatencyMs != 10000 {
		t.Errorf("Default MaxLatencyMs mismatch: got %d, want 10000", normalizer.config.MaxLatencyMs)
	}
}

// TestNewDefaultNormalizerWithCustomConfig tests constructor with custom config.
func TestNewDefaultNormalizerWithCustomConfig(t *testing.T) {
	t.Parallel()

	config := &NormalizerConfig{
		MaxLatencyMs:           5000,
		MaxErrorRate:           0.3,
		DedupWindowMinutes:     2,
		DefaultScore:           50.0,
		DefaultLatencyMs:       100,
		DefaultWallTimeSeconds: 1.0,
		DefaultErrorRate:       0.1,
		DefaultSuccess:         true,
		DefaultCost:            5.0,
	}

	normalizer := NewDefaultNormalizer(config)
	if normalizer == nil {
		t.Fatal("NewDefaultNormalizer() should not return nil")
	}

	// Verify custom config is applied
	if normalizer.config.MaxLatencyMs != 5000 {
		t.Errorf("Custom MaxLatencyMs mismatch: got %d, want 5000", normalizer.config.MaxLatencyMs)
	}
	if normalizer.config.MaxErrorRate != 0.3 {
		t.Errorf("Custom MaxErrorRate mismatch: got %f, want 0.3", normalizer.config.MaxErrorRate)
	}
}

// TestDefaultNormalizerConfig tests the default config function.
func TestDefaultNormalizerConfig(t *testing.T) {
	t.Parallel()

	config := DefaultNormalizerConfig()
	if config == nil {
		t.Fatal("DefaultNormalizerConfig() should not return nil")
	}

	// Verify default values
	if config.MaxLatencyMs != 10000 {
		t.Errorf("Default MaxLatencyMs: got %d, want 10000", config.MaxLatencyMs)
	}
	if config.MaxErrorRate != 1.0 {
		t.Errorf("Default MaxErrorRate: got %f, want 1.0", config.MaxErrorRate)
	}
	if config.DedupWindowMinutes != 1 {
		t.Errorf("Default DedupWindowMinutes: got %d, want 1", config.DedupWindowMinutes)
	}
}

// TestNormalizeBatchWithFiltered tests that filtered experiences are kept in batch.
func TestNormalizeBatchWithFiltered(t *testing.T) {
	t.Parallel()

	config := &NormalizerConfig{
		MaxLatencyMs:           1000, // Very strict
		MaxErrorRate:           1.0,
		DedupWindowMinutes:     1,
		DefaultScore:           0.0,
		DefaultLatencyMs:       0,
		DefaultWallTimeSeconds: 0.0,
		DefaultErrorRate:       0.0,
		DefaultSuccess:         false,
		DefaultCost:            0.0,
	}
	normalizer := NewDefaultNormalizer(config)
	ctx := context.Background()

	now := time.Now()

	raws := []RawExperience{
		{
			StrategyID: "strategy-001",
			TaskType:   "code_generation",
			Timestamp:  now,
			Latency:    "5s", // Will be filtered (5000ms > 1000ms)
		},
		{
			StrategyID: "strategy-002",
			TaskType:   "analysis",
			Timestamp:  now,
			Latency:    "500ms", // Within threshold
		},
	}

	normalized, err := normalizer.NormalizeBatch(ctx, raws)
	if err != nil {
		t.Fatalf("NormalizeBatch() returned unexpected error: %v", err)
	}

	// Should have 2 experiences (filtered one is still included)
	if len(normalized) != 2 {
		t.Errorf("Normalized count mismatch: got %d, want 2", len(normalized))
	}

	// Check filtered status
	if !normalized[0].IsFiltered {
		t.Error("First experience should be filtered")
	}
	if normalized[1].IsFiltered {
		t.Error("Second experience should not be filtered")
	}
}
