// Package knowledge is the DEPRECATED public alias of
// internal/knowledgeapi (M5). New code MUST import internal/knowledgeapi;
// this package exists only for external consumers and is scheduled for
// removal.
package knowledge

import (
	"github.com/Timwood0x10/ares/internal/knowledgeapi"
)

// KnowledgeService is the public API for the AKG.
// It exposes the four core operations of the Knowledge Fabric.
type KnowledgeService = knowledgeapi.KnowledgeService

// Sentinel errors for the knowledge service.
var (
	ErrNilIntent     = knowledgeapi.ErrNilIntent
	ErrEmptyTenantID = knowledgeapi.ErrEmptyTenantID
	ErrNilGraph      = knowledgeapi.ErrNilGraph
)
