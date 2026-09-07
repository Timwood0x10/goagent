package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// TestNewFileTools tests creating a new FileTools.
func TestNewFileTools(t *testing.T) {
	tools := NewFileTools()
	require.NotNil(t, tools)
	if tools.Name() != "file_tools" {
		t.Errorf("Name() = %q, want 'file_tools'", tools.Name())
	}
	if tools.Category() != core.CategorySystem {
		t.Errorf("Category() = %v, want CategorySystem", tools.Category())
	}

	capabilities := tools.Capabilities()
	if len(capabilities) != 1 {
		t.Errorf("Capabilities() length = %d, want 1", len(capabilities))
	}
	if capabilities[0] != core.CapabilityFile {
		t.Errorf("Capabilities()[0] = %v, want CapabilityFile", capabilities[0])
	}
}

// TestFileToolsExecute_MissingOperation tests missing operation parameter.
func TestFileToolsExecute_MissingOperation(t *testing.T) {
	tools := NewFileTools()
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
			name:   "empty operation",
			params: map[string]interface{}{"operation": ""},
		},
		{
			name:   "operation is nil",
			params: map[string]interface{}{"operation": nil},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tools.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when operation is missing")
			}
			if !strings.Contains(result.Error, "operation is required") {
				t.Errorf("Error message should mention operation is required, got: %s", result.Error)
			}
		})
	}
}

// TestFileToolsExecute_UnsupportedOperation tests unsupported operation types.
func TestFileToolsExecute_UnsupportedOperation(t *testing.T) {
	tools := NewFileTools()
	ctx := context.Background()

	tests := []struct {
		name      string
		operation string
	}{
		{
			name:      "delete operation",
			operation: "delete",
		},
		{
			name:      "copy operation",
			operation: "copy",
		},
		{
			name:      "move operation",
			operation: "move",
		},
		{
			name:      "random operation",
			operation: "random_operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation": tt.operation,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail for unsupported operation")
			}
			if !strings.Contains(result.Error, "unsupported operation") {
				t.Errorf("Error message should mention unsupported operation, got: %s", result.Error)
			}
		})
	}
}

// TestFileToolsRead_MissingFilePath tests read operation without file path.
func TestFileToolsRead_MissingFilePath(t *testing.T) {
	tools := NewFileTools()
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name: "no file_path",
			params: map[string]interface{}{
				"operation": "read",
			},
		},
		{
			name: "empty file_path",
			params: map[string]interface{}{
				"operation": "read",
				"file_path": "",
			},
		},
		{
			name: "file_path is nil",
			params: map[string]interface{}{
				"operation": "read",
				"file_path": nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tools.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when file_path is missing")
			}
			if !strings.Contains(result.Error, "file_path is required") {
				t.Errorf("Error message should mention file_path is required, got: %s", result.Error)
			}
		})
	}
}

// TestFileToolsRead_FileNotFound tests reading a non-existent file.
func TestFileToolsRead_FileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	ctx := context.Background()

	params := map[string]interface{}{
		"operation": "read",
		"file_path": filepath.Join(tmpDir, "nonexistent", "file.txt"),
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if result.Success {
		t.Error("Execute() should fail when file not found")
	}

	if !strings.Contains(result.Error, "file not found") {
		t.Errorf("Error message should mention file not found, got: %s", result.Error)
	}
}

// TestFileToolsRead_Success tests successful file reading.
func TestFileToolsRead_Success(t *testing.T) {
	ctx := context.Background()

	// Create a temporary file
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5"

	err := os.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	params := map[string]interface{}{
		"operation": "read",
		"file_path": testFile,
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Check result structure
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Result.Data should be a map")
	}

	if data["operation"] != "read" {
		t.Errorf("operation = %v, want 'read'", data["operation"])
	}

	// The operation reports the symlink-resolved secure path, which
	// may differ from the input on platforms where TempDir passes through a
	// symlink (e.g. macOS /var -> /private/var).
	resolvedFile, err := filepath.EvalSymlinks(testFile)
	if err != nil {
		t.Fatalf("Failed to resolve test file: %v", err)
	}
	if data["file_path"] != resolvedFile {
		t.Errorf("file_path = %v, want %s", data["file_path"], resolvedFile)
	}

	lineCount, ok := data["line_count"].(int)
	if !ok || lineCount != 5 {
		t.Errorf("line_count = %v, want 5", data["line_count"])
	}

	totalLines, ok := data["total_lines"].(int)
	if !ok || totalLines != 5 {
		t.Errorf("total_lines = %v, want 5", data["total_lines"])
	}

	resultContent, ok := data["content"].(string)
	if !ok || resultContent != content {
		t.Errorf("content mismatch")
	}
}

// TestFileToolsRead_WithOffsetAndLimit tests reading with offset and limit.
func TestFileToolsRead_WithOffsetAndLimit(t *testing.T) {
	ctx := context.Background()

	// Create a temporary file with multiple lines
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")
	content := strings.Join([]string{
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
		"line6",
		"line7",
		"line8",
		"line9",
		"line10",
	}, "\n")

	err := os.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name          string
		offset        int
		limit         int
		expectedCount int
		expectedFirst string
		expectedLast  string
	}{
		{
			name:          "read from offset 3, limit 3",
			offset:        3,
			limit:         3,
			expectedCount: 3,
			expectedFirst: "line4",
			expectedLast:  "line6",
		},
		{
			name:          "read from offset 0, limit 5",
			offset:        0,
			limit:         5,
			expectedCount: 5,
			expectedFirst: "line1",
			expectedLast:  "line5",
		},
		{
			name:          "read from offset 5, limit 10 (exceeds file)",
			offset:        5,
			limit:         10,
			expectedCount: 5,
			expectedFirst: "line6",
			expectedLast:  "line10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation": "read",
				"file_path": testFile,
				"offset":    tt.offset,
				"limit":     tt.limit,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if !result.Success {
				t.Errorf("Execute() should succeed, got error: %s", result.Error)
			}

			data, ok := result.Data.(map[string]interface{})
			if !ok {
				t.Fatal("Result.Data should be a map")
			}

			lineCount, ok := data["line_count"].(int)
			if !ok || lineCount != tt.expectedCount {
				t.Errorf("line_count = %v, want %d", data["line_count"], tt.expectedCount)
			}

			lines, ok := data["lines"].([]string)
			if !ok || len(lines) != tt.expectedCount {
				t.Errorf("lines length = %v, want %d", len(lines), tt.expectedCount)
			}

			if len(lines) > 0 {
				if lines[0] != tt.expectedFirst {
					t.Errorf("first line = %q, want %q", lines[0], tt.expectedFirst)
				}
				if lines[len(lines)-1] != tt.expectedLast {
					t.Errorf("last line = %q, want %q", lines[len(lines)-1], tt.expectedLast)
				}
			}
		})
	}
}

// TestFileToolsRead_InvalidOffset tests invalid offset values.
func TestFileToolsRead_InvalidOffset(t *testing.T) {
	ctx := context.Background()

	// Create a temporary file
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3"

	err := os.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name      string
		offset    int
		wantError bool
	}{
		{
			name:      "negative offset",
			offset:    -1,
			wantError: false, // Should be clamped to 0
		},
		{
			name:      "offset exceeds file length",
			offset:    100,
			wantError: true,
		},
		{
			name:      "offset equals file length",
			offset:    3,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation": "read",
				"file_path": testFile,
				"offset":    tt.offset,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if tt.wantError {
				if result.Success {
					t.Error("Execute() should fail for invalid offset")
				}
			} else {
				if !result.Success {
					t.Errorf("Execute() should succeed, got error: %s", result.Error)
				}
			}
		})
	}
}

// TestFileToolsWrite_MissingParameters tests write operation without required parameters.
func TestFileToolsWrite_MissingParameters(t *testing.T) {
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name: "no file_path",
			params: map[string]interface{}{
				"operation": "write",
				"content":   "test content",
			},
		},
		{
			name: "no content",
			params: map[string]interface{}{
				"operation": "write",
				"file_path": filepath.Join(tmpDir, "test.txt"),
			},
		},
		{
			name: "content is not string",
			params: map[string]interface{}{
				"operation": "write",
				"file_path": filepath.Join(tmpDir, "test.txt"),
				"content":   12345,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tools.Execute(ctx, tt.params)
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

// TestFileToolsWrite_WriteMode tests write mode (overwrite).
func TestFileToolsWrite_WriteMode(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")

	// Write initial content
	params := map[string]interface{}{
		"operation": "write",
		"file_path": testFile,
		"content":   "initial content",
		"mode":      "write",
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Overwrite with new content
	params = map[string]interface{}{
		"operation": "write",
		"file_path": testFile,
		"content":   "new content",
		"mode":      "write",
	}

	result, err = tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Verify file contains only new content
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	if string(content) != "new content" {
		t.Errorf("File content = %q, want 'new content'", string(content))
	}
}

// TestFileToolsWrite_AppendMode tests append mode.
func TestFileToolsWrite_AppendMode(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")

	// Write initial content
	params := map[string]interface{}{
		"operation": "write",
		"file_path": testFile,
		"content":   "line1\n",
		"mode":      "write",
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Append more content
	params = map[string]interface{}{
		"operation": "write",
		"file_path": testFile,
		"content":   "line2\n",
		"mode":      "append",
	}

	result, err = tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Verify file contains both lines
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	expected := "line1\nline2\n"
	if string(content) != expected {
		t.Errorf("File content = %q, want %q", string(content), expected)
	}
}

// TestFileToolsWrite_CreateDirectory tests creating directory if it doesn't exist.
func TestFileToolsWrite_CreateDirectory(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "subdir", "nested", "test.txt")

	params := map[string]interface{}{
		"operation": "write",
		"file_path": testFile,
		"content":   "test content",
		"mode":      "write",
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Verify file was created
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Error("File should be created")
	}
}

// TestFileToolsList_MissingDirectoryPath tests list operation without directory path.
func TestFileToolsList_MissingDirectoryPath(t *testing.T) {
	tools := NewFileTools()
	ctx := context.Background()

	tests := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name: "no directory_path",
			params: map[string]interface{}{
				"operation": "list",
			},
		},
		{
			name: "empty directory_path",
			params: map[string]interface{}{
				"operation":      "list",
				"directory_path": "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tools.Execute(ctx, tt.params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}
			if result.Success {
				t.Error("Execute() should fail when directory_path is missing")
			}
			if !strings.Contains(result.Error, "directory_path is required") {
				t.Errorf("Error message should mention directory_path is required, got: %s", result.Error)
			}
		})
	}
}

// TestFileToolsList_DirectoryNotFound tests listing a non-existent directory.
func TestFileToolsList_DirectoryNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	ctx := context.Background()

	params := map[string]interface{}{
		"operation":      "list",
		"directory_path": filepath.Join(tmpDir, "nonexistent"),
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if result.Success {
		t.Error("Execute() should fail when directory not found")
	}

	if !strings.Contains(result.Error, "directory not found") {
		t.Errorf("Error message should mention directory not found, got: %s", result.Error)
	}
}

// TestFileToolsList_NotADirectory tests listing a file instead of directory.
func TestFileToolsList_NotADirectory(t *testing.T) {
	ctx := context.Background()

	// Create a temporary file
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))
	testFile := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(testFile, []byte("test"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	params := map[string]interface{}{
		"operation":      "list",
		"directory_path": testFile,
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if result.Success {
		t.Error("Execute() should fail when path is not a directory")
	}

	if !strings.Contains(result.Error, "path is not a directory") {
		t.Errorf("Error message should mention path is not a directory, got: %s", result.Error)
	}
}

// TestFileToolsList_Success tests successful directory listing.
func TestFileToolsList_Success(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory with files
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))

	// Create some files
	_ = os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "file3.go"), []byte("content3"), 0644)

	// Create a subdirectory
	_ = os.Mkdir(filepath.Join(tmpDir, "subdir"), 0755)

	params := map[string]interface{}{
		"operation":      "list",
		"directory_path": tmpDir,
	}

	result, err := tools.Execute(ctx, params)
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
		return
	}

	if !result.Success {
		t.Errorf("Execute() should succeed, got error: %s", result.Error)
	}

	// Check result structure
	data, ok := result.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Result.Data should be a map")
	}

	if data["operation"] != "list" {
		t.Errorf("operation = %v, want 'list'", data["operation"])
	}

	totals, ok := data["totals"].(map[string]interface{})
	if !ok {
		t.Fatal("totals should be a map")
	}

	fileCount, ok := totals["files"].(int)
	if !ok || fileCount != 3 {
		t.Errorf("file count = %v, want 3", totals["files"])
	}

	dirCount, ok := totals["directories"].(int)
	if !ok || dirCount != 1 {
		t.Errorf("directory count = %v, want 1", totals["directories"])
	}
}

// TestFileToolsList_WithPattern tests listing with pattern filter.
func TestFileToolsList_WithPattern(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory with files
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))

	_ = os.WriteFile(filepath.Join(tmpDir, "test_1.txt"), []byte("content1"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "test_2.txt"), []byte("content2"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "other.go"), []byte("content3"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "readme.md"), []byte("content4"), 0644)

	tests := []struct {
		name          string
		pattern       string
		expectedCount int
	}{
		{
			name:          "pattern *.txt",
			pattern:       "*.txt",
			expectedCount: 2,
		},
		{
			name:          "pattern *.go",
			pattern:       "*.go",
			expectedCount: 1,
		},
		{
			name:          "pattern test_*",
			pattern:       "test_*",
			expectedCount: 2,
		},
		{
			name:          "pattern *",
			pattern:       "*",
			expectedCount: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation":      "list",
				"directory_path": tmpDir,
				"pattern":        tt.pattern,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if !result.Success {
				t.Errorf("Execute() should succeed, got error: %s", result.Error)
			}

			data, ok := result.Data.(map[string]interface{})
			if !ok {
				t.Fatal("Result.Data should be a map")
			}

			totals, ok := data["totals"].(map[string]interface{})
			if !ok {
				t.Fatal("totals should be a map")
			}

			fileCount, ok := totals["files"].(int)
			if !ok || fileCount != tt.expectedCount {
				t.Errorf("file count = %v, want %d", totals["files"], tt.expectedCount)
			}
		})
	}
}

// TestFileToolsList_IncludeHidden tests including hidden files.
func TestFileToolsList_IncludeHidden(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory with hidden files
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))

	if err := os.WriteFile(filepath.Join(tmpDir, "normal.txt"), []byte("content1"), 0644); err != nil {
		t.Fatalf("Failed to create normal.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("content2"), 0644); err != nil {
		t.Fatalf("Failed to create .hidden.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("content3"), 0644); err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	tests := []struct {
		name          string
		includeHidden bool
		expectedCount int
	}{
		{
			name:          "exclude hidden",
			includeHidden: false,
			expectedCount: 1,
		},
		{
			name:          "include hidden",
			includeHidden: true,
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation":      "list",
				"directory_path": tmpDir,
				"include_hidden": tt.includeHidden,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if !result.Success {
				t.Errorf("Execute() should succeed, got error: %s", result.Error)
			}

			data, ok := result.Data.(map[string]interface{})
			if !ok {
				t.Fatal("Result.Data should be a map")
			}

			totals, ok := data["totals"].(map[string]interface{})
			if !ok {
				t.Fatal("totals should be a map")
			}

			fileCount, ok := totals["files"].(int)
			if !ok || fileCount != tt.expectedCount {
				t.Errorf("file count = %v, want %d", totals["files"], tt.expectedCount)
			}
		})
	}
}

// TestFileToolsList_Recursive tests recursive directory listing.
func TestFileToolsList_Recursive(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory structure
	tmpDir := t.TempDir()
	tools := NewFileTools(WithAllowedDir(tmpDir))

	// Create files in root
	_ = os.WriteFile(filepath.Join(tmpDir, "root1.txt"), []byte("content1"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "root2.txt"), []byte("content2"), 0644)

	// Create subdirectory with files
	subDir := filepath.Join(tmpDir, "subdir")
	_ = os.Mkdir(subDir, 0755)
	_ = os.WriteFile(filepath.Join(subDir, "sub1.txt"), []byte("content3"), 0644)
	_ = os.WriteFile(filepath.Join(subDir, "sub2.txt"), []byte("content4"), 0644)

	// Create nested subdirectory
	nestedDir := filepath.Join(subDir, "nested")
	_ = os.Mkdir(nestedDir, 0755)
	_ = os.WriteFile(filepath.Join(nestedDir, "nested1.txt"), []byte("content5"), 0644)

	tests := []struct {
		name          string
		recursive     bool
		expectedFiles int
		expectedDirs  int
	}{
		{
			name:          "non-recursive",
			recursive:     false,
			expectedFiles: 2,
			expectedDirs:  1,
		},
		{
			name:          "recursive",
			recursive:     true,
			expectedFiles: 5,
			expectedDirs:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"operation":      "list",
				"directory_path": tmpDir,
				"recursive":      tt.recursive,
			}

			result, err := tools.Execute(ctx, params)
			if err != nil {
				t.Errorf("Execute() unexpected error: %v", err)
				return
			}

			if !result.Success {
				t.Errorf("Execute() should succeed, got error: %s", result.Error)
			}

			data, ok := result.Data.(map[string]interface{})
			if !ok {
				t.Fatal("Result.Data should be a map")
			}

			totals, ok := data["totals"].(map[string]interface{})
			if !ok {
				t.Fatal("totals should be a map")
			}

			fileCount, ok := totals["files"].(int)
			if !ok || fileCount != tt.expectedFiles {
				t.Errorf("file count = %v, want %d", totals["files"], tt.expectedFiles)
			}

			dirCount, ok := totals["directories"].(int)
			if !ok || dirCount != tt.expectedDirs {
				t.Errorf("directory count = %v, want %d", totals["directories"], tt.expectedDirs)
			}
		})
	}
}
