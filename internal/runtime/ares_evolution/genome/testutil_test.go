package genome

import (
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestMain silences slog output for the genome test suite to keep test output
// readable when debugging failures. The GA package emits many Debug/Warn
// log lines during evolve cycles that obscure test assertions.
//
// To inspect logs while developing, set GENOME_TEST_VERBOSE=1 in the
// environment and the original (stderr) default logger is preserved.
func TestMain(m *testing.M) {
	if os.Getenv("GENOME_TEST_VERBOSE") == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}
