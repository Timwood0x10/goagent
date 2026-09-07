package ares_skills

import (
	"log/slog"

	"github.com/Timwood0x10/ares/internal/logger"
)

// log is the module-scoped structured logger for the skills catalog.
// Production code in this package must use this instead of the standard
// library log package (code_rules_v2 §9.1).
var log *slog.Logger = logger.Module("ares_skills")
