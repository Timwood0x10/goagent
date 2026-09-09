package ares_bootstrap

import (
	"github.com/Timwood0x10/ares/internal/llm"
	builtintools "github.com/Timwood0x10/ares/internal/tools/resources/builtin"
	builtin_knowledge "github.com/Timwood0x10/ares/internal/tools/resources/builtin/knowledge"
)

// ToolDepsFromComponents builds the GeneralToolsDeps for
// builtintools.RegisterGeneralTools from the wired bootstrap components, so
// the knowledge / memory / planning tools receive real backends instead of
// nil guards:
//
//   - MemoryMgr        ← comp.Memory (always wired)
//   - KnowledgeSearcher / KnowledgeService ← StoreAdapter over comp.KnowledgeStore
//     (the AKG store; nil when AKG is disabled)
//   - LLMClient        ← comp.LLM.Client, when it is a *llm.Client
//
// KnowledgeRepo remains nil here: it requires a PostgreSQL connection
// (repositories.KnowledgeRepositoryInterface), which bootstrap only creates
// when distillation/storage is configured. The tool backed by that field
// (correct_knowledge) keeps its nil guard until such a repo is wired
// explicitly.
//
// The DistilledRepo field was removed with the distilled_memories schema
// ghost: the repository had zero production constructors, so the table and
// its tools saw zero reads/writes. user_profile runs on the memory manager
// (the path that actually executed); distilled_memory_search was deleted
// with the repository (TODO(tech-debt) 留痕 in RUNTIME.md #9).
func ToolDepsFromComponents(comp *Components) builtintools.GeneralToolsDeps {
	deps := builtintools.GeneralToolsDeps{
		MemoryMgr: comp.Memory,
	}
	if comp.KnowledgeStore != nil {
		adapter := builtin_knowledge.NewStoreAdapter(comp.KnowledgeStore)
		deps.KnowledgeSearcher = adapter
		deps.KnowledgeService = adapter
	}
	if comp.LLM != nil {
		if c, ok := comp.LLM.Client.(*llm.Client); ok {
			deps.LLMClient = c
		}
	}
	return deps
}
