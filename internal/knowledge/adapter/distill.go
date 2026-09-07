package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Timwood0x10/ares/api/embedding"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

// ConversationDistiller is the minimal interface for distilling conversations
// into Memory objects. The existing distillation.Distiller implements this.
type ConversationDistiller interface {
	DistillConversation(ctx context.Context, conversationID string, messages []distillation.Message, tenantID, userID string) ([]distillation.Memory, error)
}

// DistillBridge connects the Memory Distillation pipeline to AKF.
// It runs conversations through the existing Distiller, converts the
// resulting Memory objects to KnowledgeObjects, processes them through
// the AKF KnowledgePipeline, and persists them to the KnowledgeStore.
//
// The 0.2.9 write loop adds four phases before Save:
//   - relation extraction (rule-based, non-LLM),
//   - embedding + dedup (marks superseded duplicates),
//   - quality-gate scoring (sets Quality and Confidence),
//   - lifecycle promotion (candidate → active when above MinFinalScore).
type DistillBridge struct {
	distiller ConversationDistiller
	pipeline  *knowledge.KnowledgePipeline
	store     knowledge.KnowledgeStore
	// emb is optional; when nil, embedding and dedup are skipped entirely.
	emb embedding.EmbeddingService
	// gate configures the quality gate: scoring weights, dedup threshold,
	// and per-ingest caps. Always non-nil after construction.
	gate knowledge.QualityGateConfig
	// extractor extracts rule-based Relations from each object's text.
	// Always non-nil after construction.
	extractor *knowledge.RelationExtractor
	namespace string
	// model is the embedding model name recorded on each Representation
	// and KnowledgeObject.EmbeddingModel. Empty means no model recorded.
	model string
	// memoryAdapter converts distillation Memories into KnowledgeObjects,
	// capping Memory.Content length at the configured value. Always
	// non-nil after construction.
	memoryAdapter *MemoryAdapter
}

// DistillBridgeOption configures a DistillBridge.
type DistillBridgeOption func(*DistillBridge)

// WithMemoryMaxContentLen overrides the cap on Memory.Content length when
// converting distilled memories into KnowledgeObjects. Pass 0 or a
// negative value to keep DefaultMaxMemoryContentLen.
func WithMemoryMaxContentLen(n int) DistillBridgeOption {
	return func(b *DistillBridge) {
		b.memoryAdapter = NewMemoryAdapter(n)
	}
}

// NewDistillBridge creates a bridge that connects the Memory Distillation
// pipeline to the AKF KnowledgeObject system. It delegates to
// NewDistillBridgeWithGate with the 0.2.9 defaults: no embedding service
// (skips vector + dedup), the default quality gate, and the default
// relation extractor.
//
// Args:
//   - distiller: existing Memory Distiller that produces Memory objects.
//   - pipeline: AKF KnowledgePipeline (Normalizer → Resolver → Summarizer).
//     Pass nil to skip pipeline processing.
//   - store: AKF KnowledgeStore for persisting the resulting KnowledgeObjects.
//   - namespace: namespace assigned to all produced KnowledgeObjects.
//   - opts: optional DistillBridgeOption overrides (e.g. WithMemoryMaxContentLen).
func NewDistillBridge(
	distiller ConversationDistiller,
	pipeline *knowledge.KnowledgePipeline,
	store knowledge.KnowledgeStore,
	namespace string,
	opts ...DistillBridgeOption,
) *DistillBridge {
	return NewDistillBridgeWithGate(
		distiller,
		pipeline,
		store,
		nil, // emb: nil skips embedding + dedup
		knowledge.DefaultQualityGateConfig(),
		knowledge.NewRelationExtractor(),
		namespace,
		"", // model: empty means no model recorded
		opts...,
	)
}

// NewDistillBridgeWithGate creates a bridge with the full 0.2.9 write loop:
// relation extraction, embedding + dedup, quality-gate scoring, and
// lifecycle promotion before Save.
//
// Args:
//   - distiller: existing Memory Distiller that produces Memory objects.
//   - pipeline: AKF KnowledgePipeline; pass nil to skip pipeline processing.
//   - store: AKF KnowledgeStore for persisting the resulting KnowledgeObjects.
//   - emb: EmbeddingService for vector embedding; nil skips embedding + dedup.
//   - gate: QualityGateConfig for scoring, dedup threshold, and caps.
//   - extractor: RelationExtractor; nil defaults to NewRelationExtractor().
//   - namespace: namespace assigned to all produced KnowledgeObjects.
//   - model: embedding model name; empty means no model recorded.
//   - opts: optional DistillBridgeOption overrides (e.g. WithMemoryMaxContentLen).
func NewDistillBridgeWithGate(
	distiller ConversationDistiller,
	pipeline *knowledge.KnowledgePipeline,
	store knowledge.KnowledgeStore,
	emb embedding.EmbeddingService,
	gate knowledge.QualityGateConfig,
	extractor *knowledge.RelationExtractor,
	namespace, model string,
	opts ...DistillBridgeOption,
) *DistillBridge {
	if extractor == nil {
		extractor = knowledge.NewRelationExtractor()
	}
	b := &DistillBridge{
		distiller:     distiller,
		pipeline:      pipeline,
		store:         store,
		emb:           emb,
		gate:          gate,
		extractor:     extractor,
		namespace:     namespace,
		model:         model,
		memoryAdapter: NewMemoryAdapter(DefaultMaxMemoryContentLen),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	// Guard against an option that left the adapter nil.
	if b.memoryAdapter == nil {
		b.memoryAdapter = NewMemoryAdapter(DefaultMaxMemoryContentLen)
	}
	return b
}

// DistillConversation runs a conversation through the full distillation
// pipeline and persists the results as KnowledgeObjects.
//
// Steps:
//  1. Distill: uses the existing Distiller to extract Memories from messages.
//  2. Convert: maps each Memory to a KnowledgeObject via MemoryAdapter.FromMemory.
//  3. Pipeline: runs each KnowledgeObject through Normalizer → Resolver → Summarizer.
//     3.5. Relation extraction: extracts rule-based Relations from each object.
//     3.6. Embedding + dedup: embeds each object, stores the Representation, and
//     marks superseded duplicates (only when emb and store are non-nil; dedup
//     additionally requires gate.EnableDedup).
//     3.7. Quality gate: scores each non-superseded object and sets its
//     Confidence; EmbeddingModel is recorded when model is non-empty.
//  4. Persist: saves all objects to the Store, then promotes candidates
//     whose Confidence >= MinFinalScore to StatusActive (best-effort).
//
// MaxFactsPerIngest caps the number of objects that enter phase 3.5+; when
// exceeded the slice is truncated before the expensive phases run.
//
// Returns the saved KnowledgeObjects or an error if any step fails.
func (b *DistillBridge) DistillConversation(
	ctx context.Context,
	conversationID string,
	messages []distillation.Message,
	tenantID string,
	userID string,
) ([]*knowledge.KnowledgeObject, error) {
	if b.distiller == nil {
		return nil, errors.New("distill bridge: distiller is nil")
	}
	if len(messages) == 0 {
		return nil, errors.New("distill bridge: no messages to distill")
	}

	// Step 1: run the existing Memory Distiller.
	memories, err := b.distiller.DistillConversation(ctx, conversationID, messages, tenantID, userID)
	if err != nil {
		return nil, fmt.Errorf("distill bridge: distill conversation %q: %w", conversationID, err)
	}
	if len(memories) == 0 {
		return nil, nil
	}

	// Step 2: convert Memory → KnowledgeObject.
	pointers := make([]*distillation.Memory, len(memories))
	for i := range memories {
		pointers[i] = &memories[i]
	}
	objects := b.memoryAdapter.FromMemories(pointers, b.namespace)
	if len(objects) == 0 {
		return nil, nil
	}

	// Step 3: run through AKF KnowledgePipeline.
	if b.pipeline != nil {
		processed := make([]*knowledge.KnowledgeObject, 0, len(objects))
		for _, obj := range objects {
			result, pErr := b.pipeline.Process(ctx, obj)
			if pErr != nil {
				return nil, fmt.Errorf("distill bridge: pipeline process %q: %w", obj.ID, pErr)
			}
			if result != nil {
				processed = append(processed, result)
			}
		}
		objects = processed
	}

	// Cap the ingest before the expensive phases (embedding, dedup, scoring).
	if b.gate.MaxFactsPerIngest > 0 && len(objects) > b.gate.MaxFactsPerIngest {
		objects = objects[:b.gate.MaxFactsPerIngest]
	}

	// extract rule-based Relations from each object's text.
	// Relations feed the quality gate (ExtractionScore boost) and downstream
	// graph construction.
	b.extractRelations(objects)

	// embedding + dedup. Requires both emb and store; dedup
	// additionally requires gate.EnableDedup. Superseded objects skip the
	// quality gate during scoring.
	b.embedAndDedup(ctx, objects)

	// quality gate. Score each non-superseded object; the
	// resulting Quality and Confidence drive the promote decision at persist.
	b.scoreQuality(objects)

	// persist to KnowledgeStore and promote qualifying candidates.
	return objects, b.persistAndPromote(ctx, objects)
}

// extractRelations populates obj.Relations for each object
// using the rule-based RelationExtractor. Relations feed the quality gate
// (ExtractionScore boost) and downstream graph construction.
func (b *DistillBridge) extractRelations(objects []*knowledge.KnowledgeObject) {
	for _, obj := range objects {
		obj.Relations = b.extractor.Extract(obj)
	}
}

// embedAndDedup embeds each object, stores the
// Representation, and marks superseded duplicates. Requires both emb and
// store; dedup additionally requires gate.EnableDedup. Superseded objects
// skip the quality gate. Does nothing when emb or store is nil.
func (b *DistillBridge) embedAndDedup(ctx context.Context, objects []*knowledge.KnowledgeObject) {
	if b.emb == nil || b.store == nil {
		return
	}
	for _, obj := range objects {
		text := obj.Normalized
		if text == "" {
			continue
		}
		vecF64, eErr := b.emb.EmbedWithPrefix(ctx, text, "passage:")
		if eErr != nil {
			slog.Warn("distill bridge: embed object",
				"object_id", obj.ID, "error", eErr)
			continue
		}
		vec := toFloat32(vecF64)
		rep := &knowledge.Representation{
			ID:        "rep_" + obj.ID,
			ObjectID:  obj.ID,
			Model:     b.model,
			Dimension: len(vec),
			Vector:    vec,
		}
		if rErr := b.store.SaveRepresentation(ctx, rep); rErr != nil {
			// best-effort: representation is not required for Save.
			slog.Warn("distill bridge: save representation",
				"object_id", obj.ID, "error", rErr)
		}
		if !b.gate.EnableDedup {
			continue
		}
		dup, dErr := knowledge.FindDuplicate(ctx, b.store, vec, b.model, b.gate.DedupThreshold)
		if dErr != nil {
			slog.Warn("distill bridge: find duplicate",
				"object_id", obj.ID, "error", dErr)
			continue
		}
		if dup != nil {
			obj.Status = knowledge.StatusSuperseded
		}
	}
}

// scoreQuality scores each non-superseded object and sets
// its Status, Quality, Confidence, and EmbeddingModel. Superseded objects
// skip the gate; the resulting Quality and Confidence drive the promote
// decision at persist.
func (b *DistillBridge) scoreQuality(objects []*knowledge.KnowledgeObject) {
	for _, obj := range objects {
		if obj.Status == knowledge.StatusSuperseded {
			continue
		}
		obj.Status = knowledge.StatusCandidate
		q := b.gate.Evaluate(obj)
		obj.Quality = q
		obj.Confidence = b.gate.ComputeFinal(q)
		if b.model != "" {
			obj.EmbeddingModel = b.model
		}
	}
}

// persistAndPromote saves all objects to the Store and
// promotes candidates whose Confidence >= MinFinalScore to StatusActive
// (best-effort; promotion failures are logged but do not roll back the Save).
// Returns a wrapped error if Save fails. Does nothing when store is nil.
func (b *DistillBridge) persistAndPromote(ctx context.Context, objects []*knowledge.KnowledgeObject) error {
	if b.store == nil {
		return nil
	}
	if err := b.store.Save(ctx, objects...); err != nil {
		return fmt.Errorf("distill bridge: save to store: %w", err)
	}
	for _, obj := range objects {
		if obj.Status != knowledge.StatusCandidate {
			continue
		}
		if obj.Confidence < b.gate.MinFinalScore {
			continue
		}
		if pErr := b.store.Promote(ctx, obj.ID, obj.Quality); pErr != nil {
			// best-effort: promotion failure does not roll back the Save.
			slog.Warn("distill bridge: promote object",
				"object_id", obj.ID, "error", pErr)
		} else {
			obj.Status = knowledge.StatusActive
		}
	}
	return nil
}

// toFloat32 converts a []float64 embedding vector to []float32, the format
// expected by Representation.Vector and FindDuplicate. The EmbeddingService
// interface returns []float64 (matching the ARES embedding client contract),
// while the vector store layer uses []float32 to halve memory and match the
// pgvector/sqlite-vec on-disk format.
func toFloat32(vec []float64) []float32 {
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(v)
	}
	return out
}
