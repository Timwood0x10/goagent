// Package compat is the ARES Compatibility Layer — the ecosystem entry point.
//
// # Boundary (read before adding anything here)
//
// This layer exists to adapt third-party ecosystem components into ARES. It is
// NOT part of the kernel and must not grow kernel dependencies. Two rules:
//
//  1. compat may import internal/ (it adapts ARES to the outside world).
//     internal/ must NOT import compat, with the single documented exception
//     below.
//  2. New ARES capabilities do not belong here. They belong in internal/ with
//     compat adapters added only when a third party needs to plug in.
//
// # What is actually wired in production (audited 2026-09-01; updated
// 2026-09-09)
//
// ZERO production consumers. The last one —
// internal/ares_bootstrap/provide_llm.go's compat.RegisterLLM call — was
// removed on 2026-09-09: the registration was write-only (the registry had
// no readers anywhere in this repo, examples included), so bootstrap was
// populating a registry nobody queried.
//
// Every subtree has NO production reference:
//
//	compat/loader/    (markdown, pdf, html)
//	compat/protocol/  (mcp, openai_api)
//	compat/tool/
//	compat/vector/    (pgvector)
//
// They are the 0.2.x ecosystem surface, kept because removing exported packages
// is a breaking change and 0.3.1 is a patch release. They are reachable by
// third-party code and by examples, so they are not dead code in the
// delete-on-sight sense — but nothing in ARES itself calls them.
//
// TODO(tech-debt): re-evaluate deletion of the unreferenced subtrees in 0.4.x.
// The decision needs data this repo does not have (whether any downstream user
// imports them), so it is a release-note question, not a code question. Until
// then: do not add new dependencies on them, and do not let them grow.
//
// # ARES is "evolution included", not "batteries included"
//
// ARES officially maintains only the 20% of components that 80% of users need
// (OpenAI, Ollama, pgvector, Markdown/PDF, MCP); everything else is a
// third-party plugin registered via the helpers in this package.
//
// Directory layout:
//
//	compat/
//	    llm/        — LLM provider adapters (openai, ollama, anthropic, …)
//	    vector/     — Vector store adapters (pgvector, chroma, qdrant, …)
//	    loader/     — Document loaders (markdown, pdf, html, …)
//	    protocol/   — Wire protocol adapters (openai_api, mcp, http)
//	    tool/       — Tool registry and builtin tool adapters
//
// Registration entry points:
//
//	compat.RegisterLLM(name, factory)
//	compat.RegisterVector(name, factory)
//	compat.RegisterLoader(name, factory)
//	compat.RegisterProtocol(name, factory)
//	compat.RegisterTool(name, factory)
//
// Each subsystem keeps its own typed registry under its sub-package; the
// top-level helpers in compat.go delegate to the per-subsystem registries.
//
// # Registry semantics
//
// Registration is first-writer-wins: a duplicate name is rejected rather than
// silently overwritten, so a late-loading plugin cannot hijack the official
// "openai" or "ollama" adapter. compat.Default is process-wide, written during
// bootstrap and read from runtime components, so every registry is guarded by
// an RWMutex.
//
// Lookup misses return each sub-package's own ErrNotFound sentinel
// (compat/llm.ErrNotFound and friends), matchable with errors.Is. The
// package-level compat.ErrNotFound / compat.ErrAlreadyRegistered below are the
// generic equivalents; sub-packages deliberately define their own so the
// message names the subsystem.
package compat
