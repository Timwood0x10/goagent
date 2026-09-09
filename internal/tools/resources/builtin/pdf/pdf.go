package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

const (
	paramOperation       = "operation"
	paramFilePath        = "file_path"
	operationExtractText = "extract_text"
)

// PDFTool provides PDF document processing operations.
type PDFTool struct {
	*base.BaseTool
	allowedDir string
}

// PDFToolOption is a functional option for PDFTool.
type PDFToolOption func(*PDFTool)

// WithAllowedDir sets the directory that PDFTool may read files from. The
// directory is resolved to an absolute path at configuration time. When left
// unset (or passed an empty string), PDFTool denies all file access
// (deny-by-default): an empty allowedDir no longer means "any path", which
// was an arbitrary-file-read + path-existence oracle (REVIEW #30).
func WithAllowedDir(dir string) PDFToolOption {
	return func(pt *PDFTool) {
		if dir == "" {
			pt.allowedDir = ""
			return
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			pt.allowedDir = filepath.Clean(dir)
			return
		}
		pt.allowedDir = filepath.Clean(abs)
	}
}

// NewPDFTool creates a new PDFTool.
func NewPDFTool(opts ...PDFToolOption) *PDFTool {
	params := &core.ParameterSchema{
		Type: "object",
		Properties: map[string]*core.Parameter{
			paramOperation: {
				Type:        "string",
				Description: "Operation: extract_text (extract all text from PDF)",
				Enum:        []interface{}{operationExtractText},
			},
			paramFilePath: {
				Type:        "string",
				Description: "Path to the PDF file",
			},
		},
		Required: []string{paramOperation, paramFilePath},
	}

	pt := &PDFTool{
		BaseTool: base.NewBaseToolWithCapabilities("pdf_tool",
			"Extract text content from PDF files. Supports text extraction from any PDF document.",
			core.CategoryCore, []core.Capability{core.CapabilityText}, params),
	}
	for _, opt := range opts {
		opt(pt)
	}
	return pt
}

// Execute performs the PDF operation.
func (t *PDFTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	operation, ok := params[paramOperation].(string)
	if !ok || operation == "" {
		return core.NewErrorResult("operation is required"), nil
	}

	filePath, ok := params[paramFilePath].(string)
	if !ok || filePath == "" {
		return core.NewErrorResult("file_path is required"), nil
	}

	switch operation {
	case operationExtractText:
		return t.extractText(ctx, filePath)
	default:
		return core.NewErrorResult(fmt.Sprintf("unsupported operation: %s", operation)), nil
	}
}

func (t *PDFTool) extractText(ctx context.Context, filePath string) (core.Result, error) {
	// Deny-by-default: with no allowed directory configured the tool refuses
	// every read. This closes the arbitrary-PDF-read and path-existence
	// oracle that the old empty-allowedDir "legacy" mode allowed (REVIEW #30).
	if t.allowedDir == "" {
		return core.NewErrorResult("access denied: PDF tool has no allowed directory configured"), nil
	}
	// Enforce the sandbox boundary: the requested path must resolve inside
	// the allowed directory. Without this check the PDF tool could read
	// arbitrary files, bypassing the file tools sandbox.
	//
	// Resolve symlinks on both paths before the containment check so a
	// symlink inside the allowed dir cannot point outside it. The resolved
	// absPath is reused for the actual open below, closing the TOCTOU window
	// where a symlink is swapped between check and read.
	absAllowed, err := filepath.Abs(t.allowedDir)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to resolve allowed directory: %v", err)), nil
	}
	if resolved, rErr := filepath.EvalSymlinks(absAllowed); rErr == nil {
		absAllowed = resolved
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to resolve file path: %v", err)), nil
	}
	if resolved, rErr := filepath.EvalSymlinks(absPath); rErr == nil {
		absPath = resolved
	}
	rel, err := filepath.Rel(absAllowed, absPath)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to resolve path relative to allowed directory: %v", err)), nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return core.NewErrorResult(fmt.Sprintf("access denied: path %s is outside allowed directory %s", filePath, t.allowedDir)), nil
	}

	// Verify file exists and is readable — use the RESOLVED path so the
	// containment check and the actual read target the same file.
	info, err := os.Stat(absPath)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("cannot access file: %v", err)), nil
	}
	if info.IsDir() {
		return core.NewErrorResult("path is a directory, not a file"), nil
	}

	f, r, err := pdf.Open(absPath)
	if err != nil {
		return core.NewErrorResult(fmt.Sprintf("failed to open PDF: %v", err)), nil
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			log.Error("pdf: close file error", "path", filePath, "error", cerr)
		}
	}()

	var text string
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		pageText, err := page.GetPlainText(nil)
		if err != nil {
			log.Warn("pdf: page text extraction failed", "page", i, "error", err)
			continue
		}
		text += fmt.Sprintf("--- Page %d ---\n%s\n", i, pageText)
	}

	return core.NewResult(true, map[string]interface{}{
		paramOperation: operationExtractText,
		paramFilePath:  filePath,
		"pages":        r.NumPage(),
		"text":         text,
		"char_count":   len([]rune(text)),
	}), nil
}

// IsIdempotent returns true since PDF reading has no side effects.
func (t *PDFTool) IsIdempotent() bool { return true }
