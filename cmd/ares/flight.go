package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Timwood0x10/ares/internal/ares_events"
	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

var flightCmd = &cobra.Command{
	Use:   "flight",
	Short: "Agent Flight Recorder commands",
	Long: `Inspect and replay agent flight data from recorded events.
Supports text, mermaid, dot, and JSON output formats.`,
}

var (
	flightInspectFormat string
	flightInspectInput  string
)

var flightInspectCmd = &cobra.Command{
	Use:   "inspect <taskID>",
	Short: "Show flight data for a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		evts, err := loadFlightEvents(flightInspectInput)
		if err != nil {
			return fmt.Errorf("load events: %w", err)
		}
		if len(evts) == 0 {
			return errors.New("no events found")
		}

		var taskEvts []*ares_events.Event
		for _, e := range evts {
			if e.StreamID == taskID {
				taskEvts = append(taskEvts, e)
			}
		}
		if len(taskEvts) == 0 {
			return fmt.Errorf("no events found for task %s", taskID)
		}

		switch flightInspectFormat {
		case "text":
			return inspectText(taskID, taskEvts)
		case "mermaid":
			return inspectMermaid(taskEvts)
		case "dot":
			return inspectDOT(taskEvts)
		case "json":
			return inspectJSON(taskEvts)
		default:
			return fmt.Errorf("unknown format: %s (supported: text, mermaid, dot, json)", flightInspectFormat)
		}
	},
}

var (
	flightReplayStep  int
	flightReplayInput string
)

var flightReplayCmd = &cobra.Command{
	Use:   "replay <taskID>",
	Short: "Step-by-step replay of a task",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]

		evts, err := loadFlightEvents(flightReplayInput)
		if err != nil {
			return fmt.Errorf("load events: %w", err)
		}

		store := ares_events.NewMemoryEventStore()
		defer func() { _ = store.Close() }()

		ctx := context.Background()

		streamEvents := make(map[string][]*ares_events.Event)
		for _, e := range evts {
			streamEvents[e.StreamID] = append(streamEvents[e.StreamID], e)
		}
		for streamID, sevts := range streamEvents {
			if err := store.Append(ctx, streamID, sevts, 0); err != nil {
				return fmt.Errorf("append events for stream %s: %w", streamID, err)
			}
		}

		session, err := flight.NewReplaySession(ctx, store, taskID)
		if err != nil {
			return fmt.Errorf("create replay session: %w", err)
		}

		summary := session.Summary()
		fmt.Printf("Task: %s\n", summary.TaskID)
		fmt.Printf("Total steps: %d\n", summary.TotalSteps)
		fmt.Printf("Duration: %s\n", summary.Duration)
		fmt.Printf("Agents: %s\n", strings.Join(summary.Agents, ", "))
		fmt.Println("---")

		if flightReplayStep >= 0 {
			rs, err := session.StepTo(flightReplayStep)
			if err != nil {
				return fmt.Errorf("step to %d: %w", flightReplayStep, err)
			}
			printReplayStep(rs)
		} else {
			for {
				rs, err := session.Step()
				if err != nil {
					break
				}
				printReplayStep(rs)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(flightCmd)

	flightCmd.AddCommand(flightInspectCmd)
	flightInspectCmd.Flags().StringVarP(&flightInspectFormat, "format", "f", "text", "Output format: text, mermaid, dot, json")
	flightInspectCmd.Flags().StringVarP(&flightInspectInput, "input", "i", "", "Path to JSON events file (default: stdin)")

	flightCmd.AddCommand(flightReplayCmd)
	flightReplayCmd.Flags().IntVarP(&flightReplayStep, "step", "s", -1, "Jump to a specific step (0-indexed)")
	flightReplayCmd.Flags().StringVarP(&flightReplayInput, "input", "i", "", "Path to JSON events file (default: stdin)")
}

// ── Shared helpers ──────────────────────────────────────────

func loadFlightEvents(path string) ([]*ares_events.Event, error) {
	var reader io.Reader
	if path == "" {
		reader = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		reader = f
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("input is empty")
	}

	var evts []*ares_events.Event
	if err := json.Unmarshal(data, &evts); err != nil {
		return nil, fmt.Errorf("parse JSON events: %w", err)
	}
	return evts, nil
}

func inspectText(taskID string, evts []*ares_events.Event) error {
	tl := flight.NewTimeline()
	dl := flight.NewDecisionLog()
	de := flight.NewDiagnosticsEngine()

	for _, e := range evts {
		te := flight.TimelineEvent{
			ID:       e.ID,
			AgentID:  e.StreamID,
			Type:     mapFlightEventType(e.Type),
			Name:     eventName(e),
			StartAt:  e.Timestamp,
			Metadata: e.Payload,
		}
		tl.Add(te)

		if e.Type == "decision" {
			d := flight.Decision{
				ID:        e.ID,
				AgentID:   e.StreamID,
				Type:      flight.DecisionType(stringOr(e.Payload, "type", "unknown")),
				Selected:  stringOr(e.Payload, "selected", ""),
				Reason:    stringOr(e.Payload, "reason", ""),
				Timestamp: e.Timestamp,
				Metadata:  e.Payload,
			}
			dl.Add(d)
		}

		if e.Type == "error" {
			errMsg := stringOr(e.Payload, "error", "unknown error")
			cat := flight.ClassifyError(errMsg)
			suggestions := flight.SuggestFix(cat)
			suggestion := ""
			if len(suggestions) > 0 {
				suggestion = suggestions[0]
			}
			dr := flight.DiagnosticRecord{
				ID:         e.ID,
				AgentID:    e.StreamID,
				TaskID:     taskID,
				Category:   cat,
				RootCause:  errMsg,
				Suggestion: suggestion,
				Timestamp:  e.Timestamp,
			}
			de.Record(dr)
		}
	}

	summary := tl.Summary()
	fmt.Printf("=== Flight Inspector: %s ===\n\n", taskID)
	fmt.Printf("Timeline Summary:\n")
	fmt.Printf("  Events:        %d\n", summary.EventCount)
	fmt.Printf("  Total Duration: %s\n", formatDuration(summary.TotalDuration))
	fmt.Printf("  Tool Duration:  %s (%.1f%%)\n", formatDuration(summary.ToolDuration), summary.ToolPercent)
	fmt.Printf("  LLM Duration:   %s (%.1f%%)\n", formatDuration(summary.LLMDuration), summary.LLMPercent)
	fmt.Printf("  Wait Duration:  %s (%.1f%%)\n", formatDuration(summary.WaitDuration), summary.WaitPercent)
	fmt.Println()

	decisions := dl.All()
	if len(decisions) > 0 {
		fmt.Printf("Decisions (%d):\n", len(decisions))
		for _, d := range decisions {
			fmt.Printf("  [%s] agent=%s selected=%q reason=%q confidence=%.2f\n",
				d.Type, d.AgentID, d.Selected, d.Reason, d.Confidence)
		}
		fmt.Println()
	}

	records := de.All()
	if len(records) > 0 {
		fmt.Printf("Diagnostics (%d):\n", len(records))
		for _, r := range records {
			fmt.Printf("  [%s] agent=%s cause=%q fix=%q\n",
				r.Category, r.AgentID, r.RootCause, r.Suggestion)
		}
		fmt.Println()
	}

	fmt.Printf("Events:\n")
	for i, e := range evts {
		fmt.Printf("  %d. [%s] %s (%s) @ %s\n",
			i, e.Type, eventName(e), e.StreamID, e.Timestamp.Format(time.RFC3339))
	}

	return nil
}

func inspectMermaid(evts []*ares_events.Event) error {
	g := buildGraph(evts)
	fmt.Println(g.ExportMermaid())
	return nil
}

func inspectDOT(evts []*ares_events.Event) error {
	g := buildGraph(evts)
	fmt.Println(g.ExportDOT())
	return nil
}

func inspectJSON(evts []*ares_events.Event) error {
	data, err := json.MarshalIndent(evts, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func buildGraph(evts []*ares_events.Event) *flight.Graph {
	g := flight.NewGraph()
	hasRoot := false

	for _, e := range evts {
		nodeID := e.ID
		if nodeID == "" {
			nodeID = fmt.Sprintf("evt-%d", e.Version)
		}

		nodeType := flight.NodeAgent
		switch e.Type {
		case "tool.call", "tool.result":
			nodeType = flight.NodeTool
		case "llm.call", "llm.result":
			nodeType = flight.NodeLLM
		}

		status := flight.StatusCompleted
		if e.Type == "error" {
			status = flight.StatusFailed
		}

		parentID := stringOr(e.Payload, "parent_id", "")

		if parentID == "" && !hasRoot {
			hasRoot = true
		} else if parentID == "" {
			// Root node's ID may be synthesized ("evt-<version>"); use the same
			// synthesis for the parent reference so it never dangles.
			parentID = evts[0].ID
			if parentID == "" {
				parentID = fmt.Sprintf("evt-%d", evts[0].Version)
			}
		}

		node := &flight.GraphNode{
			ID:       nodeID,
			ParentID: parentID,
			Type:     nodeType,
			Name:     eventName(e),
			Status:   status,
			StartAt:  e.Timestamp,
			Metadata: e.Payload,
		}
		g.AddNode(node)
	}
	return g
}

func mapFlightEventType(t ares_events.EventType) flight.EventType {
	switch t {
	case "agent.started":
		return flight.EventAgentStart
	case "agent.stopped":
		return flight.EventAgentEnd
	case "tool.call":
		return flight.EventToolCall
	case "tool.result":
		return flight.EventToolResult
	case "llm.call":
		return flight.EventLLMCall
	case "llm.result":
		return flight.EventLLMResult
	case "error":
		return flight.EventError
	case "memory.distilled", "memory.op":
		return flight.EventMemoryOp
	case "decision":
		return flight.EventDecision
	default:
		return flight.EventType(t)
	}
}

func eventName(e *ares_events.Event) string {
	if name, ok := e.Payload["name"].(string); ok && name != "" {
		return name
	}
	if tool, ok := e.Payload["tool"].(string); ok && tool != "" {
		return tool
	}
	if model, ok := e.Payload["model"].(string); ok && model != "" {
		return model
	}
	return string(e.Type)
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fus", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d.Milliseconds()))
	}
	return d.Truncate(time.Millisecond).String()
}

func printReplayStep(step *flight.ReplayStep) {
	fmt.Printf("Step %d: [%s] agent=%s @ %s\n",
		step.StepNum, step.EventType, step.AgentID,
		step.Timestamp.Format(time.RFC3339))
	if len(step.Payload) > 0 {
		data, err := json.MarshalIndent(step.Payload, "  ", "  ")
		if err == nil {
			fmt.Printf("  payload: %s\n", string(data))
		}
	}
}
