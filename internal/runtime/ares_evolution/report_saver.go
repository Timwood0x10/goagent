package evolution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SaveReport writes the human-readable evolution report to a file.
// Creates parent directories if they don't exist.
// Returns an error if the file cannot be written or context is cancelled.
//
// Args:
//
//	ctx - operation context (checked before I/O).
//	r - the evolution report (must not be nil).
//	path - absolute or relative file path.
//
// Returns:
//
//	error - non-nil if write fails or context cancelled.
func SaveReport(ctx context.Context, r *EvolutionReport, path string) error {
	if r == nil {
		return errors.New("report must not be nil")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create report directory %s: %w", dir, err)
	}

	data := ReportString(r)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		return fmt.Errorf("write report to %s: %w", path, err)
	}

	return nil
}
