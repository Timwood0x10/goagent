package archive

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Potential bug scenarios tested below:
//  1. EventToolCallCompleted with no "output" key in payload — extractToolOutput
//     returns "" and extractVerdict leaves all fields empty (no panic).
//     Covered by TestExtractVerdict_NoOutputKey.
//  2. "PASS" substring in a file_tools output path (e.g. "/tmp/PASS_test.go")
//     must NOT set GoTest=pass — the verdict is only set when the tool name
//     contains "test". Covered by TestExtractVerdict_NoFalsePositive.
//  3. "exit code: 00" (zero-padded) must parse as exit code 0 (pass) —
//     strconv.Atoi("00") returns 0. Covered by TestExtractVerdict_ExitCodeParsing.

// --- Test helpers ---

func newToolEvent(toolName, output string) *ares_events.Event {
	return &ares_events.Event{
		Type: ares_events.EventToolCallCompleted,
		Payload: map[string]any{
			"tool_name": toolName,
			"output":    output,
		},
	}
}

func newTaskEvent(task, result string) *ares_events.Event {
	payload := map[string]any{
		ares_events.EventKeyTask: task,
	}
	if result != "" {
		payload[ares_events.EventKeyResult] = result
	}
	return &ares_events.Event{
		Type:    ares_events.EventTaskCompleted,
		Payload: payload,
	}
}

func newMessageEvent(content string) *ares_events.Event {
	return &ares_events.Event{
		Type: ares_events.EventMessageAdded,
		Payload: map[string]any{
			keyContent: content,
		},
	}
}

// --- extractVerdict tests ---

func TestExtractVerdict(t *testing.T) {
	tests := []struct {
		name   string
		events []*ares_events.Event
		want   Verdict
	}{
		{
			name: "go vet pass via code_runner exit code 0",
			events: []*ares_events.Event{
				newToolEvent(toolCodeRunner, "exit code: 0"),
			},
			want: Verdict{GoVet: verdictPass},
		},
		{
			name: "go vet fail via code_runner exit code 1",
			events: []*ares_events.Event{
				newToolEvent(toolCodeRunner, "exit code: 1"),
			},
			want: Verdict{GoVet: verdictFail},
		},
		{
			name: "go test FAIL",
			events: []*ares_events.Event{
				newToolEvent("go_test", "FAIL\nexit code: 1"),
			},
			want: Verdict{GoTest: verdictFail},
		},
		{
			name: "go test pass",
			events: []*ares_events.Event{
				newToolEvent("go_test", "PASS\nok\tpkg\t0.5s"),
			},
			want: Verdict{GoTest: verdictPass},
		},
		{
			name: "race DATA RACE triggers race fail and test fail",
			events: []*ares_events.Event{
				newToolEvent("go_test_race", "FAIL\nDATA RACE\nexit code: 1"),
			},
			want: Verdict{GoTest: verdictFail, RaceDetector: verdictFail},
		},
		{
			name: "golangci 3 issues",
			events: []*ares_events.Event{
				newToolEvent("golangci_lint", "3 issues found"),
			},
			want: Verdict{GoLint: "3 issues"},
		},
		{
			name: "golangci 0 issues is pass",
			events: []*ares_events.Event{
				newToolEvent("golangci_lint", "0 issues"),
			},
			want: Verdict{GoLint: verdictPass},
		},
		{
			name: "golangci no issues count is pass",
			events: []*ares_events.Event{
				newToolEvent("golangci_lint", "all good, no problems"),
			},
			want: Verdict{GoLint: verdictPass},
		},
		{
			name: "go test skip via no test files",
			events: []*ares_events.Event{
				newToolEvent("go_test", "no test files"),
			},
			want: Verdict{GoTest: verdictSkip},
		},
		{
			name:   "no tool events yields empty verdict",
			events: []*ares_events.Event{newTaskEvent("do work", "done")},
			want:   Verdict{},
		},
		{
			name: "nil events yield empty verdict",
			want: Verdict{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVerdict(tt.events)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractVerdict_ExitCodeParsing(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantVet string
	}{
		{"lowercase exit code 0", "exit code: 0", verdictPass},
		{"capital exit code 1", "Exit code: 1", verdictFail},
		{"zero-padded exit code 00", "exit code: 00", verdictPass},
		{"exit code 2", "exit code: 2", verdictFail},
		{"no exit code line", "some output", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := extractVerdict([]*ares_events.Event{
				newToolEvent(toolCodeRunner, tt.output),
			})
			assert.Equal(t, tt.wantVet, v.GoVet)
		})
	}
}

func TestExtractVerdict_NoFalsePositive(t *testing.T) {
	// Bug scenario 2: a file_tools result containing "PASS" in a path must
	// NOT set GoTest=pass. Only the tool name determines which verdict field
	// is set.
	events := []*ares_events.Event{
		newToolEvent(toolFileTools, "Wrote /tmp/PASS_test.go (100 bytes)"),
	}
	v := extractVerdict(events)
	assert.Empty(t, v.GoTest, "GoTest must not be set by a file_tools event")
	assert.Empty(t, v.GoVet, "GoVet must not be set by a file_tools event")
	assert.Empty(t, v.GoLint, "GoLint must not be set by a file_tools event")
}

func TestExtractVerdict_NoOutputKey(t *testing.T) {
	// Bug scenario 1: EventToolCallCompleted with no "output" key must not
	// panic and must yield an empty verdict.
	events := []*ares_events.Event{
		{
			Type: ares_events.EventToolCallCompleted,
			Payload: map[string]any{
				"tool_name": toolCodeRunner,
			},
		},
	}
	v := extractVerdict(events)
	assert.Equal(t, Verdict{}, v, "no output key → empty verdict, no panic")
}

func TestExtractVerdict_NilPayload(t *testing.T) {
	events := []*ares_events.Event{
		{Type: ares_events.EventToolCallCompleted},
	}
	assert.NotPanics(t, func() {
		v := extractVerdict(events)
		assert.Equal(t, Verdict{}, v)
	})
}

// --- extractFileChanges tests ---

func TestExtractFileChanges(t *testing.T) {
	t.Run("file_tools write with args map", func(t *testing.T) {
		events := []*ares_events.Event{
			{
				Type: ares_events.EventToolCallCompleted,
				Payload: map[string]any{
					"tool_name": toolFileTools,
					"args": map[string]any{
						"path":      "internal/foo.go",
						"operation": opWrite,
					},
					"output": "Wrote internal/foo.go (50 bytes)",
				},
			},
		}
		changes := extractFileChanges(events)
		require.Len(t, changes, 1)
		assert.Equal(t, "internal/foo.go", changes[0].Path)
		assert.Contains(t, changes[0].Summary, "Wrote")
	})

	t.Run("file_tools with input JSON", func(t *testing.T) {
		events := []*ares_events.Event{
			{
				Type: ares_events.EventToolCallCompleted,
				Payload: map[string]any{
					"tool_name": toolFileTools,
					"input":     `{"path": "main.go", "operation": "read"}`,
					"output":    "Read main.go",
				},
			},
		}
		changes := extractFileChanges(events)
		require.Len(t, changes, 1)
		assert.Equal(t, "main.go", changes[0].Path)
	})

	t.Run("diff stat parsed for lines added", func(t *testing.T) {
		events := []*ares_events.Event{
			{
				Type: ares_events.EventToolCallCompleted,
				Payload: map[string]any{
					"tool_name": toolFileTools,
					"args": map[string]any{
						"path":      "diff.go",
						"operation": opWrite,
					},
					"output": "+10 -3\nsome context",
				},
			},
		}
		changes := extractFileChanges(events)
		require.Len(t, changes, 1)
		assert.Equal(t, 10, changes[0].LinesAdded)
	})

	t.Run("non-file tools skipped", func(t *testing.T) {
		events := []*ares_events.Event{
			newToolEvent(toolCodeRunner, "exit code: 0"),
		}
		changes := extractFileChanges(events)
		assert.Nil(t, changes)
	})

	t.Run("no path skipped", func(t *testing.T) {
		events := []*ares_events.Event{
			{
				Type: ares_events.EventToolCallCompleted,
				Payload: map[string]any{
					"tool_name": toolFileTools,
					"output":    "no path here",
				},
			},
		}
		changes := extractFileChanges(events)
		assert.Nil(t, changes)
	})

	t.Run("nil events returns nil", func(t *testing.T) {
		changes := extractFileChanges(nil)
		assert.Nil(t, changes)
	})
}

// --- extractDecisions tests ---

func TestExtractDecisions(t *testing.T) {
	events := []*ares_events.Event{
		newMessageEvent("We decided to use PostgreSQL for persistence."),
		newMessageEvent("I will use the repository pattern for data access."),
		newMessageEvent("The weather is nice today."),
		{
			Type: ares_events.EventLLMCall,
			Payload: map[string]any{
				keyContent: "The architecture adopts a layered design.",
			},
		},
	}
	decisions := extractDecisions(events)
	require.Len(t, decisions, 3, "three decision sentences matched")
	assert.Contains(t, decisions[0], "decided")
	assert.Contains(t, decisions[1], "will use")
	assert.Contains(t, decisions[2], "architecture")
}

func TestExtractDecisions_Capped(t *testing.T) {
	// Generate 15 decision lines; only 10 should be returned.
	msg := ""
	for i := 0; i < 15; i++ {
		msg += "We decided on option " + string(rune('A'+i)) + ".\n"
	}
	events := []*ares_events.Event{newMessageEvent(msg)}
	decisions := extractDecisions(events)
	assert.Len(t, decisions, 10, "decisions capped at 10")
}

// --- extractSummary tests ---

func TestExtractSummary(t *testing.T) {
	t.Run("task and verdict combined", func(t *testing.T) {
		events := []*ares_events.Event{
			newTaskEvent("Implement feature X", ""),
			newToolEvent(toolCodeRunner, "exit code: 0"),
		}
		summary := extractSummary(events, extractVerdict(events))
		assert.Contains(t, summary, "Implement feature X")
		assert.Contains(t, summary, "vet=pass")
	})

	t.Run("no task yields verdict only", func(t *testing.T) {
		events := []*ares_events.Event{
			newToolEvent("go_test", "PASS"),
		}
		summary := extractSummary(events, extractVerdict(events))
		assert.Contains(t, summary, "test=pass")
	})

	t.Run("capped at 160 chars", func(t *testing.T) {
		longTask := string(make([]byte, 300))
		for i := range longTask {
			longTask = longTask[:i] + "x" + longTask[i+1:]
		}
		events := []*ares_events.Event{
			newTaskEvent(longTask, ""),
		}
		summary := extractSummary(events, extractVerdict(events))
		assert.LessOrEqual(t, len(summary), 160)
	})
}

// --- extractTODOs tests ---

func TestExtractTODOs(t *testing.T) {
	events := []*ares_events.Event{
		newToolEvent(toolCodeRunner, "TODO: refactor this\nnormal line\nFIXME: handle error"),
		newMessageEvent("We should roll back the change if it fails."),
	}
	todos := extractTODOs(events)
	require.Len(t, todos, 3)
	assert.Contains(t, todos[0], "TODO")
	assert.Contains(t, todos[1], "FIXME")
	assert.Contains(t, todos[2], "roll back")
}

// --- BuildRoundRecord tests ---

func TestBuildRoundRecord_Full(t *testing.T) {
	events := []*ares_events.Event{
		newTaskEvent("Implement feature X", "done"),
		newToolEvent(toolCodeRunner, "exit code: 0"),
		newToolEvent("go_test", "PASS\nok\tpkg\t0.5s"),
		{
			Type: ares_events.EventToolCallCompleted,
			Payload: map[string]any{
				"tool_name": toolFileTools,
				"args": map[string]any{
					"path":      "internal/foo.go",
					"operation": opWrite,
				},
				"output": "Wrote internal/foo.go (50 bytes)",
			},
		},
		newMessageEvent("We decided to use a layered architecture."),
		newToolEvent(toolCodeRunner, "TODO: add tests later"),
	}

	refs := map[string]string{
		roleCommit: "abc1234",
		rolePR:     "#142",
	}

	record, err := BuildRoundRecord(context.Background(), 5, actionImplement, events, refs)
	require.NoError(t, err)
	require.NotNil(t, record)

	assert.Equal(t, 5, record.Round)
	assert.Equal(t, actionImplement, record.Action)
	assert.NotEmpty(t, record.Summary)
	assert.Equal(t, verdictPass, record.Verdict.GoVet)
	assert.Equal(t, verdictPass, record.Verdict.GoTest)
	require.Len(t, record.Files, 1)
	assert.Equal(t, "internal/foo.go", record.Files[0].Path)
	require.Len(t, record.Decisions, 1)
	assert.Contains(t, record.Decisions[0], "architecture")
	require.Len(t, record.TODOs, 1)
	assert.Contains(t, record.TODOs[0], "TODO")
	assert.Equal(t, "abc1234", record.Refs[roleCommit])
	assert.Equal(t, "#142", record.Refs[rolePR])
}

func TestBuildRoundRecord_InvalidInputs(t *testing.T) {
	validEvents := []*ares_events.Event{newTaskEvent("work", "done")}

	tests := []struct {
		name      string
		round     int
		action    string
		events    []*ares_events.Event
		wantErrIs error
	}{
		{
			name:      "round zero",
			round:     0,
			action:    actionImplement,
			events:    validEvents,
			wantErrIs: ErrInvalidRound,
		},
		{
			name:      "negative round",
			round:     -1,
			action:    actionImplement,
			events:    validEvents,
			wantErrIs: ErrInvalidRound,
		},
		{
			name:      "invalid action",
			round:     1,
			action:    "bogus",
			events:    validEvents,
			wantErrIs: ErrInvalidAction,
		},
		{
			name:      "empty action",
			round:     1,
			action:    "",
			events:    validEvents,
			wantErrIs: ErrInvalidAction,
		},
		{
			name:      "nil events",
			round:     1,
			action:    actionImplement,
			events:    nil,
			wantErrIs: ErrNoEvents,
		},
		{
			name:      "empty events slice",
			round:     1,
			action:    actionImplement,
			events:    []*ares_events.Event{},
			wantErrIs: ErrNoEvents,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildRoundRecord(context.Background(), tt.round, tt.action, tt.events, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErrIs)
		})
	}
}

func TestBuildRoundRecord_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := BuildRoundRecord(ctx, 1, actionImplement,
		[]*ares_events.Event{newTaskEvent("work", "done")}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestBuildRoundRecord_InvalidRefs(t *testing.T) {
	_, err := BuildRoundRecord(context.Background(), 1, actionImplement,
		[]*ares_events.Event{newTaskEvent("work", "done")},
		map[string]string{roleCommit: "too-short"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidIdentifier)
}

func TestBuildRoundRecord_ExtractedIdentifiersMerged(t *testing.T) {
	// Identifiers found in tool output should be merged into Refs when no
	// caller-supplied value exists for the same role.
	events := []*ares_events.Event{
		newTaskEvent("Implement feature X", ""),
		newToolEvent(toolCodeRunner, "commit abc1234 deployed\nPR #142 merged"),
	}
	record, err := BuildRoundRecord(context.Background(), 1, actionImplement, events, nil)
	require.NoError(t, err)
	// "abc1234" should appear in the commit role (extracted, not caller-supplied).
	assert.Contains(t, record.Refs[roleCommit], "abc1234")
	assert.Contains(t, record.Refs[rolePR], "#142")
}

func TestBuildRoundRecord_CallerRefsTakePrecedence(t *testing.T) {
	// When a caller supplies a commit hash, the extracted one must not
	// overwrite it.
	events := []*ares_events.Event{
		newTaskEvent("Implement feature X", ""),
		newToolEvent(toolCodeRunner, "commit abc1234 deployed"),
	}
	refs := map[string]string{roleCommit: "deadbef00d"}
	record, err := BuildRoundRecord(context.Background(), 1, actionImplement, events, refs)
	require.NoError(t, err)
	assert.Equal(t, "deadbef00d", record.Refs[roleCommit],
		"caller-supplied ref takes precedence over extracted")
}

func TestExtractToolOutput_BothShapes(t *testing.T) {
	t.Run("output key", func(t *testing.T) {
		ev := &ares_events.Event{
			Payload: map[string]any{"output": "from output key"},
		}
		assert.Equal(t, "from output key", extractToolOutput(ev))
	})

	t.Run("result key fallback", func(t *testing.T) {
		ev := &ares_events.Event{
			Payload: map[string]any{ares_events.EventKeyResult: "from result key"},
		}
		assert.Equal(t, "from result key", extractToolOutput(ev))
	})

	t.Run("error key fallback", func(t *testing.T) {
		ev := &ares_events.Event{
			Payload: map[string]any{"error": "error message"},
		}
		assert.Equal(t, "error message", extractToolOutput(ev))
	})

	t.Run("nil payload returns empty", func(t *testing.T) {
		ev := &ares_events.Event{}
		assert.Equal(t, "", extractToolOutput(ev))
	})

	t.Run("nil event returns empty", func(t *testing.T) {
		assert.Equal(t, "", extractToolOutput(nil))
	})
}

func TestExtractToolName(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"tool_name key", map[string]any{"tool_name": toolFileTools}, toolFileTools},
		{"tool key fallback", map[string]any{"tool": "search"}, "search"},
		{"function key fallback", map[string]any{"function": "exec"}, "exec"},
		{"no key returns empty", map[string]any{"other": "value"}, ""},
		{"nil payload returns empty", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := &ares_events.Event{Payload: tt.payload}
			assert.Equal(t, tt.want, extractToolName(ev))
		})
	}
}

func TestMergeRefs(t *testing.T) {
	t.Run("protected takes precedence", func(t *testing.T) {
		protected := map[string]string{roleCommit: "abc1234"}
		extracted := map[string][]string{
			roleCommit: {"def5678"},
			rolePR:     {"#142"},
		}
		merged := mergeRefs(protected, extracted)
		assert.Equal(t, "abc1234", merged[roleCommit])
		assert.Equal(t, "#142", merged[rolePR])
	})

	t.Run("nil protected yields extracted only", func(t *testing.T) {
		extracted := map[string][]string{
			roleCommit: {"abc1234", "def5678"},
		}
		merged := mergeRefs(nil, extracted)
		assert.Equal(t, "abc1234, def5678", merged[roleCommit])
	})

	t.Run("empty extracted yields protected only", func(t *testing.T) {
		protected := map[string]string{roleCommit: "abc1234"}
		merged := mergeRefs(protected, nil)
		assert.Equal(t, "abc1234", merged[roleCommit])
	})

	t.Run("both nil yields empty non-nil map", func(t *testing.T) {
		merged := mergeRefs(nil, nil)
		require.NotNil(t, merged)
		assert.Empty(t, merged)
	})

	t.Run("empty slices in extracted are skipped", func(t *testing.T) {
		extracted := map[string][]string{
			roleCommit: {},
			rolePR:     {"#142"},
		}
		merged := mergeRefs(nil, extracted)
		_, hasCommit := merged[roleCommit]
		assert.False(t, hasCommit, "empty slice role should not be added")
		assert.Equal(t, "#142", merged[rolePR])
	})
}

func TestExtractIdentifiersFromEvents(t *testing.T) {
	events := []*ares_events.Event{
		newTaskEvent("Deploy commit abc1234", "PR #142 merged"),
		newToolEvent(toolCodeRunner, "server at 10.0.0.1:8080"),
	}
	result := ExtractIdentifiersFromEvents(events)
	assert.Contains(t, result[roleCommit], "abc1234")
	assert.Contains(t, result[rolePR], "#142")
	assert.Contains(t, result[roleIPPort], "10.0.0.1:8080")
}

func TestExtractIdentifiersFromEvents_Empty(t *testing.T) {
	result := ExtractIdentifiersFromEvents(nil)
	require.NotNil(t, result)
	for _, role := range []string{roleCommit, rolePR, roleIPPort, roleOwnerRepo, roleGoCmd, roleVerdict} {
		assert.NotNil(t, result[role])
	}
}

// Ensure errors package import is used (for future error identity tests).
var _ = errors.Is
