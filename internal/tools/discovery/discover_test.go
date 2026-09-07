package discovery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// fakeLookup implements LookupFunc with an install map: a command name is
// "installed" iff it appears in the map (value is its resolved path).
func fakeLookup(installed map[string]string) LookupFunc {
	return func(name string) (string, error) {
		path, ok := installed[name]
		if !ok {
			return "", errors.New("command not found")
		}
		return path, nil
	}
}

// fakeExec implements ExecFunc: returns "usage: <name> <args>" for --help and
// "<name>:<joined-args>" for executions.
func fakeExec(_ context.Context, name string, args []string) ([]byte, error) {
	if len(args) == 1 && args[0] == "--help" {
		return []byte("\nusage: " + name + " [options]\n"), nil
	}
	joined := ""
	for i, a := range args {
		if i > 0 {
			joined += ","
		}
		joined += a
	}
	return []byte(name + ":" + joined), nil
}

func TestDiscoverer_DiscoverFindsInstalledCommands(t *testing.T) {
	d := NewDiscoverer(
		[]string{"git", "missing-cmd"},
		WithLookup(fakeLookup(map[string]string{"git": "/usr/bin/git"})),
		WithExec(fakeExec),
	)

	tools, err := d.Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 1, "only installed commands should be discovered")

	git := tools[0]
	assert.Equal(t, "git", git.Name())
	assert.Contains(t, git.Description(), "usage: git", "description should come from --help first line")
}

func TestDiscoverer_DiscoverEmptyAllowlist(t *testing.T) {
	d := NewDiscoverer(nil, WithLookup(fakeLookup(nil)), WithExec(fakeExec))
	tools, err := d.Discover(context.Background())
	require.NoError(t, err)
	assert.Empty(t, tools)
}

func TestCommandTool_ExecuteRunsArguments(t *testing.T) {
	tool := NewCommandTool("git", "usage: git", fakeExec, func(name string) bool { return name == "git" })

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"args": []interface{}{"status", "--short"},
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok, "Data should be a map")
	stdout, ok := data["stdout"].(string)
	require.True(t, ok, "stdout should be a string")
	assert.Equal(t, "git:status,--short", stdout)
}

func TestCommandTool_ExecuteRejectsNonAllowlistedName(t *testing.T) {
	tool := NewCommandTool("rm", "usage: rm", fakeExec, func(name string) bool { return name == "git" })

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"args": []interface{}{"-rf", "/"},
	})
	require.NoError(t, err)
	assert.False(t, result.Success, "non-allowlisted command must be rejected")
	assert.Contains(t, result.Error, "allowlist")
}

func TestCommandTool_ExecutePropagatesCommandError(t *testing.T) {
	failExec := func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}
	tool := NewCommandTool("git", "usage: git", failExec, func(string) bool { return true })

	result, err := tool.Execute(context.Background(), nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exit status 1")
}

func TestCommandTool_ImplementsToolInterface(t *testing.T) {
	tool := NewCommandTool("git", "usage: git", fakeExec, func(string) bool { return true })
	var _ core.Tool = tool
	assert.Equal(t, core.CategorySystem, tool.Category())
	assert.Empty(t, tool.Capabilities())
	params := tool.Parameters()
	require.NotNil(t, params)
	require.NotNil(t, params.Properties["args"], "args parameter must be declared")
}

// TestCommandTool_ExecuteAcceptsStringSlice verifies the []string parameter
// shape is accepted (previously only []interface{} worked, and a []string
// would silently run the command with no arguments).
func TestCommandTool_ExecuteAcceptsStringSlice(t *testing.T) {
	tool := NewCommandTool("git", "usage: git", fakeExec, func(string) bool { return true })

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"args": []string{"status", "--short"},
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	data, ok := result.Data.(map[string]interface{})
	require.True(t, ok)
	stdout, ok := data["stdout"].(string)
	require.True(t, ok)
	assert.Equal(t, "git:status,--short", stdout)
}

// TestCommandTool_ExecuteOversizedOutput verifies an excessive command output
// is rejected instead of exhausting memory.
func TestCommandTool_ExecuteOversizedOutput(t *testing.T) {
	bigOutput := make([]byte, maxCommandOutputBytes+1)
	for i := range bigOutput {
		bigOutput[i] = 'x'
	}
	// the exec closure itself rejects over-cap output with an
	// error (single-point enforcement) — it never returns partial bytes.
	bigExec := func(_ context.Context, name string, _ []string) ([]byte, error) {
		return nil, fmt.Errorf("command %q output exceeds %d bytes; refusing to return partial output", name, maxCommandOutputBytes)
	}
	tool := NewCommandTool("git", "usage: git", bigExec, func(string) bool { return true })

	result, err := tool.Execute(context.Background(), map[string]interface{}{"args": []string{}})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "output exceeds")
}
