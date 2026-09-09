// Package discovery provides runtime discovery of native host commands and
// adapts them into core.Tool instances for the tool registry.
//
// This is the "本机工具发现" primitive (ares-vs-prime-agent 5.8): probe
// `command -v` + `--help` for each allowlisted command and expose the ones
// that exist as executable tools, so agents can call host utilities without
// every tool description being baked into the context up front.
//
// Security boundary: only commands listed in the allowlist are ever probed or
// executed. The command name is fixed at construction time (never taken from
// request parameters), and execution uses exec.CommandContext with an
// argument slice — no shell, no string interpolation — so a caller cannot
// inject shell metacharacters or invoke unlisted binaries.
package discovery

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// limitedBuffer is an io.Writer that caps total bytes written and sets
// the exceeded flag when the limit is hit. It replaces bytes.Buffer so a
// chatty command cannot exhaust memory before the post-run size check.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.exceeded {
		return len(p), nil // discard silently; caller checks the flag
	}
	remaining := lb.limit - lb.buf.Len()
	if len(p) >= remaining {
		lb.buf.Write(p[:remaining])
		lb.exceeded = true
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string { return lb.buf.String() }
func (lb *limitedBuffer) Bytes() []byte  { return lb.buf.Bytes() }

// ExecFunc runs a command with the given arguments and returns its stdout.
// It is abstracted so tests can inject a fake runner instead of executing
// real host commands.
type ExecFunc func(ctx context.Context, name string, args []string) ([]byte, error)

// LookupFunc resolves a command name to its executable path, like `command -v`.
type LookupFunc func(name string) (string, error)

// paramArgs is the parameter key carrying CLI arguments for a CommandTool.
const paramArgs = "args"

// Option configures a Discoverer.
type Option func(*Discoverer)

// WithExec overrides the command execution function (test injection).
func WithExec(fn ExecFunc) Option {
	return func(d *Discoverer) {
		d.exec = fn
	}
}

// WithLookup overrides the path lookup function (test injection).
func WithLookup(fn LookupFunc) Option {
	return func(d *Discoverer) {
		d.lookup = fn
	}
}

// Discoverer probes an allowlist of native commands and builds core.Tool
// instances for the ones present on the host.
type Discoverer struct {
	allowlist []string
	exec      ExecFunc
	lookup    LookupFunc
}

// NewDiscoverer creates a Discoverer for the given allowlist. Only these
// command names are probed and executed; anything else is rejected.
func NewDiscoverer(allowlist []string, opts ...Option) *Discoverer {
	d := &Discoverer{
		allowlist: allowlist,
		exec: func(ctx context.Context, name string, args []string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // allowlist-gated by design
			// Use a limited writer so the buffer cannot grow unbounded while
			// the command is still running — a chatty command (e.g. `yes`)
			// would otherwise exhaust memory before the post-run size check.
			outBuf := &limitedBuffer{limit: maxCommandOutputBytes}
			errBuf := &limitedBuffer{limit: maxCommandOutputBytes}
			cmd.Stdout = outBuf
			cmd.Stderr = errBuf
			if err := cmd.Run(); err != nil {
				if outBuf.exceeded || errBuf.exceeded {
					return nil, fmt.Errorf("command %q output exceeds %d bytes", name, maxCommandOutputBytes)
				}
				if stderr := strings.TrimSpace(errBuf.String()); stderr != "" {
					return nil, fmt.Errorf("%w: %s", err, stderr)
				}
				return nil, err
			}
			if outBuf.exceeded {
				return nil, fmt.Errorf("command %q output exceeds %d bytes; refusing to return partial output", name, maxCommandOutputBytes)
			}
			return outBuf.Bytes(), nil
		},
		lookup: exec.LookPath,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Discover probes each allowlisted command and returns tools for the ones
// that exist on the host. Non-existent commands are skipped, not errors, so a
// host missing optional utilities degrades gracefully.
func (d *Discoverer) Discover(ctx context.Context) ([]core.Tool, error) {
	tools := make([]core.Tool, 0, len(d.allowlist))
	for _, name := range d.allowlist {
		if !d.allowed(name) {
			continue
		}
		if _, err := d.lookup(name); err != nil {
			// Command not installed on this host — skip silently.
			continue
		}
		desc, err := d.describe(ctx, name)
		if err != nil {
			desc = fmt.Sprintf("Native command %q", name)
		}
		tools = append(tools, NewCommandTool(name, desc, d.exec, d.allowed))
	}
	return tools, nil
}

// describe extracts a one-line summary from `name --help` output.
func (d *Discoverer) describe(ctx context.Context, name string) (string, error) {
	out, err := d.exec(ctx, name, []string{"--help"})
	if err != nil {
		return "", err
	}
	line := firstNonEmptyLine(string(out))
	if line == "" {
		return "", fmt.Errorf("empty help output for %q", name)
	}
	return strings.TrimSpace(line), nil
}

func (d *Discoverer) allowed(name string) bool {
	for _, allow := range d.allowlist {
		if allow == name {
			return true
		}
	}
	return false
}

// firstNonEmptyLine returns the first non-blank line of s.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// CommandTool adapts a native host command into a core.Tool. The command name
// is fixed at construction; Execute only passes user-supplied arguments to
// exec.CommandContext, so the executable itself can never be chosen by the
// caller (no arbitrary command execution).
type CommandTool struct {
	name    string
	desc    string
	exec    ExecFunc
	allowed func(string) bool
}

// NewCommandTool creates a CommandTool wrapping the given native command.
// allowed is the allowlist checker; Execute rejects if the tool name is not
// permitted (defense in depth against misconfiguration).
func NewCommandTool(name, desc string, exec ExecFunc, allowed func(string) bool) *CommandTool {
	return &CommandTool{name: name, desc: desc, exec: exec, allowed: allowed}
}

// Name returns the command name.
func (t *CommandTool) Name() string { return t.name }

// Description returns the one-line help summary.
func (t *CommandTool) Description() string { return t.desc }

// Category returns the system category.
func (t *CommandTool) Category() core.ToolCategory { return core.CategorySystem }

// Capabilities returns no capabilities (native commands are generic).
func (t *CommandTool) Capabilities() []core.Capability { return nil }

// Parameters declares a single string-array argument: the CLI arguments to
// pass to the command.
func (t *CommandTool) Parameters() *core.ParameterSchema {
	return &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			paramArgs: {
				Type:        "array",
				Description: "Arguments to pass to the command",
			},
		},
	}
}

// maxCommandOutputBytes caps stdout captured from a native command so a
// misbehaving or malicious command (e.g. `yes`) cannot exhaust memory.
const maxCommandOutputBytes = 1 << 20 // 1 MiB

// Execute runs the command with the given arguments and returns stdout.
// The args parameter accepts either []string or []interface{} (each element a
// string); other shapes are treated as "no args" rather than silently
// producing a mismatched invocation.
func (t *CommandTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	if t.allowed != nil && !t.allowed(t.name) {
		return core.NewErrorResult("command not in allowlist"), nil
	}
	var args []string
	switch raw := params[paramArgs].(type) {
	case []string:
		args = raw
	case []interface{}:
		for _, a := range raw {
			if s, ok := a.(string); ok {
				args = append(args, s)
			}
		}
	}
	out, err := t.exec(ctx, t.name, args)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("command %q failed: %v", t.name, err)), nil
	}
	// the exec closure rejects over-cap output upstream, so len(out) here
	// is always within budget — no second truncation check to drift.
	return core.NewResult(true, map[string]interface{}{
		"stdout": string(out),
	}), nil
}

// compile-time interface check
var _ core.Tool = (*CommandTool)(nil)
