// Package knowledgeapi provides the canonical type and interface domain for
// the ARES Knowledge Fabric (AKF) — the Agent Knowledge Graph (AKG).
//
// This package exposes the core AKF types (KnowledgeObject, WorkingGraph,
// Representation, Relation, Evidence) and the pipeline interfaces
// (Normalizer, EntityMatcher, Validator, Summarizer), re-exported from
// internal/knowledge via type aliases, plus the KnowledgeService interface
// and its sentinel errors defined here. The internal implementation lives
// in internal/knowledge; this package is the type/interface domain that
// adapters (internal/knowledge/service) programme against, which keeps the
// implementation domain free of a back-edge import.
//
// Key design principle: storage-agnostic. Modules may back the knowledge
// store with any vector database (PostgreSQL pgvector, SQLite-vec, Weaviate,
// Qdrant, Milvus, etc.) by implementing the KnowledgeStore interface.
//
// This package holds the canonical definitions previously exposed as
// api/knowledge (M5 migration); api/knowledge is now a pure forwarding layer.
//
// Beta: this package is part of the AKG (Autonomous Knowledge Graph)
// subsystem and is currently BETA. The API is not yet stable and may
// change between minor releases. Do not depend on it in production
// without pinning a version. Feedback welcome.
package knowledgeapi

import (
	"github.com/Timwood0x10/ares/internal/knowledge"
)

// ObjectType identifies the type of a knowledge object.
type ObjectType = knowledge.ObjectType

// Evidence records the provenance of a KnowledgeObject.
type Evidence = knowledge.Evidence

// KnowledgeObject is the universal knowledge representation.
//
// Three-layer data structure:
//   - Raw:        Original bytes from the source, preserved for re-distillation.
//   - Normalized: Cleaned, standardized text for embedding and matching.
//   - Summary:    LLM-friendly summary for token-efficient retrieval.
type KnowledgeObject = knowledge.KnowledgeObject

// Representation stores an embedding vector for a KnowledgeObject.
type Representation = knowledge.Representation

// Relation connects two KnowledgeObjects with a named relationship.
type Relation = knowledge.Relation

// WorkingGraph is a task-specific cognitive graph.
// Lifecycle: Build → Consume → Destroy. Never persisted.
type WorkingGraph = knowledge.WorkingGraph

// Query defines filter criteria for KnowledgeStore queries.
type Query = knowledge.Query

// KnowledgeStore is an optional persistence layer for KnowledgeObjects.
// It serves as Cache, Persistence, and History — not a required hop.
// Provider → Pipeline → KnowledgeRuntime bypasses Store entirely.
type KnowledgeStore = knowledge.KnowledgeStore

// Intent describes what knowledge is needed and within what constraints.
type Intent = knowledge.Intent

// Scope defines the boundaries for knowledge retrieval.
type Scope = knowledge.Scope

// Constraint is a key-value filter with an operator.
type Constraint = knowledge.Constraint

// TokenBudget allocates token usage between graph context and LLM reasoning.
type TokenBudget = knowledge.TokenBudget

// Normalizer converts Raw bytes into Normalized text.
type Normalizer = knowledge.Normalizer

// EntityMatcher attempts to match a KnowledgeObject against existing entities.
type EntityMatcher = knowledge.EntityMatcher

// Validator checks whether a merge result is consistent.
type Validator = knowledge.Validator

// Summarizer compresses Normalized text into a concise Summary.
type Summarizer = knowledge.Summarizer

// ResolveResult is the outcome of entity matching.
type ResolveResult = knowledge.ResolveResult

// ValidationResult is the outcome of conflict validation.
type ValidationResult = knowledge.ValidationResult

// Conflict describes a field-level disagreement between sources.
type Conflict = knowledge.Conflict

// KnowledgePipeline orchestrates processing of KnowledgeObjects through
// Normalizer → EntityMatcher → Validator → Summarizer stages.
type KnowledgePipeline = knowledge.KnowledgePipeline

// Object type constants.
const (
	ObjectMemory       = knowledge.ObjectMemory
	ObjectUser         = knowledge.ObjectUser
	ObjectProject      = knowledge.ObjectProject
	ObjectCode         = knowledge.ObjectCode
	ObjectIssue        = knowledge.ObjectIssue
	ObjectCommit       = knowledge.ObjectCommit
	ObjectDecision     = knowledge.ObjectDecision
	ObjectDocument     = knowledge.ObjectDocument
	ObjectToolResult   = knowledge.ObjectToolResult
	ObjectWorkflow     = knowledge.ObjectWorkflow
	ObjectRuntime      = knowledge.ObjectRuntime
	ObjectArchitecture = knowledge.ObjectArchitecture
)

// Built-in relation names.
const (
	RelDependsOn   = knowledge.RelDependsOn
	RelCalls       = knowledge.RelCalls
	RelCauses      = knowledge.RelCauses
	RelFixes       = knowledge.RelFixes
	RelBelongsTo   = knowledge.RelBelongsTo
	RelUses        = knowledge.RelUses
	RelImplements  = knowledge.RelImplements
	RelSimilarTo   = knowledge.RelSimilarTo
	RelGeneratedBy = knowledge.RelGeneratedBy
	RelDecidedBy   = knowledge.RelDecidedBy
	RelSupersedes  = knowledge.RelSupersedes
	RelLearnsFrom  = knowledge.RelLearnsFrom
)

// NewKnowledgePipeline creates a KnowledgePipeline with the given processors.
var NewKnowledgePipeline = knowledge.NewKnowledgePipeline
