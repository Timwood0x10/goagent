// Package knowledge is the DEPRECATED public alias of
// internal/knowledgeapi (M5). New code MUST import internal/knowledgeapi;
// this package exists only for external consumers and is scheduled for
// removal.
package knowledge

import (
	"github.com/Timwood0x10/ares/internal/knowledgeapi"
)

// ObjectType identifies the type of a knowledge object.
type ObjectType = knowledgeapi.ObjectType

// Evidence records the provenance of a KnowledgeObject.
type Evidence = knowledgeapi.Evidence

// KnowledgeObject is the universal knowledge representation.
//
// Three-layer data structure:
//   - Raw:        Original bytes from the source, preserved for re-distillation.
//   - Normalized: Cleaned, standardized text for embedding and matching.
//   - Summary:    LLM-friendly summary for token-efficient retrieval.
type KnowledgeObject = knowledgeapi.KnowledgeObject

// Representation stores an embedding vector for a KnowledgeObject.
type Representation = knowledgeapi.Representation

// Relation connects two KnowledgeObjects with a named relationship.
type Relation = knowledgeapi.Relation

// WorkingGraph is a task-specific cognitive graph.
// Lifecycle: Build → Consume → Destroy. Never persisted.
type WorkingGraph = knowledgeapi.WorkingGraph

// Query defines filter criteria for KnowledgeStore queries.
type Query = knowledgeapi.Query

// KnowledgeStore is an optional persistence layer for KnowledgeObjects.
// It serves as Cache, Persistence, and History — not a required hop.
// Provider → Pipeline → KnowledgeRuntime bypasses Store entirely.
type KnowledgeStore = knowledgeapi.KnowledgeStore

// Intent describes what knowledge is needed and within what constraints.
type Intent = knowledgeapi.Intent

// Scope defines the boundaries for knowledge retrieval.
type Scope = knowledgeapi.Scope

// Constraint is a key-value filter with an operator.
type Constraint = knowledgeapi.Constraint

// TokenBudget allocates token usage between graph context and LLM reasoning.
type TokenBudget = knowledgeapi.TokenBudget

// Normalizer converts Raw bytes into Normalized text.
type Normalizer = knowledgeapi.Normalizer

// EntityMatcher attempts to match a KnowledgeObject against existing entities.
type EntityMatcher = knowledgeapi.EntityMatcher

// Validator checks whether a merge result is consistent.
type Validator = knowledgeapi.Validator

// Summarizer compresses Normalized text into a concise Summary.
type Summarizer = knowledgeapi.Summarizer

// ResolveResult is the outcome of entity matching.
type ResolveResult = knowledgeapi.ResolveResult

// ValidationResult is the outcome of conflict validation.
type ValidationResult = knowledgeapi.ValidationResult

// Conflict describes a field-level disagreement between sources.
type Conflict = knowledgeapi.Conflict

// KnowledgePipeline orchestrates processing of KnowledgeObjects through
// Normalizer → EntityMatcher → Validator → Summarizer stages.
type KnowledgePipeline = knowledgeapi.KnowledgePipeline

// Object type constants.
const (
	ObjectMemory       = knowledgeapi.ObjectMemory
	ObjectUser         = knowledgeapi.ObjectUser
	ObjectProject      = knowledgeapi.ObjectProject
	ObjectCode         = knowledgeapi.ObjectCode
	ObjectIssue        = knowledgeapi.ObjectIssue
	ObjectCommit       = knowledgeapi.ObjectCommit
	ObjectDecision     = knowledgeapi.ObjectDecision
	ObjectDocument     = knowledgeapi.ObjectDocument
	ObjectToolResult   = knowledgeapi.ObjectToolResult
	ObjectWorkflow     = knowledgeapi.ObjectWorkflow
	ObjectRuntime      = knowledgeapi.ObjectRuntime
	ObjectArchitecture = knowledgeapi.ObjectArchitecture
)

// Built-in relation names.
const (
	RelDependsOn   = knowledgeapi.RelDependsOn
	RelCalls       = knowledgeapi.RelCalls
	RelCauses      = knowledgeapi.RelCauses
	RelFixes       = knowledgeapi.RelFixes
	RelBelongsTo   = knowledgeapi.RelBelongsTo
	RelUses        = knowledgeapi.RelUses
	RelImplements  = knowledgeapi.RelImplements
	RelSimilarTo   = knowledgeapi.RelSimilarTo
	RelGeneratedBy = knowledgeapi.RelGeneratedBy
	RelDecidedBy   = knowledgeapi.RelDecidedBy
	RelSupersedes  = knowledgeapi.RelSupersedes
	RelLearnsFrom  = knowledgeapi.RelLearnsFrom
)

// NewKnowledgePipeline creates a KnowledgePipeline with the given processors.
var NewKnowledgePipeline = knowledgeapi.NewKnowledgePipeline
