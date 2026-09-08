package builtin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// CodeRunner provides code execution capabilities with sandbox constraints.
//
// SECURITY: This tool executes code on the host system. Python is disabled by
// default. Operators must explicitly enable it via EnablePython(true) after
// reviewing the sandbox constraints. The allowlist mode is the primary defense
// — only the modules listed in allowedImports are permitted.
//
// JavaScript execution is intentionally NOT supported: the Python-oriented
// validator (import allowlist + dangerous-pattern scan) does not understand
// CommonJS `require`, so enabling node -e would hand the model an unsandboxed
// shell (require('child_process')). Re-introduce JS only together with a
// JS-specific validator (e.g. literal-argument require allowlist plus the
// node --permission model).
type CodeRunner struct {
	*base.BaseTool
	mu                sync.RWMutex
	enablePython      bool
	timeout           time.Duration
	maxOutputSize     int
	dangerousPatterns []string
	allowedImports    map[string]bool
	strictAllowlist   bool
}

// pythonIOGracePeriod bounds how long cmd.Wait keeps reading the child's output
// pipes after the process is gone or the context expired. It exists so the
// timeout path can never wedge on a pipe held open by a grandchild.
const pythonIOGracePeriod = 5 * time.Second

// allowedPythonImports is the default allowlist of modules that may be imported
// in executed Python code. Operators can extend this via AddAllowedImport.
var allowedPythonImports = []string{
	"math", "random", "statistics", "itertools", "functools",
	"collections", "decimal", "fractions", "re", "string",
	"datetime", "time", "calendar",
	"json", "csv",
}

// NewCodeRunner creates a new CodeRunner tool.
//
// By default both Python and JavaScript execution are DISABLED. Operators must
// call EnablePython(true) after evaluating the security
// implications. The strict allowlist mode is enabled by default so that only
// the modules in allowedImports can be used.
func NewCodeRunner() *CodeRunner {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"operation": {
				Type:        "string",
				Description: "Operation to perform (run_python)",
				Enum:        []interface{}{"run_python"},
			},
			"code": {
				Type:        "string",
				Description: "Code to execute",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Execution timeout in seconds (default: 30, max: 60)",
				Default:     30,
			},
			"max_output_bytes": {
				Type:        "integer",
				Description: "Maximum output size in bytes (default: 10240)",
				Default:     10240,
			},
		},
		Required: []string{"operation", "code"},
	}

	return &CodeRunner{
		BaseTool:        base.NewBaseToolWithCapabilities("code_runner", "Execute Python code with sandbox constraints", core.CategorySystem, []core.Capability{core.CapabilityExternal}, params),
		enablePython:    false,
		timeout:         30 * time.Second,
		maxOutputSize:   10240,
		strictAllowlist: true,
		allowedImports:  buildAllowedImportsSet(allowedPythonImports),
		dangerousPatterns: []string{
			"__import__", "__builtins__", "compile(",
			"eval(", "exec(", "globals(", "locals(",
			"open(", "system(", "popen", "fork(",
		},
	}
}

// NewCodeRunnerWithOptions creates a new CodeRunner with custom options.
//
// Operators are strongly encouraged to keep enablePython=false unless they
// understand the risks. The strict allowlist remains enabled.
func NewCodeRunnerWithOptions(enablePython bool, timeout time.Duration, maxOutputSize int) *CodeRunner {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			"operation": {
				Type:        "string",
				Description: "Operation to perform (run_python)",
				Enum:        []interface{}{"run_python"},
			},
			"code": {
				Type:        "string",
				Description: "Code to execute",
			},
			"timeout_seconds": {
				Type:        "integer",
				Description: "Execution timeout in seconds (default: 30, max: 60)",
				Default:     30,
			},
			"max_output_bytes": {
				Type:        "integer",
				Description: "Maximum output size in bytes (default: 10240)",
				Default:     10240,
			},
		},
		Required: []string{"operation", "code"},
	}

	return &CodeRunner{
		BaseTool:        base.NewBaseToolWithCapabilities("code_runner", "Execute Python code with sandbox constraints", core.CategorySystem, []core.Capability{core.CapabilityExternal}, params),
		enablePython:    enablePython,
		timeout:         timeout,
		maxOutputSize:   maxOutputSize,
		strictAllowlist: true,
		allowedImports:  buildAllowedImportsSet(allowedPythonImports),
		dangerousPatterns: []string{
			"__import__", "__builtins__", "compile(",
			"eval(", "exec(", "globals(", "locals(",
			"open(", "system(", "popen", "fork(",
		},
	}
}

// buildAllowedImportsSet converts a slice of module names into a set for O(1) lookup.
func buildAllowedImportsSet(modules []string) map[string]bool {
	set := make(map[string]bool, len(modules))
	for _, m := range modules {
		set[m] = true
	}
	return set
}

// Execute performs the code execution operation.
func (t *CodeRunner) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	operation, ok := params["operation"].(string)
	if !ok || operation == "" {
		return core.NewErrorResult("operation is required"), nil
	}

	code, ok := params["code"].(string)
	if !ok || code == "" {
		return core.NewErrorResult("code is required"), nil
	}

	if len(code) > 10000 {
		return core.NewErrorResult("code exceeds maximum length of 10000 characters"), nil
	}

	// Validate code for potential security issues.
	if err := t.validateCode(code); err != nil {
		return core.NewErrorResult(fmt.Sprintf("code validation failed: %v", err)), nil
	}

	// Get execution parameters.
	timeoutSeconds := getInt(params, "timeout_seconds", 30)
	if timeoutSeconds > 60 {
		timeoutSeconds = 60
	}
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}

	timeout := time.Duration(timeoutSeconds) * time.Second

	// The schema declares "max_output_bytes"; read THAT key (the previous
	// code read "max_output_size", so an LLM following the schema had its
	// value silently ignored). Accept the legacy key too for back-compat.
	maxOutputSize := getInt(params, "max_output_bytes", getInt(params, "max_output_size", t.maxOutputSize))
	if maxOutputSize < 1024 {
		maxOutputSize = 1024
	}
	const maxOutputCeiling = 1 << 20 // 1 MiB: bound hostile caller requests
	if maxOutputSize > maxOutputCeiling {
		maxOutputSize = maxOutputCeiling
	}

	// Create context with timeout.
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch operation {
	case "run_python":
		if !t.IsPythonEnabled() {
			return core.NewErrorResult("Python execution is disabled"), nil
		}
		return t.runPython(execCtx, code, maxOutputSize)
	default:
		return core.NewErrorResult(fmt.Sprintf("unsupported operation: %s", operation)), nil
	}
}

// importPattern matches an `import` statement and captures the comma-separated
// module list that follows it, up to a semicolon or newline. Capturing the full
// list (not just the first token) prevents bypasses like `import math, os` where
// only `math` was previously validated. The `;` boundary ensures statements such
// as `import math; import os` yield a separate match per statement.
var importPattern = regexp.MustCompile(`\bimport\s+([^;\n]+)`)

// fromImportPattern matches `from <module> import` statements, capturing the
// possibly-dotted module name so its top-level package can be allowlisted.
var fromImportPattern = regexp.MustCompile(`\bfrom\s+([\w.]+)\s+import`)

// splitImportList parses a comma-separated import list such as
// "math, os as o, json" and returns the top-level module names
// ["math", "os", "json"]. It strips `as` aliases and reduces dotted
// paths like "os.path" to their top-level package "os".
func splitImportList(s string) []string {
	var modules []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip an `as alias` suffix: keep the first whitespace-delimited token.
		if fields := strings.Fields(part); len(fields) > 0 {
			part = fields[0]
		}
		modules = append(modules, topLevelModule(part))
	}
	return modules
}

// topLevelModule returns the top-level package of a dotted module path,
// e.g. "os.path" -> "os". Non-dotted names are returned unchanged.
func topLevelModule(mod string) string {
	if idx := strings.IndexByte(mod, '.'); idx >= 0 {
		return mod[:idx]
	}
	return mod
}

// stripPythonComments removes single-line comments from Python code.
func stripPythonComments(code string) string {
	lines := strings.Split(code, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		// Find the first unquoted '#' character.
		inSingleQuote := false
		inDoubleQuote := false
		commentStart := -1
		for i, c := range line {
			switch {
			case c == '\'' && !inDoubleQuote:
				inSingleQuote = !inSingleQuote
			case c == '"' && !inSingleQuote:
				inDoubleQuote = !inDoubleQuote
			case c == '#' && !inSingleQuote && !inDoubleQuote:
				commentStart = i
				goto foundComment
			}
		}
	foundComment:
		if commentStart >= 0 {
			line = line[:commentStart]
		}
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// foldLineContinuations merges backslash-continued lines (Python's explicit
// line continuation) into a single logical line. Without this, validation
// would only see the first fragment of `import math \` + newline + `, os`,
// letting `os` slip past the allowlist while Python still imports it.
func foldLineContinuations(code string) string {
	lines := strings.Split(code, "\n")
	var b strings.Builder
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		if strings.HasSuffix(trimmed, "\\") {
			// Drop the continuation backslash and join with a space.
			b.WriteString(strings.TrimSuffix(trimmed, "\\"))
			b.WriteString(" ")
			continue
		}
		b.WriteString(lines[i])
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// collapseCallSpacing removes whitespace between a callee and its opening
// parenthesis, so `open ("/etc/passwd")` normalizes to `open("/etc/passwd")`.
// The dangerous-pattern denylist matched literal strings like "open(", which
// Python's legal whitespace defeated; normalizing first closes that hole without
// weakening the patterns themselves.
func collapseCallSpacing(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '(' {
			// Drop the whitespace run immediately before this "(".
			j := len(out)
			for j > 0 {
				switch out[j-1] {
				case ' ', '\t', '\n', '\r':
					j--
					continue
				}
				break
			}
			out = out[:j]
		}
		out = append(out, s[i])
	}
	return string(out)
}

// validateCode checks code for potential security issues.
//
// When strictAllowlist is true (the default), only the modules listed in
// allowedImports may be imported. The dangerous-pattern denylist is retained
// as defense-in-depth but is not relied upon as the primary control.
func (t *CodeRunner) validateCode(code string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	stripped := foldLineContinuations(stripPythonComments(code))
	lowerCode := strings.ToLower(stripped)

	// Defense-in-depth: reject known dangerous builtins. Match against the
	// call-normalized form, not the raw text: Python permits whitespace (and a
	// folded newline) between a callee and its "(", so a literal "open(" test
	// was defeated by a single space — and "open" is a builtin needing no
	// import, so the allowlist above never sees it and this denylist is the
	// ONLY control over file access.
	callCode := collapseCallSpacing(lowerCode)
	for _, pattern := range t.dangerousPatterns {
		if strings.Contains(callCode, strings.ToLower(pattern)) {
			return fmt.Errorf("potentially dangerous pattern detected: %s", pattern)
		}
	}

	if t.strictAllowlist {
		// Strip `from X import Y` statements so the `import X` check below does
		// not falsely match the `import` keyword inside them (e.g. `from json
		// import loads` would otherwise be parsed as `import loads`).
		importCode := fromImportPattern.ReplaceAllString(lowerCode, "")

		// Validate `import X[, Y, ...]` statements. Each module is reduced to
		// its top-level package and checked against the allowlist.
		for _, m := range importPattern.FindAllStringSubmatch(importCode, -1) {
			for _, module := range splitImportList(m[1]) {
				if !t.allowedImports[module] {
					return fmt.Errorf("import not in allowlist: %s", module)
				}
			}
		}

		// Validate `from X import Y` statements. The possibly-dotted module is
		// reduced to its top-level package before allowlist lookup.
		for _, m := range fromImportPattern.FindAllStringSubmatch(lowerCode, -1) {
			if len(m) >= 2 {
				if module := topLevelModule(m[1]); !t.allowedImports[module] {
					return fmt.Errorf("import not in allowlist: %s", module)
				}
			}
		}
	}

	return nil
}

// limitedWriter wraps a bytes.Buffer and stops writing once maxBytes has been
// reached, preventing unbounded memory growth from malicious output.
type limitedWriter struct {
	buf       *bytes.Buffer
	maxBytes  int
	truncated bool
}

// newLimitedWriter creates a writer that caps output at maxBytes.
func newLimitedWriter(maxBytes int) *limitedWriter {
	return &limitedWriter{
		buf:      &bytes.Buffer{},
		maxBytes: maxBytes,
	}
}

// Write writes bytes to the buffer, stopping once maxBytes is exceeded.
func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.truncated {
		return len(p), nil
	}
	if w.buf.Len()+len(p) > w.maxBytes {
		remaining := w.maxBytes - w.buf.Len()
		if remaining > 0 {
			if _, err := w.buf.Write(p[:remaining]); err != nil {
				return 0, err
			}
		}
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// String returns the captured output, with a truncation marker if needed.
func (w *limitedWriter) String() string {
	out := w.buf.String()
	if w.truncated {
		out += "\n... (output truncated)"
	}
	return out
}

// runPython executes Python code.
func (t *CodeRunner) runPython(ctx context.Context, code string, maxOutputSize int) (core.Result, error) {
	cmd := exec.CommandContext(ctx, "python3", "-c", code) // #nosec G204
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// WaitDelay is what makes the timeout path actually terminate. On context
	// expiry CommandContext kills the DIRECT child only, and Wait then keeps
	// blocking on the stdout/stderr pipes for as long as any grandchild holds a
	// write end — so a script that spawns a child and hangs would pin this
	// goroutine forever and the process-group kill below would never run. With
	// WaitDelay set, Go gives the pipes a grace period and then force-closes
	// and returns.
	cmd.WaitDelay = pythonIOGracePeriod
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	workDir, err := os.MkdirTemp("", "code-runner-*")
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to create temp dir: %v", err)), nil
	}
	cmd.Dir = workDir
	defer func() {
		if rmErr := os.RemoveAll(workDir); rmErr != nil {
			log.Error("failed to clean up temp dir", "path", workDir, "error", rmErr)
		}
	}()

	stdout := newLimitedWriter(maxOutputSize)
	stderr := newLimitedWriter(maxOutputSize)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	startTime := time.Now()
	runErr := cmd.Run()
	executionTime := time.Since(startTime)

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if cmd.Process != nil {
				// Best-effort kill of the whole process group; the child
				// processes spawned by the script must not outlive the
				// timeout. Ignore errors on this cleanup path.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return core.NewResult(false, map[string]interface{}{
				"operation":      "run_python",
				"success":        false,
				"error":          "execution timeout",
				"stderr":         stderr.String(),
				"execution_time": executionTime.Milliseconds(),
			}), nil
		}

		return core.NewResult(false, map[string]interface{}{
			"operation":      "run_python",
			"success":        false,
			"error":          runErr.Error(),
			"stderr":         stderr.String(),
			"execution_time": executionTime.Milliseconds(),
		}), nil
	}

	return core.NewResult(true, map[string]interface{}{
		"operation":      "run_python",
		"success":        true,
		"output":         stdout.String(),
		"stderr":         stderr.String(),
		"execution_time": executionTime.Milliseconds(),
	}), nil
}

// EnablePython enables or disables Python execution.
func (t *CodeRunner) EnablePython(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enablePython = enabled
}

// SetTimeout sets the execution timeout.
func (t *CodeRunner) SetTimeout(timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timeout = timeout
}

// SetMaxOutputSize sets the maximum output size.
func (t *CodeRunner) SetMaxOutputSize(size int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxOutputSize = size
}

// IsPythonEnabled returns whether Python execution is enabled.
func (t *CodeRunner) IsPythonEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.enablePython
}

// AddAllowedImport adds a module name to the Python import allowlist.
func (t *CodeRunner) AddAllowedImport(module string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.allowedImports[module] = true
}

// AddDangerousPattern adds a custom dangerous pattern to validate against.
func (t *CodeRunner) AddDangerousPattern(pattern string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dangerousPatterns = append(t.dangerousPatterns, pattern)
}

// GetSupportedLanguages returns the list of supported languages. Only Python
// is supported since the JavaScript path was removed (no JS validator).
func (t *CodeRunner) GetSupportedLanguages() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	languages := []string{}
	if t.enablePython {
		languages = append(languages, "python")
	}
	return languages
}

// Helper functions.
func getInt(params map[string]interface{}, key string, defaultVal int) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// io is imported to ensure the limitedWriter satisfies io.Writer at compile time.
var _ io.Writer = (*limitedWriter)(nil)
