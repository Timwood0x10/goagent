// Package distillation ...
package distillation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

func (d *Distiller) GetMetrics() *DistillationMetrics {
	return &DistillationMetrics{
		AttemptTotal:     d.metrics.AttemptTotal.Load(),
		SuccessTotal:     d.metrics.SuccessTotal.Load(),
		FilteredNoise:    d.metrics.FilteredNoise.Load(),
		FilteredSecurity: d.metrics.FilteredSecurity.Load(),
		ConflictResolved: d.metrics.ConflictResolved.Load(),
		MemoriesCreated:  d.metrics.MemoriesCreated.Load(),
	}
}

// ResetMetrics resets the distillation metrics.
//
// Thread-safety: Uses atomic operations to safely reset metrics.
func (d *Distiller) ResetMetrics() {
	d.metrics.AttemptTotal.Store(0)
	d.metrics.SuccessTotal.Store(0)
	d.metrics.FilteredNoise.Store(0)
	d.metrics.FilteredSecurity.Store(0)
	d.metrics.ConflictResolved.Store(0)
	d.metrics.MemoriesCreated.Store(0)
}

// SubscribeAndDistill subscribes to an EventStore and automatically
// distills memories from incoming ares_events.
//
// When DistillationThreshold > 0, EventMessageAdded events accumulate until
// the threshold count is reached before being forwarded to processEvent,
// mirroring the v0.2.4 examples/knowledge-base config.yaml
// distillation_threshold semantics. EventTaskCompleted events bypass the
// gate and fire immediately. A threshold of 0 preserves the legacy
// ungated behaviour: every event fires immediately.
//
// Args:
//
//	ctx - operation context. Cancelling it closes the subscription.
//	store - the event store to subscribe to. If nil, this method is a no-op.
func (d *Distiller) SubscribeAndDistill(ctx context.Context, store ares_events.EventStore) {
	if store == nil {
		return
	}
	ch, err := store.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventMessageAdded,
			ares_events.EventTaskCompleted,
		},
	})
	if err != nil {
		log.Error("failed to subscribe to ares_events for distillation", "error", err)
		return
	}

	log.InfoContext(ctx, "[Memory Distillation] Event subscription started")

	// Lifecycle is ctx-driven: the goroutine exits on ctx cancellation or
	// channel close. The errgroup holds it so a panic in the subscription
	// loop surfaces through the group instead of killing the process.
	d.distillEg.Go(func() error {
		var roundCounter int
		for {
			select {
			case <-ctx.Done():
				log.InfoContext(ctx, "[Memory Distillation] Event subscription stopped by context")
				return ctx.Err()
			case event, ok := <-ch:
				if !ok {
					log.InfoContext(ctx, "[Memory Distillation] Event channel closed")
					return nil
				}
				// Task completion bypasses the round gate: tasks are terminal
				// signals whose distillation should not be delayed.
				if event.Type == ares_events.EventTaskCompleted {
					d.processEvent(ctx, event)
					continue
				}
				// Threshold 0 preserves legacy ungated behaviour.
				d.configMu.RLock()
				threshold := d.config.DistillationThreshold
				d.configMu.RUnlock()
				if threshold <= 0 {
					d.processEvent(ctx, event)
					continue
				}
				roundCounter++
				if roundCounter%threshold != 0 {
					log.DebugContext(ctx, "[Memory Distillation] Round gate holding",
						"round", roundCounter, "threshold", threshold)
					continue
				}
				log.InfoContext(ctx, "[Memory Distillation] Round gate reached, triggering distillation",
					"round", roundCounter, "threshold", threshold)
				d.processEvent(ctx, event)
			}
		}
	})
}

// processEvent handles a single event for distillation.
//
// Args:
//
//	ctx - operation context.
//	event - the event to process. If nil, this method is a no-op.
func (d *Distiller) processEvent(ctx context.Context, event *ares_events.Event) {
	if event == nil {
		return
	}
	switch event.Type {
	case ares_events.EventMessageAdded:
		role, _ := event.Payload["role"].(string)
		log.Debug("distiller received message event",
			"stream_id", event.StreamID,
			"role", role,
		)
		if d.OnMessageAdded != nil {
			d.OnMessageAdded(ctx, event.StreamID, role)
		}
	case ares_events.EventTaskCompleted:
		taskID, _ := event.Payload["task_id"].(string)
		log.Debug("distiller received task completion",
			"stream_id", event.StreamID,
			"task_id", taskID,
		)
		if taskID != "" && d.OnTaskCompleted != nil {
			d.OnTaskCompleted(ctx, taskID)
		}
	default:
		log.Debug("distiller ignoring event type", "type", event.Type)
	}
}

// formatImportanceScores formats importance scores for logging.
func formatImportanceScores(memories []Memory) string {
	if len(memories) == 0 {
		return "[]"
	}
	scores := make([]string, len(memories))
	for i, mem := range memories {
		scores[i] = fmt.Sprintf("%.2f", mem.Importance)
	}
	return "[" + strings.Join(scores, ", ") + "]"
}

// formatMemoryTypes formats memory types for logging.
func formatMemoryTypes(memories []Memory) string {
	if len(memories) == 0 {
		return "[]"
	}
	types := make([]string, len(memories))
	for i, mem := range memories {
		types[i] = string(mem.Type)
	}
	return fmt.Sprintf("%v", types)
}

// extractTurnID finds the TurnID of the user message that matches the given problem text.
// This is a lightweight lookup that avoids text matching on every message.
func extractTurnID(messages []Message, problem string) string {
	problemTrunc := truncpkg.Plain(problem, 50)
	for _, msg := range messages {
		if msg.Role == "user" && strings.Contains(msg.Content, problemTrunc) {
			return msg.TurnID
		}
	}
	return ""
}

// extractEvidenceFromMessages collects tool observation evidence from messages
// belonging to the given turn. Uses TurnID for precise structured association
// (not content text matching, which is fragile with truncated/duplicated text).
// Tool result content comes from cleaner-generated summaries, not raw regexp extraction.
func extractEvidenceFromMessages(messages []Message, turnID string) []string {
	if turnID == "" || len(messages) == 0 {
		return nil
	}

	var evidence []string
	for _, msg := range messages {
		if msg.TurnID != turnID {
			continue
		}
		switch msg.Role {
		case "tool_call":
			for _, tc := range msg.ToolCalls {
				if fn, ok := tc["function"].(map[string]interface{}); ok {
					if name, ok := fn["name"].(string); ok {
						id, _ := tc["id"].(string)
						if id != "" {
							evidence = append(evidence, fmt.Sprintf("Action %s: %s", id, name))
						} else {
							evidence = append(evidence, fmt.Sprintf("Action: %s", name))
						}
					}
				}
			}
		case "tool_result":
			if msg.Content != "" {
				// Content is already a cleaner-generated summary (from buildCleanedDistillationMessages),
				// not raw tool output. Truncate length only, no regexp extraction needed.
				if len(msg.Content) > 120 {
					evidence = append(evidence, fmt.Sprintf("Observed: %s...", msg.Content[:120]))
				} else {
					evidence = append(evidence, fmt.Sprintf("Observed: %s", msg.Content))
				}
			}
		}
	}
	if len(evidence) == 0 {
		return nil
	}
	return evidence
}

// syncToExperienceStore writes distilled memories to the experience store.
// It converts each memory to an experience using type mapping rules.
//
// Args:
//
//	ctx - operation context.
//	memories - the distilled memories to sync.
//	tenantID - tenant ID for multi-tenancy.
//
// Returns:
//
//	error - the first error encountered, or nil.
func (d *Distiller) syncToExperienceStore(ctx context.Context, memories []Memory, tenantID string) error {
	for _, mem := range memories {
		exp := d.convertMemoryToExperience(&mem, tenantID)
		if err := d.expStore.Create(ctx, exp); err != nil {
			return fmt.Errorf("sync memory %s to experience store: %w", mem.ID, err)
		}
		log.DebugContext(ctx, "[Memory Distillation] Synced memory to experience store",
			"memory_id", mem.ID,
			"experience_type", exp.Type)
	}
	return nil
}

// convertMemoryToExperience converts a Memory to a StoredExperience using type mapping rules.
//
// Mapping rules:
//
//	MemoryKnowledge   → TypeSolution
//	MemoryInteraction → TypeSolution
//	MemoryPreference  → TypeHeuristic
//	MemoryProfile     → TypeStrategy
//
// Args:
//
//	mem - the memory to convert.
//	tenantID - tenant ID for multi-tenancy.
//
// Returns:
//
//	*StoredExperience - the converted experience.
func (d *Distiller) convertMemoryToExperience(mem *Memory, tenantID string) *StoredExperience {
	problem, _ := mem.Metadata["problem"].(string)
	solution, _ := mem.Metadata["solution"].(string)

	return &StoredExperience{
		TenantID: tenantID,
		Type:     memoryTypeToExperienceType(mem.Type),
		Problem:  problem,
		Solution: solution,
		Score:    mem.Importance,
		Source:   "memory_distillation",
		Metadata: map[string]interface{}{
			"memory_id":   mem.ID,
			"memory_type": mem.Type.String(),
			"content":     mem.Content,
			"source":      mem.Source,
			"importance":  mem.Importance,
			"created_at":  mem.CreatedAt.Format(time.RFC3339),
		},
	}
}

// Experience type constants for the experience store.
const (
	TypeSolution  = "solution"
	TypeHeuristic = "heuristic"
	TypeStrategy  = "strategy"
	TypeFailure   = "failure"
	TypeGeneral   = "general"
)

// memoryTypeToExperienceType maps Memory types to Experience types.
// This bridges the memory distillation system with the experience system.
//
// Args:
//
//	mt - the memory type.
//
// Returns:
//
//	string - the corresponding experience type.
func memoryTypeToExperienceType(mt MemoryType) string {
	switch mt {
	case MemoryKnowledge:
		return TypeSolution
	case MemoryInteraction:
		return TypeSolution
	case MemoryPreference:
		return TypeHeuristic
	case MemoryProfile:
		return TypeStrategy
	default:
		return TypeGeneral
	}
}
