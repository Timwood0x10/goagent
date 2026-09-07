// Package retriever implements intent-driven knowledge retrieval.
//
// Unlike traditional TopK vector search, the Retriever uses the full AKF
// pipeline (Plan → Load → Pipeline → Link → Reduce → Compile) to produce
// LLM-ready context from a natural language query. Embedding is used only
// as a fallback signal, not the primary retrieval mechanism.
//
// Flow:
//
//	Query + Intent
//	    │
//	    ▼
//	KnowledgePlanner (generates requirements from intent)
//	    │
//	    ▼
//	SourceDiscovery (maps requirements to providers)
//	    │
//	    ▼
//	KnowledgeRuntime (Load → Pipeline → Link → Reduce)
//	    │
//	    ▼
//	WorkingGraph
//	    │
//	    ▼
//	Compiler (Prompt / Markdown / JSON / XML / ToolSchema)
//	    │
//	    ▼
//	CompiledContext
//
// Beta: this package is part of the AKG (Autonomous Knowledge Graph)
// subsystem and is currently BETA. The API is not yet stable and may
// change between minor releases. Do not depend on it in production
// without pinning a version. Feedback welcome.
package retriever
