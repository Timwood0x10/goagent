// Package eval provides Evidence — structured evaluation results with verifiable chains.
//
// Design principle (from AI Agents in Depth Ch.8):
// "Verification results should not be compressed into a single scalar score.
// A trajectory evaluation is more like a structured diagnosis: which dimensions
// passed, which failed, with what evidence, and at what confidence."
package eval

import (
	"fmt"
	"time"
)

// Verdict represents the overall assessment of an agent execution.
type Verdict int

const (
	VerdictPass      Verdict = iota // Task completed successfully
	VerdictFail                     // Task failed or produced incorrect results
	VerdictUncertain                // Cannot determine — needs human review
)

func (v Verdict) String() string {
	switch v {
	case VerdictPass:
		return "pass"
	case VerdictFail:
		return "fail"
	default:
		return "uncertain"
	}
}

// EvidenceItem is a single piece of verifiable evidence.
// Each item has a source type that determines how it was obtained.
type EvidenceItem struct {
	// Type categorizes the evidence source.
	// "test"     — unit test / integration test result
	// "tool_call" — tool invocation and its return value
	// "db_state"  — database state change verification
	// "file"      — file existence / content check
	// "llm_quote" — LLM-verified claim with citation
	// "human"     — human review / approval
	Type string

	// Name identifies the specific evidence (test name, tool name, etc.)
	Name string

	// Status is the outcome: "passed" / "failed" / "missing" / "skipped"
	Status string

	// Detail contains the actual data (test output, tool response, file content, etc.)
	Detail string

	// Confirmed indicates whether this evidence has been independently verified.
	Confirmed bool
}

func (e *EvidenceItem) String() string {
	return fmt.Sprintf("[%s] %s: %s — %s", e.Type, e.Name, e.Status, e.Detail)
}

// DimensionScore represents the evaluation of one aspect of the execution.
type DimensionScore struct {
	// Name is the dimension being evaluated.
	// Examples: "task_result", "rule_compliance", "privacy_boundary",
	//           "fact_reliability", "promise_action_consistency"
	Name string

	// Score and Max define the rating scale.
	Score int
	Max   int

	// Pass indicates whether this dimension meets the threshold.
	Pass bool

	// Evidence contains the verifiable items supporting this score.
	Evidence []EvidenceItem

	// Flag describes what went wrong, if anything.
	// Empty when Pass is true.
	Flag string
}

// Evidence is the top-level structured evaluation result.
// It replaces scalar scores with a diagnosis that preserves:
// - Which dimensions passed/failed
// - What evidence supports each judgment
// - Overall confidence level
type Evidence struct {
	// TaskID identifies the task being evaluated.
	TaskID string

	// Role is the agent role whose execution is being assessed.
	Role string

	// Verdict is the overall pass/fail determination.
	Verdict Verdict

	// Flag summarizes the overall failure reason, if any.
	// Empty when the verdict is not a failure.
	Flag string

	// Dimensions contains per-aspect scores with evidence.
	Dimensions []DimensionScore

	// Confidence is the overall confidence in this evaluation (0.0–1.0).
	Confidence float64

	// Source indicates which verifier produced this evidence.
	// "result_verifier" / "process_verifier" / "rubric_judge"
	Source string

	// CreatedAt records when the evaluation was performed.
	CreatedAt time.Time

	// Meta carries additional diagnostic information.
	Meta map[string]any
}

// NewEvidence creates an empty Evidence structure.
func NewEvidence(taskID, role, source string) *Evidence {
	return &Evidence{
		TaskID:    taskID,
		Role:      role,
		Source:    source,
		CreatedAt: time.Now(),
		Meta:      make(map[string]any),
	}
}

// AddDimension adds a scored dimension to the evidence.
// A failing dimension without an explicit flag gets an auto-generated flag
// so that FailureFlags always reports every failing dimension, and the
// overall Flag records the first failing dimension.
func (e *Evidence) AddDimension(name string, score, max int, evidence []EvidenceItem, flag string) {
	pass := false
	if max > 0 {
		// Float threshold (#45): integer division (max*2/3) truncates and
		// disagrees with the float formula used elsewhere (e.g. max=2 →
		// int 1 vs float 1.33). One formula everywhere.
		pass = float64(score) >= float64(max)*2/3
	} else if flag == "" {
		flag = fmt.Sprintf("%s has invalid max score (%d)", name, max)
	}
	if !pass && flag == "" {
		flag = fmt.Sprintf("%s below threshold (%d/%d)", name, score, max)
	}
	e.Dimensions = append(e.Dimensions, DimensionScore{
		Name:     name,
		Score:    score,
		Max:      max,
		Pass:     pass,
		Evidence: evidence,
		Flag:     flag,
	})
	if !pass && e.Flag == "" {
		e.Flag = flag
	}
}

// HasFailure returns true if any dimension failed.
func (e *Evidence) HasFailure() bool {
	for _, d := range e.Dimensions {
		if !d.Pass {
			return true
		}
	}
	return false
}

// FailureFlags returns all non-passing dimension flags.
func (e *Evidence) FailureFlags() []string {
	var flags []string
	for _, d := range e.Dimensions {
		if !d.Pass && d.Flag != "" {
			flags = append(flags, d.Flag)
		}
	}
	return flags
}

// String returns a human-readable summary.
func (e *Evidence) String() string {
	status := e.Verdict.String()
	if e.HasFailure() {
		status += fmt.Sprintf("(%d failures)", len(e.FailureFlags()))
	}
	return fmt.Sprintf("Evidence{%s %s conf=%.2f dims=%d}", e.Role, status, e.Confidence, len(e.Dimensions))
}
