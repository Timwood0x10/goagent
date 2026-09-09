package ares_events

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Compactor is responsible for monitoring event streams and compacting
// old events into EventSummary records when streams exceed configured thresholds.
//
// The compaction pipeline:
//  1. Monitor stream size (via StreamVersion or count)
//  2. When threshold exceeded, identify candidate events (old ones outside keepRecent window)
//  3. Build an EventSummary from those events using rule-based aggregation
//  4. Persist summary via SummaryRepository
//  5. Optionally trim raw events from the store
type Compactor struct {
	store      EventStore
	repo       SummaryRepository
	config     CompactionConfig
	summarizer EventSummarizer
	trimStore  TrimAwareStore // Optional: trims events after compaction.
}

// EventSummarizer generates a human-readable summary text from a set of events.
// Implementations can be rule-based (default) or LLM-powered.
type EventSummarizer func(events []*Event) string

// NewCompactor creates a new Compactor with the given dependencies.
//
// Config sanitization: a non-positive Threshold OR a negative KeepRecent is
// replaced by the defaults. KeepRecent flows straight into slice arithmetic
// (candidates := events[:total-KeepRecent]); a negative value turns
// `total <= KeepRecent` false on an empty stream and slices past the end —
// panicking inside the store's errgroup worker, which kills the whole
// process, not just the request.
func NewCompactor(store EventStore, repo SummaryRepository, config CompactionConfig) *Compactor {
	if config.Threshold <= 0 || config.KeepRecent < 0 {
		config = DefaultCompactionConfig()
	}
	return &Compactor{
		store:      store,
		repo:       repo,
		config:     config,
		summarizer: DefaultSummarizer,
	}
}

// WithSummarizer sets a custom summarizer function (e.g., LLM-based).
func (c *Compactor) WithSummarizer(s EventSummarizer) *Compactor {
	c.summarizer = s
	return c
}

// WithTrimStore sets the trim store for deleting compacted events.
func (c *Compactor) WithTrimStore(ts TrimAwareStore) *Compactor {
	c.trimStore = ts
	return c
}

// CheckAndCompact examines a single stream and triggers compaction if needed.
// Returns true if compaction was performed, along with any error.
func (c *Compactor) CheckAndCompact(ctx context.Context, streamID string) (bool, error) {
	if c.store == nil {
		return false, errors.New("compactor: store is nil")
	}

	version, err := c.store.StreamVersion(ctx, streamID)
	if err != nil {
		return false, fmt.Errorf("check stream version: %w", err)
	}

	// No compaction needed if under threshold.
	if version <= int64(c.config.Threshold) {
		return false, nil
	}

	return c.compactStream(ctx, streamID)
}

// ForceCompact forces compaction on a stream regardless of the configured threshold.
// Useful for manual triggering or testing. Returns true if compaction was performed.
func (c *Compactor) ForceCompact(ctx context.Context, streamID string) (bool, error) {
	if c.store == nil {
		return false, errors.New("compactor: store is nil")
	}
	return c.compactStream(ctx, streamID)
}

// CompactAll scans all known streams and compacts those exceeding the threshold.
func (c *Compactor) CompactAll(ctx context.Context, knownStreamIDs []string) (int, error) {
	compacted := 0
	for _, streamID := range knownStreamIDs {
		didCompact, err := c.CheckAndCompact(ctx, streamID)
		if err != nil {
			log.Error("compaction: failed for stream", "stream_id", streamID, "error", err)
			continue
		}
		if didCompact {
			compacted++
		}
	}
	return compacted, nil
}

// compactStream performs the actual compaction for a single stream.
func (c *Compactor) compactStream(ctx context.Context, streamID string) (bool, error) {
	// Read all events in the stream to determine what to compact.
	allEvents, err := c.store.Read(ctx, streamID, ReadOptions{
		Direction: ReadAscending,
	})
	if err != nil {
		return false, fmt.Errorf("read stream for compaction: %w", err)
	}

	totalEvents := len(allEvents)
	if c.config.KeepRecent < 0 || totalEvents <= c.config.KeepRecent {
		return false, nil
	}

	// Candidate events are everything except the most recent KeepRecent events.
	candidateCount := totalEvents - c.config.KeepRecent
	candidates := allEvents[:candidateCount]

	if len(candidates) == 0 {
		return false, nil
	}

	// Build summary from candidate events.
	summary := c.buildSummary(streamID, candidates)

	// Persist summary.
	if err := c.repo.Save(ctx, summary); err != nil {
		return false, fmt.Errorf("save event summary: %w", err)
	}

	log.Info("compaction: summary created",
		"stream_id", streamID,
		"summary_id", summary.ID,
		"events_compacted", summary.EventCount,
		"version_range", fmt.Sprintf("%d-%d", summary.StartVersion, summary.EndVersion),
	)

	// Optionally trim compacted events from the live store.
	if c.config.EnableTrimming && c.trimStore != nil {
		if removed, err := c.trimStore.TrimBefore(ctx, streamID, summary.EndVersion); err != nil {
			log.Warn("compaction: failed to trim old events", "error", err)
		} else if removed > 0 {
			log.Info("compaction: trimmed events from live store",
				"stream_id", streamID,
				"removed", removed,
			)
		}
	}

	return true, nil
}

// buildSummary constructs an EventSummary from a slice of events using rule-based aggregation.
//
//nolint:gocyclo // Complex event aggregation logic with multiple event types
func (c *Compactor) buildSummary(streamID string, events []*Event) *EventSummary {
	if len(events) == 0 {
		return nil
	}

	summary := &EventSummary{
		ID:              NewEventSummaryID(),
		StreamID:        streamID,
		EventCount:      len(events),
		StartVersion:    events[0].Version,
		EndVersion:      events[len(events)-1].Version,
		StartTime:       events[0].Timestamp,
		EndTime:         events[len(events)-1].Timestamp,
		EventTypeCounts: make(map[string]int),
		TasksCreated:    make([]string, 0),
		ToolsCalled:     make([]string, 0),
		Errors:          make([]string, 0),
		CreatedAt:       time.Now(),
	}

	// Aggregate data from events.
	toolSet := make(map[string]bool)
	taskSet := make(map[string]bool)

	for _, evt := range events {
		// Count event types.
		eventTypeCounts := summary.EventTypeCounts
		eventTypeCounts[string(evt.Type)]++

		// Extract agent ID from metadata or payload. Track the FIRST
		// non-empty agent ID; if a later event carries a different one,
		// mark the summary as multi-agent rather than silently attributing
		// everything to the first agent.
		if aid := extractAgentID(evt); aid != "" {
			if summary.AgentID == "" {
				summary.AgentID = aid
			} else if summary.AgentID != aid && summary.AgentID != "multi" {
				summary.AgentID = "multi"
			}
		}

		// Extract task IDs from task.created events.
		if evt.Type == EventTaskCreated {
			if taskID, ok := evt.Payload["task_id"].(string); ok && taskID != "" {
				if !taskSet[taskID] {
					taskSet[taskID] = true
					summary.TasksCreated = append(summary.TasksCreated, taskID)
				}
				// Use first task ID as primary task.
				if summary.TaskID == "" {
					summary.TaskID = taskID
				}
			}
		}

		// Extract tool names from llm.call events.
		if evt.Type == EventLLMCall {
			if toolName, ok := evt.Payload["tool"].(string); ok && toolName != "" {
				if !toolSet[toolName] {
					toolSet[toolName] = true
					summary.ToolsCalled = append(summary.ToolsCalled, toolName)
				}
			}
			if toolName, ok := evt.Payload["function"].(string); ok && toolName != "" {
				if !toolSet[toolName] {
					toolSet[toolName] = true
					summary.ToolsCalled = append(summary.ToolsCalled, toolName)
				}
			}
		}

		// Extract tool names from tool.call.* events too (E-02).
		if evt.Type == EventToolCallStarted || evt.Type == EventToolCallCompleted {
			if toolName, ok := evt.Payload["tool"].(string); ok && toolName != "" {
				if !toolSet[toolName] {
					toolSet[toolName] = true
					summary.ToolsCalled = append(summary.ToolsCalled, toolName)
				}
			}
			if toolName, ok := evt.Payload["tool_name"].(string); ok && toolName != "" {
				if !toolSet[toolName] {
					toolSet[toolName] = true
					summary.ToolsCalled = append(summary.ToolsCalled, toolName)
				}
			}
		}

		// Extract session/user info from session.created events.
		if evt.Type == EventSessionCreated {
			if sid, ok := evt.Payload["session_id"].(string); ok {
				summary.SessionID = sid
			}
			if uid, ok := evt.Payload["user_id"].(string); ok {
				summary.UserID = uid
			}
			if input, ok := evt.Payload["input"].(string); ok && len(input) > 200 {
				summary.RequestSummary = input[:200] + "..."
			} else if input, ok := evt.Payload["input"].(string); ok {
				summary.RequestSummary = input
			}
		}

		// Extract user request from message.added events.
		if evt.Type == EventMessageAdded {
			if summary.RequestSummary == "" {
				if content, ok := evt.Payload["content"].(string); ok && content != "" {
					if len(content) > 200 {
						summary.RequestSummary = content[:200] + "..."
					} else {
						summary.RequestSummary = content
					}
				}
			}
		}

		// Collect errors from task.failed and failover events.
		if evt.Type == EventTaskFailed || evt.Type == EventFailoverTriggered {
			if errMsg, ok := evt.Payload["error"].(string); ok && errMsg != "" {
				if len(summary.Errors) < 10 { // Cap error collection.
					summary.Errors = append(summary.Errors, errMsg)
				}
			}
		}
	}

	if summary.AgentID == "" {
		summary.AgentID = "unknown"
	}

	// Determine outcome.
	switch {
	case summary.EventTypeCounts[string(EventTaskFailed)] > 0 || summary.EventTypeCounts[string(EventFailoverTriggered)] > 0:
		if summary.EventTypeCounts[string(EventTaskCompleted)] > 0 {
			summary.Outcome = "partial"
		} else {
			summary.Outcome = "failed"
		}
	case summary.EventTypeCounts[string(EventTaskCompleted)] > 0:
		summary.Outcome = "completed"
	default:
		summary.Outcome = "active"
	}

	// Generate human-readable summary text using the configured summarizer.
	summary.SummaryText = c.summarizer(events)

	return summary
}

// collectTool extracts a tool name from payload under the given key and appends it
// to the tools slice if not already present in the seen set.
func collectTool(payload map[string]any, key string, seen map[string]bool, tools *[]string) {
	if t, ok := payload[key].(string); ok && t != "" {
		if !seen[t] {
			seen[t] = true
			*tools = append(*tools, t)
		}
	}
}

// extractAgentID pulls the agent_id from an event's metadata or payload.
// Returns "" when neither carries a non-empty value.
func extractAgentID(evt *Event) string {
	if aid, ok := evt.Metadata["agent_id"].(string); ok && aid != "" {
		return aid
	}
	if aid, ok := evt.Payload["agent_id"].(string); ok && aid != "" {
		return aid
	}
	return ""
}

// DefaultSummarizer is a rule-based summarizer that produces a concise English
// summary of an event sequence without requiring an LLM call.
//
// Output format:
// "Agent {id} ran {n} tasks [{task_ids}], called {m} tools [{tools}],
// emitted {k} events over {duration}, bound to user request '{snippet}', result: {outcome}"
//
//nolint:gocyclo // Complex event summarization with multiple formats
//nolint:gocyclo
func DefaultSummarizer(events []*Event) string {
	if len(events) == 0 {
		return "(empty event window)"
	}

	var (
		streamID   string
		taskIDs    []string
		tools      []string
		typeCounts map[string]int
		startTime  time.Time
		endTime    time.Time
		outcome    string
		request    string
		errMsgs    []string
	)

	typeCounts = make(map[string]int)
	toolSet := make(map[string]bool)
	taskSet := make(map[string]bool)

	for _, evt := range events {
		streamID = evt.StreamID

		if startTime.IsZero() || evt.Timestamp.Before(startTime) {
			startTime = evt.Timestamp
		}
		if evt.Timestamp.After(endTime) {
			endTime = evt.Timestamp
		}

		typeCounts[string(evt.Type)]++

		if evt.Type == EventTaskCreated {
			if tid, _ := evt.Payload["task_id"].(string); tid != "" {
				if !taskSet[tid] {
					taskSet[tid] = true
					taskIDs = append(taskIDs, tid)
				}
			}
		}

		if evt.Type == EventLLMCall {
			collectTool(evt.Payload, "tool", toolSet, &tools)
			collectTool(evt.Payload, "function", toolSet, &tools)
			collectTool(evt.Payload, "tool_name", toolSet, &tools)
		}

		if evt.Type == EventSessionCreated {
			if input, _ := evt.Payload["input"].(string); input != "" {
				request = input
			}
		}

		if evt.Type == EventMessageAdded && request == "" {
			if content, _ := evt.Payload["content"].(string); content != "" {
				request = content
			}
		}

		if evt.Type == EventTaskFailed || evt.Type == EventFailoverTriggered {
			if e, _ := evt.Payload["error"].(string); e != "" && len(errMsgs) < 3 {
				errMsgs = append(errMsgs, e)
			}
		}
	}

	// Determine outcome.
	hasFailed := typeCounts[string(EventTaskFailed)] > 0 || typeCounts[string(EventFailoverTriggered)] > 0
	hasCompleted := typeCounts[string(EventTaskCompleted)] > 0
	switch {
	case hasFailed && hasCompleted:
		outcome = "partial"
	case hasFailed:
		outcome = "failed"
	case hasCompleted:
		outcome = "completed"
	default:
		outcome = "active"
	}

	// Sort for deterministic output.
	sort.Strings(taskIDs)
	sort.Strings(tools)

	duration := endTime.Sub(startTime).Round(time.Second)
	eventCount := len(events)

	parts := make([]string, 0, 12)
	parts = append(parts, fmt.Sprintf("Agent %s", streamID))

	if len(taskIDs) > 0 {
		taskList := strings.Join(taskIDs, ", ")
		if len(taskIDs) > 3 {
			taskList = strings.Join(taskIDs[:3], ", ") + fmt.Sprintf(" (+%d more)", len(taskIDs)-3)
		}
		parts = append(parts, fmt.Sprintf("ran %d task(s) [%s]", len(taskIDs), taskList))
	} else {
		parts = append(parts, fmt.Sprintf("processed %d events", eventCount))
	}

	if len(tools) > 0 {
		toolList := strings.Join(tools, ", ")
		if len(tools) > 5 {
			toolList = strings.Join(tools[:5], ", ") + fmt.Sprintf(" (+%d more)", len(tools)-5)
		}
		parts = append(parts, fmt.Sprintf("called %d tool(s) [%s]", len(tools), toolList))
	}

	parts = append(parts, fmt.Sprintf("emitted %d events", eventCount))
	parts = append(parts, fmt.Sprintf("duration %s", duration))

	if request != "" {
		snippet := request
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		parts = append(parts, fmt.Sprintf("bound to user request: %q", snippet))
	}

	parts = append(parts, fmt.Sprintf("result: %s", outcome))

	if len(errMsgs) > 0 {
		errList := strings.Join(errMsgs, "; ")
		parts = append(parts, fmt.Sprintf("errors: [%s]", errList))
	}

	return strings.Join(parts, ", ")
}

// CleanupOldSummaries removes expired summaries based on the configured TTL.
func (c *Compactor) CleanupOldSummaries(ctx context.Context) (int64, error) {
	threshold := time.Now().Add(-c.config.SummaryTTL)
	return c.repo.DeleteOlderThan(ctx, threshold)
}
