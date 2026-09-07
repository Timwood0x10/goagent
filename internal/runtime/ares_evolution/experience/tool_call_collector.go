package experience

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ToolCallExperienceCollector converts ToolCallRecord values into normalized
// experiences and persists them in an ExperienceStore. This bridges raw
// observability data (each tool invocation) into the evolution experience
// pipeline so the GA / MemoryAwareScorer can learn from actual tool usage.
type ToolCallExperienceCollector struct {
	normalizer Normalizer
	store      ExperienceStore
}

// NewToolCallExperienceCollector creates a ToolCallExperienceCollector.
// Returns an error if normalizer or store is nil.
func NewToolCallExperienceCollector(normalizer Normalizer, store ExperienceStore) (*ToolCallExperienceCollector, error) {
	if normalizer == nil {
		return nil, errors.New("normalizer must not be nil")
	}
	if store == nil {
		return nil, errors.New("store must not be nil")
	}
	return &ToolCallExperienceCollector{
		normalizer: normalizer,
		store:      store,
	}, nil
}

// Collect converts a single ToolCallRecord into a NormalizedExperience and
// appends it to the store. Filtered records are silently dropped with a debug log.
func (c *ToolCallExperienceCollector) Collect(ctx context.Context, record ToolCallRecord) error {
	if c == nil {
		return errors.New("tool call collector is nil")
	}

	raw := recordToRaw(record)

	normalized, err := c.normalizer.Normalize(ctx, raw)
	if err != nil {
		return fmt.Errorf("normalize tool call: %w", err)
	}

	if normalized.IsFiltered {
		slog.DebugContext(ctx, "tool call filtered by normalizer",
			"tool", record.ToolName,
			"strategy_id", record.StrategyID,
			"reason", normalized.FilterReason,
		)
		return nil
	}

	if err := c.store.Append(ctx, normalized); err != nil {
		return fmt.Errorf("store tool call: %w", err)
	}
	return nil
}

// CollectBatch converts multiple ToolCallRecords into normalized experiences
// and appends them to the store in one batch. This is more efficient than
// calling Collect repeatedly.
func (c *ToolCallExperienceCollector) CollectBatch(ctx context.Context, records []ToolCallRecord) error {
	if c == nil {
		return errors.New("tool call collector is nil")
	}

	raws := make([]RawExperience, 0, len(records))
	for _, rec := range records {
		raws = append(raws, recordToRaw(rec))
	}

	normalized, err := c.normalizer.NormalizeBatch(ctx, raws)
	if err != nil {
		return fmt.Errorf("normalize tool call batch: %w", err)
	}

	// Keep only non-filtered experiences.
	var toStore []NormalizedExperience
	for _, n := range normalized {
		if !n.IsFiltered {
			toStore = append(toStore, n)
		}
	}

	if len(toStore) == 0 {
		return nil
	}

	if err := c.store.AppendBatch(ctx, toStore); err != nil {
		return fmt.Errorf("store tool call batch: %w", err)
	}
	return nil
}

// recordToRaw maps a ToolCallRecord to a RawExperience for normalization.
func recordToRaw(rec ToolCallRecord) RawExperience {
	raw := RawExperience{
		StrategyID:   rec.StrategyID,
		TaskType:     rec.TaskType,
		Timestamp:    rec.Timestamp,
		MutationType: "tool_call",
		Score:        boolToScore(rec.Success),
		Latency:      rec.LatencyMs,
		Success:      rec.Success,
		Metadata: map[string]interface{}{
			"tool_name":         rec.ToolName,
			"input_summary":     rec.InputSummary,
			"output_summary":    rec.OutputSummary,
			"error_code":        rec.ErrorCode,
			"retry_count":       rec.RetryCount,
			"result_size_bytes": rec.ResultSizeBytes,
		},
	}

	if rec.ErrorCode != "" {
		raw.ErrorRate = 1.0
		raw.Cost = float64(rec.ResultSizeBytes + int64(rec.RetryCount)*1024)
	} else {
		raw.ErrorRate = 0.0
		raw.Cost = float64(rec.ResultSizeBytes)
	}

	return raw
}

func boolToScore(v bool) float64 {
	if v {
		return 1.0
	}
	return 0.0
}
