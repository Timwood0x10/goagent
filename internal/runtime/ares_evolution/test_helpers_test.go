package evolution

import (
	"io"
	"log/slog"
)

// discardLogs temporarily redirects the default slog logger to discard output.
// It returns a restore function that must be called via defer.
//
// Use this in tests that intentionally trigger expected WARN/INFO log messages
// (e.g., graceful degradation paths, guardrail warnings) to avoid polluting
// CI output with noise that looks like errors.
func discardLogs() func() {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return func() { slog.SetDefault(old) }
}
