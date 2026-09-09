// Package experience is the DEPRECATED public alias of internal/llmexp (M5).
// New code MUST import internal/llmexp; this package exists only for
// external consumers and is scheduled for removal.
package experience

import "github.com/Timwood0x10/ares/internal/llmexp"

// ExperienceRepository defines the interface for experience storage and
// retrieval. It is the storage-agnostic contract that decouples the
// distillation pipeline from any specific vector database.
type ExperienceRepository = llmexp.ExperienceRepository
