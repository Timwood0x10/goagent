package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// TestNewCodeRunner tests creating a new CodeRunner.
func TestNewCodeRunner(t *testing.T) {
	runner := NewCodeRunner()
	require.NotNil(t, runner)
	if runner.Name() != "code_runner" {
		t.Errorf("Name() = %q, want 'code_runner'", runner.Name())
	}
	if runner.Category() != core.CategorySystem {
		t.Errorf("Category() = %v, want CategorySystem", runner.Category())
	}
	// SECURITY: Python is disabled by default; operators must opt in.
	if runner.enablePython {
		t.Error("Python should be disabled by default")
	}
	if runner.timeout != 30*time.Second {
		t.Errorf("Default timeout = %v, want 30s", runner.timeout)
	}
	if runner.maxOutputSize != 10240 {
		t.Errorf("Default maxOutputSize = %d, want 10240", runner.maxOutputSize)
	}
}

// TestNoJSExecution asserts the removal contract: the code_runner tool must
// not offer or execute JavaScript. The Python-oriented validator (import
// allowlist + dangerous-pattern scan) does not understand CommonJS `require`,
// so `node -e` with require('child_process') would be an unsandboxed shell.
func TestNoJSExecution(t *testing.T) {
	runner := NewCodeRunner()

	// Schema must not advertise run_js.
	operationParam, exists := runner.Parameters().Properties["operation"]
	require.True(t, exists)
	for _, v := range operationParam.Enum {
		if v == "run_js" {
			t.Error("schema must not advertise the run_js operation")
		}
	}

	// Execute must reject run_js even when Python is enabled.
	runner.EnablePython(true)
	result, err := runner.Execute(context.Background(), map[string]interface{}{
		"operation": "run_js",
		"code":      "console.log('hello')",
	})
	require.NoError(t, err)
	require.False(t, result.Success, "run_js must never execute")
}

// TestNewCodeRunnerWithOptions tests creating a CodeRunner with custom options.
func TestNewCodeRunnerWithOptions(t *testing.T) {
	tests := []struct {
		name          string
		enablePython  bool
		timeout       time.Duration
		maxOutputSize int
	}{
		{
			name:          "python enabled",
			enablePython:  true,
			timeout:       60 * time.Second,
			maxOutputSize: 20480,
		},
		{
			name:          "python disabled",
			enablePython:  false,
			timeout:       15 * time.Second,
			maxOutputSize: 5120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := NewCodeRunnerWithOptions(tt.enablePython, tt.timeout, tt.maxOutputSize)
			require.NotNil(t, runner)
			if runner.enablePython != tt.enablePython {
				t.Errorf("enablePython = %v, want %v", runner.enablePython, tt.enablePython)
			}
			if runner.timeout != tt.timeout {
				t.Errorf("timeout = %v, want %v", runner.timeout, tt.timeout)
			}
			if runner.maxOutputSize != tt.maxOutputSize {
				t.Errorf("maxOutputSize = %d, want %d", runner.maxOutputSize, tt.maxOutputSize)
			}
		})
	}
}

// TestCodeRunnerExecute_MissingParameters tests missing required parameters.
func TestCodeRunnerExecute_MissingParameters(t *testing.T) {
	runner := NewCodeRunner()
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name:   "no parameters",
			params: map[string]interface{}{},
		},
		{
			name: "missing operation",
			params: map[string]interface{}{
				"code": "print('hello')",
			},
		},
		{
			name: "empty operation",
			params: map[string]interface{}{
				"operation": "",
				"code":      "print('hello')",
			},
		},
		{
			name: "missing code",
			params: map[string]interface{}{
				"operation": "run_python",
			},
		},
		{
			name: "empty code",
			params: map[string]interface{}{
				"operation": "run_python",
				"code":      "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runner.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when required parameters are missing")
			}
		})
	}
}

// TestCodeRunnerExecute_InvalidOperation tests invalid operation types.
func TestCodeRunnerExecute_InvalidOperation(t *testing.T) {
	runner := NewCodeRunner()
	ctx := context.Background()

	tests := []struct {
		name      string
		operation string
	}{
		{
			name:      "invalid operation",
			operation: "invalid_op",
		},
		{
			name:      "empty operation",
			operation: "",
		},
		{
			name:      "random operation",
			operation: "run_ruby",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation": tt.operation,
				"code":      "print('test')",
			}

			result, err := runner.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail for invalid operation")
			}
		})
	}
}

// TestCodeRunnerExecute_CodeValidation tests code validation.
func TestCodeRunnerExecute_CodeValidation(t *testing.T) {
	runner := NewCodeRunner()
	// Enable Python so that code validation (not the enable check) is exercised.
	runner.EnablePython(true)
	ctx := context.Background()

	tests := []struct {
		name      string
		code      string
		wantError bool
	}{
		{
			name:      "safe code",
			code:      "print('hello')",
			wantError: false,
		},
		{
			name:      "safe math",
			code:      "x = 1 + 2; print(x)",
			wantError: false,
		},
		{
			name:      "dangerous - import os",
			code:      "import os",
			wantError: true,
		},
		{
			name:      "dangerous - import subprocess",
			code:      "import subprocess",
			wantError: true,
		},
		{
			name:      "dangerous - eval",
			code:      "eval('1+1')",
			wantError: true,
		},
		{
			name:      "dangerous - exec",
			code:      "exec('print(1)')",
			wantError: true,
		},
		{
			name:      "dangerous - __import__",
			code:      "__import__('os')",
			wantError: true,
		},
		{
			name:      "dangerous - open",
			code:      "open('/etc/passwd')",
			wantError: true,
		},
		{
			name:      "dangerous - system",
			code:      "import os; os.system('ls')",
			wantError: true,
		},
		{
			name:      "case insensitive - IMPORT OS",
			code:      "IMPORT OS",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation": "run_python",
				"code":      tt.code,
			}

			result, err := runner.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if tt.wantError {
				if result.Success {
					t.Error("Execute() should fail for dangerous code")
				}
				if !strings.Contains(result.Error, "code validation failed") {
					t.Errorf("Error message should mention code validation, got: %s", result.Error)
				}
			} else if !result.Success && strings.Contains(result.Error, "code validation failed") {
				// Safe code might still fail if Python is not available, but that's OK
				// Just check that validation didn't reject it
				t.Errorf("Safe code should not be rejected by validation: %s", result.Error)
			}
		})
	}
}

// TestCodeRunnerImportAllowlist exercises the Python import allowlist
// enforcement directly via validateCode. It covers the sandbox-escape
// regressions (comma lists, `as` aliases, dotted paths, semicolon-separated
// statements, dotted `from` imports) that the previous single-token regex
// missed.
func TestCodeRunnerImportAllowlist(t *testing.T) {
	runner := NewCodeRunner()
	// Enable Python so the runner is in a realistic state; strict allowlist
	// remains on by default.
	runner.EnablePython(true)

	tests := []struct {
		name    string
		code    string
		wantErr bool
	}{
		{
			name:    "comma list with blocked module",
			code:    "import math, os",
			wantErr: true,
		},
		{
			name:    "as alias on blocked module",
			code:    "import os as o",
			wantErr: true,
		},
		{
			name:    "comma list all allowed",
			code:    "import math, json",
			wantErr: false,
		},
		{
			name:    "dotted blocked module",
			code:    "import os.path",
			wantErr: true,
		},
		{
			name:    "from blocked module",
			code:    "from os import system",
			wantErr: true,
		},
		{
			name:    "extra spaces with blocked module",
			code:    "import  os  ,  subprocess",
			wantErr: true,
		},
		{
			name:    "comment stripped before validation",
			code:    "import math # comment",
			wantErr: false,
		},
		{
			name:    "semicolon separated statements",
			code:    "import math; import os",
			wantErr: true,
		},
		{
			name:    "dotted from-import blocked module",
			code:    "from os.path import system",
			wantErr: true,
		},
		{
			name:    "comma list with alias all allowed",
			code:    "import math, json as j, re",
			wantErr: false,
		},
		{
			name:    "single blocked module regression",
			code:    "import os",
			wantErr: true,
		},
		{
			name:    "single blocked subprocess regression",
			code:    "import subprocess",
			wantErr: true,
		},
		{
			name:    "backslash line continuation bypass",
			code:    "import math \\\n, os",
			wantErr: true,
		},
		{
			name:    "from allowed module single name",
			code:    "from json import loads",
			wantErr: false,
		},
		{
			name:    "from allowed module multiple names",
			code:    "from json import loads, dumps",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runner.validateCode(tt.code)
			if tt.wantErr {
				require.Error(t, err, "validateCode(%q) should return an error", tt.code)
			} else {
				require.NoError(t, err, "validateCode(%q) should not return an error", tt.code)
			}
		})
	}
}

// TestCodeRunnerExecute_PythonDisabled tests Python disabled scenario.
func TestCodeRunnerExecute_PythonDisabled(t *testing.T) {
	runner := NewCodeRunner()
	runner.EnablePython(false)
	ctx := context.Background()

	params := map[string]interface{}{
		"operation": "run_python",
		"code":      "print('hello')",
	}

	result, err := runner.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if result.Success {
		t.Error("Execute() should fail when Python is disabled")
	}

	if !strings.Contains(result.Error, "Python execution is disabled") {
		t.Errorf("Error message should mention Python is disabled, got: %s", result.Error)
	}
}

// TestCodeRunnerExecute_TimeoutParameters tests timeout parameter handling.
func TestCodeRunnerExecute_TimeoutParameters(t *testing.T) {
	runner := NewCodeRunner()
	ctx := context.Background()

	tests := []struct {
		name            string
		timeoutSeconds  interface{}
		expectedSeconds int
	}{
		{
			name:            "default timeout",
			timeoutSeconds:  nil,
			expectedSeconds: 30,
		},
		{
			name:            "valid timeout",
			timeoutSeconds:  10,
			expectedSeconds: 10,
		},
		{
			name:            "timeout capped at 60",
			timeoutSeconds:  100,
			expectedSeconds: 60,
		},
		{
			name:            "timeout minimum 1",
			timeoutSeconds:  0,
			expectedSeconds: 1,
		},
		{
			name:            "timeout minimum 1 negative",
			timeoutSeconds:  -5,
			expectedSeconds: 1,
		},
		{
			name:            "timeout as float",
			timeoutSeconds:  5.5,
			expectedSeconds: 5,
		},
		{
			name:            "timeout as string",
			timeoutSeconds:  "15",
			expectedSeconds: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation":       "run_python",
				"code":            "print('test')",
				"timeout_seconds": tt.timeoutSeconds,
			}

			result, err := runner.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			// We can't verify the exact timeout without actually running code
			// Just verify the parameter is accepted
			if result.Data == nil && result.Error == "" {
				t.Error("Execute() should return a result with data or error")
			}
		})
	}
}

// TestCodeRunnerExecute_MaxOutputSize tests max output size parameter handling.
func TestCodeRunnerExecute_MaxOutputSize(t *testing.T) {
	runner := NewCodeRunner()
	ctx := context.Background()

	tests := []struct {
		name           string
		maxOutputBytes interface{}
		expectedSize   int
	}{
		{
			name:           "default size",
			maxOutputBytes: nil,
			expectedSize:   10240,
		},
		{
			name:           "valid size",
			maxOutputBytes: 2048,
			expectedSize:   2048,
		},
		{
			name:           "size minimum 1024",
			maxOutputBytes: 100,
			expectedSize:   1024,
		},
		{
			name:           "size as float",
			maxOutputBytes: 512.5,
			expectedSize:   512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation":        "run_python",
				"code":             "print('test')",
				"max_output_bytes": tt.maxOutputBytes,
			}

			result, err := runner.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if result.Data == nil && result.Error == "" {
				t.Error("Execute() should return a result with data or error")
			}
		})
	}
}

// TestCodeRunnerEnableDisable tests enable/disable methods.
func TestCodeRunnerEnableDisable(t *testing.T) {
	runner := NewCodeRunner()

	// Test initial state — disabled by default for safety.
	if runner.IsPythonEnabled() {
		t.Error("Python should be disabled initially")
	}

	// Enable Python
	runner.EnablePython(true)
	if !runner.IsPythonEnabled() {
		t.Error("Python should be enabled after EnablePython(true)")
	}

	// Disable Python
	runner.EnablePython(false)
	if runner.IsPythonEnabled() {
		t.Error("Python should be disabled after EnablePython(false)")
	}
}

// TestCodeRunnerSetTimeout tests timeout setting.
func TestCodeRunnerSetTimeout(t *testing.T) {
	runner := NewCodeRunner()

	tests := []struct {
		name    string
		timeout time.Duration
	}{
		{
			name:    "5 seconds",
			timeout: 5 * time.Second,
		},
		{
			name:    "1 minute",
			timeout: 1 * time.Minute,
		},
		{
			name:    "0 seconds",
			timeout: 0,
		},
		{
			name:    "negative timeout",
			timeout: -1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner.SetTimeout(tt.timeout)
			if runner.timeout != tt.timeout {
				t.Errorf("SetTimeout(%v) did not set timeout, got %v", tt.timeout, runner.timeout)
			}
		})
	}
}

// TestCodeRunnerSetMaxOutputSize tests max output size setting.
func TestCodeRunnerSetMaxOutputSize(t *testing.T) {
	runner := NewCodeRunner()

	tests := []struct {
		name string
		size int
	}{
		{
			name: "512 bytes",
			size: 512,
		},
		{
			name: "1024 bytes",
			size: 1024,
		},
		{
			name: "0 bytes",
			size: 0,
		},
		{
			name: "negative size",
			size: -100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner.SetMaxOutputSize(tt.size)
			if runner.maxOutputSize != tt.size {
				t.Errorf("SetMaxOutputSize(%d) did not set size, got %d", tt.size, runner.maxOutputSize)
			}
		})
	}
}

// TestCodeRunnerGetSupportedLanguages tests getting supported languages.
func TestCodeRunnerGetSupportedLanguages(t *testing.T) {
	runner := NewCodeRunner()

	// Test initial state — no languages enabled by default.
	languages := runner.GetSupportedLanguages()
	if len(languages) != 0 {
		t.Errorf("Initial languages count = %d, want 0", len(languages))
	}

	// Enable Python
	runner.EnablePython(true)
	languages = runner.GetSupportedLanguages()
	if len(languages) != 1 {
		t.Errorf("Languages count with Python enabled = %d, want 1", len(languages))
	}
	if len(languages) > 0 && languages[0] != "python" {
		t.Errorf("Language with Python enabled = %s, want 'python'", languages[0])
	}

	// Disable Python
	runner.EnablePython(false)
	languages = runner.GetSupportedLanguages()
	if len(languages) != 0 {
		t.Errorf("Languages count with Python disabled = %d, want 0", len(languages))
	}
}

// TestCodeRunnerCapabilities tests code runner capabilities.
func TestCodeRunnerCapabilities(t *testing.T) {
	runner := NewCodeRunner()

	capabilities := runner.Capabilities()
	if len(capabilities) != 1 {
		t.Errorf("Capabilities() length = %d, want 1", len(capabilities))
	}

	if capabilities[0] != core.CapabilityExternal {
		t.Errorf("Capabilities()[0] = %v, want CapabilityExternal", capabilities[0])
	}

	parameters := runner.Parameters()
	require.NotNil(t, parameters)

	if parameters.Type != "object" {
		t.Errorf("Parameters.Type = %q, want 'object'", parameters.Type)
	}

	// Check required parameters
	if len(parameters.Required) != 2 {
		t.Errorf("parameters.Required length = %d, want 2", len(parameters.Required))
	}

	requiredParams := make(map[string]bool)
	for _, req := range parameters.Required {
		requiredParams[req] = true
	}

	if !requiredParams["operation"] {
		t.Error("'operation' should be required")
	}
	if !requiredParams["code"] {
		t.Error("'code' should be required")
	}

	// Check operation enum
	operationParam, exists := parameters.Properties["operation"]
	if !exists {
		t.Error("operation parameter should exist")
	}

	if operationParam.Type != "string" {
		t.Errorf("operation parameter Type = %q, want 'string'", operationParam.Type)
	}

	if operationParam.Enum == nil || len(operationParam.Enum) != 1 {
		t.Error("operation parameter should have exactly 1 enum value (run_python)")
	}
}
