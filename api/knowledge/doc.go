// Package knowledge is the DEPRECATED public alias of
// internal/knowledgeapi (M5). New code MUST import internal/knowledgeapi;
// this package exists only for external consumers and is scheduled for
// removal.
//
// This package exposes the core AKF types (KnowledgeObject,
// WorkingGraph, Representation, Relation, Evidence) and the pipeline
// interfaces (Normalizer, EntityMatcher, Validator, Summarizer) to
// external modules. The canonical definitions live in
// internal/knowledgeapi; this file re-exports its public contract via
// type aliases so external callers can construct, process, and query
// knowledge graphs without importing internal packages.
//
// It also exposes the KnowledgeService interface, allowing external
// modules (including AI assistants) to build, query, and compile
// knowledge graphs without importing internal packages.
//
// Key design principle: storage-agnostic. External modules may back
// the knowledge store with any vector database (PostgreSQL pgvector,
// SQLite-vec, Weaviate, Qdrant, Milvus, etc.) by implementing the
// KnowledgeStore interface.
//
// Beta: this package is part of the AKG (Autonomous Knowledge Graph)
// subsystem and is currently BETA. The API is not yet stable and may
// change between minor releases. Do not depend on it in production
// without pinning a version. Feedback welcome.
package knowledge
