package taskfabric

import (
	"log/slog"

	"github.com/Timwood0x10/ares/internal/logger"
)

// log is the module-scoped structured logger for the task fabric.
// Production code in this package must use this instead of the standard
// library log package (library code does not print
// directly; output goes through an injected logger or event sink).
var log *slog.Logger = logger.Module("taskfabric")
