package agentsyscall

import (
	"log/slog"

	"github.com/Timwood0x10/ares/internal/logger"
)

// log is the module-scoped structured logger for the agent syscall layer.
// Production code in this package must use this instead of the standard
// library log package.
var log *slog.Logger = logger.Module("agentsyscall")
