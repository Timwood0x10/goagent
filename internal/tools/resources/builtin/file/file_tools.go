package builtin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// Parameter and schema constants
const (
	// Parameter names
	paramOperation     = "operation"
	paramFilePath      = "file_path"
	paramDirectoryPath = "directory_path"
	paramContent       = "content"
	paramMode          = "mode"
	paramOffset        = "offset"
	paramLimit         = "limit"
	paramRecursive     = "recursive"

	// Types
	typeString = "string"
	typeObject = "object"

	// Operations
	operationRead  = "read"
	operationWrite = "write"
	operationList  = "list"

	// Modes
	modeWrite  = "write"
	modeAppend = "append"
)

// FileTools provides file system operations.
//
// SECURITY: All paths are validated against allowedDir. If allowedDir is empty
// the tool refuses to operate — operators MUST configure WithAllowedDir, either
// per-instance or via RegisterGeneralTools.
type FileTools struct {
	*base.BaseTool
	allowedDir string
}

// FileToolsOption is a functional option for FileTools.
type FileToolsOption func(*FileTools)

// WithAllowedDir sets the allowed directory for security checks. The directory
// is resolved to an absolute path with symlinks evaluated at construction time.
func WithAllowedDir(dir string) FileToolsOption {
	return func(ft *FileTools) {
		resolved, err := resolveSecurePath(dir)
		if err != nil {
			// Fall back to the cleaned absolute path so that misconfiguration
			// is still visible instead of silently allowing all paths.
			abs, absErr := filepath.Abs(dir)
			if absErr != nil {
				ft.allowedDir = filepath.Clean(dir)
				return
			}
			ft.allowedDir = filepath.Clean(abs)
			return
		}
		ft.allowedDir = resolved
	}
}

// resolveSecurePath returns the absolute path with symlinks evaluated.
func resolveSecurePath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path %q: %w", path, err)
		}
		path = abs
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("evaluate symlinks for %q: %w", path, err)
	}
	return resolved, nil
}

// isPathAllowed reports whether targetPath is contained within allowedDir
// and returns the symlink-resolved secure path to use for the actual
// filesystem operation. Using the returned path (instead of the caller's
// original path) closes the TOCTOU window between validation and access: a
// symlink swapped in after the check cannot redirect the operation outside
// the allowed directory.
//
// Both paths are resolved to their absolute, symlink-evaluated forms before
// the check. A relative path that escapes the allowed directory (via ".." or
// a symlink) is rejected.
func (t *FileTools) isPathAllowed(targetPath string) (string, error) {
	if t.allowedDir == "" {
		return "", errors.New("file tools have no allowedDir configured; refusing to operate")
	}

	resolvedTarget, err := resolveSecurePath(targetPath)
	if err != nil {
		// For paths that do not yet exist (e.g., a file being created),
		// EvalSymlinks fails. Fall back to evaluating the existing prefix.
		resolvedTarget, err = resolveSecurePathPrefix(targetPath)
		if err != nil {
			return "", fmt.Errorf("resolve target path %q: %w", targetPath, err)
		}
	}

	rel, err := filepath.Rel(t.allowedDir, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("compute relative path from %q to %q: %w", t.allowedDir, resolvedTarget, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path %s is outside allowed directory %s", targetPath, t.allowedDir)
	}
	return resolvedTarget, nil
}

// resolveSecurePathPrefix evaluates symlinks for the longest existing prefix
// of path, then rejoins the remaining non-existent tail. This supports
// validating paths for files that have not been created yet.
func resolveSecurePathPrefix(path string) (string, error) {
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path %q: %w", path, err)
		}
		path = abs
	}
	path = filepath.Clean(path)

	// Walk up the path until we find a component that exists.
	existing := path
	tail := ""
	for {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			if tail == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, tail), nil
		}
		// Step up one directory.
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached root without finding an existing component.
			return path, nil
		}
		tail = filepath.Join(filepath.Base(existing), tail)
		existing = parent
	}
}

// NewFileTools creates a new FileTools tool.
func NewFileTools(opts ...FileToolsOption) *FileTools {
	params := &core.ParameterSchema{
		Type: typeObject,
		Properties: map[string]*core.Parameter{
			paramOperation: {
				Type:        typeString,
				Description: "Operation to perform (read, write, list)",
				Enum:        []interface{}{operationRead, operationWrite, operationList},
			},
			paramFilePath: {
				Type:        typeString,
				Description: "Absolute path to the file",
			},
			paramDirectoryPath: {
				Type:        typeString,
				Description: "Absolute path to the directory (for list operation)",
			},
			paramContent: {
				Type:        typeString,
				Description: "Content to write (for write operation)",
			},
			paramMode: {
				Type:        typeString,
				Description: "Write mode: 'write' (overwrite) or 'append'",
				Default:     modeWrite,
				Enum:        []interface{}{modeWrite, modeAppend},
			},
			paramOffset: {
				Type:        "integer",
				Description: "Starting line number for read (0-based)",
			},
			paramLimit: {
				Type:        "integer",
				Description: "Maximum number of lines to read",
			},
			paramRecursive: {
				Type:        "boolean",
				Description: "List directories recursively",
				Default:     false,
			},
			"include_hidden": {
				Type:        "boolean",
				Description: "Include hidden files (starting with .)",
				Default:     false,
			},
			"pattern": {
				Type:        typeString,
				Description: "Glob pattern to filter files (e.g., '*.go', 'test_*')",
			},
		},
		Required: []string{paramOperation},
	}

	ft := &FileTools{
		BaseTool: base.NewBaseToolWithCapabilities("file_tools", "Read, write, and list files and directories", core.CategorySystem, []core.Capability{core.CapabilityFile}, params),
	}

	for _, opt := range opts {
		opt(ft)
	}

	return ft
}

// Execute performs the file operation.
func (t *FileTools) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	operation, ok := params[paramOperation].(string)
	if !ok || operation == "" {
		return core.NewErrorResult("operation is required"), nil
	}

	switch operation {
	case "read":
		return t.readFile(ctx, params)
	case "write":
		return t.writeFile(ctx, params)
	case "list":
		return t.listFiles(ctx, params)
	default:
		return core.NewErrorResult(fmt.Sprintf("unsupported operation: %s", operation)), nil
	}
}

// readFile reads content from a file.
func (t *FileTools) readFile(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	filePath, ok := params[paramFilePath].(string)
	if !ok || filePath == "" {
		return core.NewErrorResult(paramFilePath + " is required for read operation"), nil
	}

	// Security: validate path is within allowed directory BEFORE any filesystem access.
	// Use the symlink-resolved secure path for the actual read so a symlink
	// swapped in after validation cannot redirect the read.
	safePath, err := t.isPathAllowed(filePath)
	if err != nil {
		return core.NewErrorResult(err.Error()), nil
	}
	filePath = safePath

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Try to find similar files in the same directory
		dir := filepath.Dir(filePath)
		baseName := filepath.Base(filePath)
		suggestions := findSimilarFiles(dir, baseName)

		if len(suggestions) > 0 {
			return core.NewErrorResult(fmt.Sprintf("file not found: %s\n\nDid you mean:\n  - %s", filePath, strings.Join(suggestions, "\n  - "))), nil
		}

		return core.NewErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
	}

	// Read file line-by-line with offset/limit so a large file is never
	// fully loaded into memory when the caller only wants a window.
	offset := getInt(params, paramOffset, 0)
	limit := getInt(params, paramLimit, 0)
	if offset < 0 {
		offset = 0
	}

	f, err := os.Open(filePath) // #nosec G304
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to read file: %v", err)), nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // 10MB max line
	var resultLines []string
	totalLines := 0
	for scanner.Scan() {
		if totalLines >= offset {
			if limit > 0 && len(resultLines) >= limit {
				totalLines++
				continue // keep counting totalLines but skip storing
			}
			resultLines = append(resultLines, scanner.Text())
		}
		totalLines++
	}
	if err := scanner.Err(); err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to read file: %v", err)), nil
	}
	if offset >= totalLines {
		return core.NewErrorResult("offset exceeds file length"), nil
	}

	return core.NewResult(true, map[string]interface{}{
		"operation":   "read",
		"file_path":   filePath,
		"content":     strings.Join(resultLines, "\n"),
		"lines":       resultLines,
		"line_count":  len(resultLines),
		"total_lines": totalLines,
		"offset":      offset,
		"limit":       limit,
	}), nil
}

// writeFile writes content to a file.
func (t *FileTools) writeFile(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	filePath, ok := params[paramFilePath].(string)
	if !ok || filePath == "" {
		return core.NewErrorResult(paramFilePath + " is required for write operation"), nil
	}

	content, ok := params[paramContent].(string)
	if !ok {
		return core.NewErrorResult(paramContent + " is required for write operation"), nil
	}

	// Security: validate path is within allowed directory BEFORE any filesystem access.
	// Use the symlink-resolved secure path for the actual write so a symlink
	// swapped in after validation cannot redirect the write.
	safePath, err := t.isPathAllowed(filePath)
	if err != nil {
		return core.NewErrorResult(err.Error()), nil
	}
	filePath = safePath

	// Get write mode
	mode := getString(params, paramMode)
	if mode == "" {
		mode = modeWrite
	}

	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to create directory: %v", err)), nil
	}

	var writeErr error
	if mode == modeAppend {
		// Append mode
		file, openErr := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600) // #nosec G304
		if openErr != nil {
			return core.NewErrorResult(fmt.Sprintf("failed to open file: %v", openErr)), nil
		}
		defer func() {
			if closeErr := file.Close(); closeErr != nil {
				log.Error("failed to close file: ", "error", closeErr)
			}
		}()

		_, writeErr = file.WriteString(content)
		if writeErr != nil {
			return core.NewErrorResult(fmt.Sprintf("failed to write to file: %v", writeErr)), nil
		}
	} else {
		// Write mode (overwrite)
		writeErr = os.WriteFile(filePath, []byte(content), 0600)
	}

	if writeErr != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to write file: %v", writeErr)), nil
	}

	return core.NewResult(true, map[string]interface{}{
		paramOperation:  operationWrite,
		paramFilePath:   filePath,
		paramMode:       mode,
		"bytes_written": len(content),
		"success":       true,
	}), nil
}

// listFiles lists files and directories in a directory.
//
//nolint:gocyclo // Complex file listing with multiple filter conditions
func (t *FileTools) listFiles(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	dirPath, ok := params[paramDirectoryPath].(string)
	if !ok || dirPath == "" {
		return core.NewErrorResult(paramDirectoryPath + " is required for list operation"), nil
	}

	// Convert relative path to absolute path
	if !filepath.IsAbs(dirPath) {
		absPath, err := filepath.Abs(dirPath)
		if err != nil {
			return core.NewErrorResult(fmt.Sprintf("failed to resolve absolute path: %v", err)), nil
		}
		dirPath = absPath
	}

	dirPath = filepath.Clean(dirPath)

	// Security: validate path is within allowed directory BEFORE any filesystem access.
	// Use the symlink-resolved secure path for the actual listing so a
	// symlink swapped in after validation cannot redirect the walk.
	safePath, err := t.isPathAllowed(dirPath)
	if err != nil {
		return core.NewErrorResult(err.Error()), nil
	}
	dirPath = safePath

	// Check if directory exists
	info, err := os.Stat(dirPath)
	if err != nil {
		if os.IsNotExist(err) {
			return core.NewErrorResult(fmt.Sprintf("directory not found: %s", dirPath)), nil
		}
		return core.NewErrorResult(fmt.Sprintf("failed to access directory: %v", err)), nil
	}

	if !info.IsDir() {
		return core.NewErrorResult("path is not a directory"), nil
	}

	recursive := getBool(params, "recursive", false)
	includeHidden := getBool(params, "include_hidden", false)
	pattern := getString(params, "pattern")

	// Collect entries
	var files []map[string]interface{}
	var dirs []map[string]interface{}

	if recursive {
		// Walk directory recursively
		err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(dirPath, path)
			if err != nil {
				return err
			}

			// Skip root directory
			if relPath == "." {
				return nil
			}

			// Check hidden files
			if !includeHidden {
				base := filepath.Base(path)
				if strings.HasPrefix(base, ".") {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}

			// Check pattern
			if pattern != "" {
				matched, err := filepath.Match(pattern, filepath.Base(path))
				if err != nil {
					return err
				}
				if !matched {
					if info.IsDir() {
						return nil
					}
					return nil
				}
			}

			entry := map[string]interface{}{
				"path":     path,
				"rel_path": relPath,
				"name":     info.Name(),
				"size":     info.Size(),
				"mode":     info.Mode().String(),
				"modified": info.ModTime(),
				"is_dir":   info.IsDir(),
			}

			if info.IsDir() {
				dirs = append(dirs, entry)
			} else {
				files = append(files, entry)
			}

			return nil
		})
	} else {
		// List only top-level
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return core.NewErrorResult(fmt.Sprintf("failed to read directory: %v", err)), nil
		}

		for _, entry := range entries {
			name := entry.Name()

			// Check hidden files
			if !includeHidden && strings.HasPrefix(name, ".") {
				continue
			}

			// Check pattern
			if pattern != "" {
				matched, err := filepath.Match(pattern, name)
				if err != nil {
					return core.NewErrorResult(fmt.Sprintf("invalid pattern: %v", err)), nil
				}
				if !matched {
					continue
				}
			}

			info, err := entry.Info()
			if err != nil {
				continue
			}

			fullPath := filepath.Join(dirPath, name)
			entryMap := map[string]interface{}{
				"path":     fullPath,
				"name":     name,
				"size":     info.Size(),
				"mode":     info.Mode().String(),
				"modified": info.ModTime(),
				"is_dir":   entry.IsDir(),
			}

			if entry.IsDir() {
				dirs = append(dirs, entryMap)
			} else {
				files = append(files, entryMap)
			}
		}
	}

	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to list directory: %v", err)), nil
	}

	return core.NewResult(true, map[string]interface{}{
		"operation":   "list",
		"directory":   dirPath,
		"files":       files,
		"directories": dirs,
		"totals": map[string]interface{}{
			"directories": len(dirs),
			"files":       len(files),
		},
	}), nil
}

// getBool safely gets a boolean parameter.
func getBool(params map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := params[key].(bool); ok {
		return val
	}
	return defaultVal
}

// getString safely gets a string parameter.
func getString(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

// getInt safely gets an int parameter.
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

// findSimilarFiles finds files with similar names in the same directory.
func findSimilarFiles(dir, baseName string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Extract base name without extension
	nameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	var suggestions []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		entryName := entry.Name()
		entryWithoutExt := strings.TrimSuffix(entryName, filepath.Ext(entryName))

		// Check if base name matches (case insensitive)
		if strings.EqualFold(nameWithoutExt, entryWithoutExt) {
			suggestions = append(suggestions, filepath.Join(dir, entryName))
		}
	}

	return suggestions
}
