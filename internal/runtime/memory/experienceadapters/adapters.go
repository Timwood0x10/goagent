// Package experienceadapters provides the shared adapter types that bridge
// production storage/knowledge types to the memory-retriever and distillation
// contracts. Both the SDK (sdk/memory_wiring.go) and the bootstrap layer
// (internal/ares_bootstrap/retriever_wiring.go) previously maintained
// duplicate copies of these adapters; this package is the single source of
// truth for that field mapping.
package experienceadapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/api/experience"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/knowledge/adapter"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	"github.com/Timwood0x10/ares/internal/scoreutil"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// DefaultTenant is the tenant scope used for distillation writes and RAG
// reads when the caller does not supply an explicit tenant.
const DefaultTenant = ares_events.DefaultTenantID

// DefaultListLimit caps the best-effort ListByType call backing
// GetByMemoryType. 1000 is a safe upper bound for a deduplication scan; the
// distiller's in-memory map dedups after retrieval anyway.
const DefaultListLimit = 1000

// ExperienceSearcher adapts repositories.ExperienceRepositoryInterface (which
// returns *storage_models.Experience) to the distillation.Experience contract
// expected by the memory retriever. The retriever only reads, so the narrow
// searcher surface is sufficient.
type ExperienceSearcher struct {
	// Repo is the underlying PostgreSQL experience repository.
	Repo repositories.ExperienceRepositoryInterface
}

// NewExperienceSearcher builds a searcher over the given repository.
func NewExperienceSearcher(repo repositories.ExperienceRepositoryInterface) *ExperienceSearcher {
	return &ExperienceSearcher{Repo: repo}
}

// SearchByVector delegates to the PostgreSQL repository and converts each
// storage_models.Experience into a distillation.Experience. Entries with a
// blank ID are dropped defensively — they cannot be referenced later and
// would only add noise to the prompt.
func (s *ExperienceSearcher) SearchByVector(
	ctx context.Context,
	vector []float64,
	tenantID string,
	limit int,
) ([]distillation.Experience, error) {
	if s == nil || s.Repo == nil {
		return nil, errors.New("experience searcher: repository is nil")
	}
	storageExps, err := s.Repo.SearchByVector(ctx, vector, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("experience searcher: %w", err)
	}
	out := make([]distillation.Experience, 0, len(storageExps))
	for _, se := range storageExps {
		if se == nil || se.ID == "" {
			continue
		}
		out = append(out, ToDistillationExperience(se))
	}
	return out, nil
}

// DistillationRepo adapts repositories.ExperienceRepositoryInterface to the
// full distillation.ExperienceRepository contract required by
// NewMemoryManagerWithDistiller. It carries the write-side methods
// (Create/Update/Delete/DeleteBatch) and the memory-type queries
// (GetByMemoryType/CountByMemoryType) the distiller invokes at store and
// deduplication time.
//
// The underlying repository is responsible for its own concurrency safety;
// this adapter holds no mutable state and is safe for concurrent use.
type DistillationRepo struct {
	// Repo is the underlying PostgreSQL experience repository.
	Repo repositories.ExperienceRepositoryInterface
	// DefaultTenant is used for Create/Update when the Experience DTO
	// carries no tenant (the distillation.Experience struct has no TenantID
	// field, so the distiller path relies on the adapter to supply one).
	DefaultTenant string
}

// NewDistillationRepo constructs an adapter wrapping the given postgres
// repository. defaultTenant is used for Create/Update when the Experience
// DTO carries no tenant.
func NewDistillationRepo(repo repositories.ExperienceRepositoryInterface, defaultTenant string) *DistillationRepo {
	if defaultTenant == "" {
		defaultTenant = DefaultTenant
	}
	return &DistillationRepo{Repo: repo, DefaultTenant: defaultTenant}
}

// SearchByVector delegates to the postgres repository and converts each
// storage_models.Experience into a distillation.Experience.
func (r *DistillationRepo) SearchByVector(
	ctx context.Context,
	vector []float64,
	tenantID string,
	limit int,
) ([]distillation.Experience, error) {
	if r == nil || r.Repo == nil {
		return nil, errors.New("distillation repo: repository is nil")
	}
	storageExps, err := r.Repo.SearchByVector(ctx, vector, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("distillation repo search: %w", err)
	}
	out := make([]distillation.Experience, 0, len(storageExps))
	for _, se := range storageExps {
		if se == nil || se.ID == "" {
			continue
		}
		out = append(out, ToDistillationExperience(se))
	}
	return out, nil
}

// memoryTypeToStorageType maps a distillation MemoryType to the storage
// Type value used for persistence and lookup. The storage schema stores
// success/failure and legacy outcome labels, while MemoryType carries
// knowledge/preference/interaction/profile semantics; this single mapping
// keeps write-side Type and query-side WHERE type = $1 consistent so the
// distiller's solution cap and cross-session dedup can match rows.
func memoryTypeToStorageType(mt experience.MemoryType) string {
	switch mt {
	case experience.MemoryKnowledge:
		return storage_models.ExperienceTypeSuccess
	case experience.MemoryPreference:
		return storage_models.ExperienceTypePattern
	case experience.MemoryInteraction:
		return storage_models.ExperienceTypeSolution
	case experience.MemoryProfile:
		return storage_models.ExperienceTypeDistilled
	default:
		return storage_models.ExperienceTypeSuccess
	}
}

// GetByMemoryType returns experiences whose storage Type matches the
// memory-type label. The mapping is best-effort: storage Type stores
// success/failure/etc. while MemoryType.String() returns
// fact/preference/solution/profile, so this surface is approximate. It is
// only used by the distiller's deduplication path.
func (r *DistillationRepo) GetByMemoryType(
	ctx context.Context,
	tenantID string,
	memoryType experience.MemoryType,
) ([]experience.Experience, error) {
	if r == nil || r.Repo == nil {
		return nil, errors.New("distillation repo: repository is nil")
	}
	storageExps, err := r.Repo.ListByType(ctx, memoryTypeToStorageType(memoryType), tenantID, DefaultListLimit)
	if err != nil {
		return nil, fmt.Errorf("distillation repo get by memory type: %w", err)
	}
	out := make([]experience.Experience, 0, len(storageExps))
	for _, se := range storageExps {
		if se == nil || se.ID == "" {
			continue
		}
		out = append(out, ToDistillationExperience(se))
	}
	return out, nil
}

// CountByMemoryType returns the number of experiences for the given tenant
// and memory type. Best-effort, mirroring GetByMemoryType's approximate
// Type→MemoryType mapping.
func (r *DistillationRepo) CountByMemoryType(
	ctx context.Context,
	tenantID string,
	memoryType experience.MemoryType,
) (int, error) {
	if r == nil || r.Repo == nil {
		return 0, errors.New("distillation repo: repository is nil")
	}
	storageExps, err := r.Repo.ListByType(ctx, memoryTypeToStorageType(memoryType), tenantID, DefaultListLimit)
	if err != nil {
		return 0, fmt.Errorf("distillation repo count by memory type: %w", err)
	}
	count := 0
	for _, se := range storageExps {
		if se != nil && se.ID != "" {
			count++
		}
	}
	return count, nil
}

// Create inserts a new experience. The Experience DTO carries no tenant,
// so the adapter's DefaultTenant is applied. ExtractionMethod is preserved
// in Metadata so the round-trip through SearchByVector restores it.
func (r *DistillationRepo) Create(ctx context.Context, exp *experience.Experience) error {
	if r == nil || r.Repo == nil {
		return errors.New("distillation repo: repository is nil")
	}
	if exp == nil {
		return errors.New("distillation repo: experience is nil")
	}
	storage := ToStorageExperience(exp, r.DefaultTenant)
	if err := r.Repo.Create(ctx, storage); err != nil {
		return fmt.Errorf("distillation repo create: %w", err)
	}
	return nil
}

// Update updates an existing experience. Same tenant/ExtractionMethod
// handling as Create.
func (r *DistillationRepo) Update(ctx context.Context, exp *experience.Experience) error {
	if r == nil || r.Repo == nil {
		return errors.New("distillation repo: repository is nil")
	}
	if exp == nil {
		return errors.New("distillation repo: experience is nil")
	}
	storage := ToStorageExperience(exp, r.DefaultTenant)
	if err := r.Repo.Update(ctx, storage); err != nil {
		return fmt.Errorf("distillation repo update: %w", err)
	}
	return nil
}

// Delete removes an experience by ID.
func (r *DistillationRepo) Delete(ctx context.Context, id string) error {
	if r == nil || r.Repo == nil {
		return errors.New("distillation repo: repository is nil")
	}
	if err := r.Repo.Delete(ctx, id, r.DefaultTenant); err != nil {
		return fmt.Errorf("distillation repo delete: %w", err)
	}
	return nil
}

// DeleteBatch deletes multiple experiences by ID. The postgres repository
// exposes no batch API, so this loops single deletes. A failure short-circuits
// and the remaining IDs are left in place; the caller (distiller) already
// falls back to per-id deletes on batch failure.
func (r *DistillationRepo) DeleteBatch(ctx context.Context, ids []string) error {
	if r == nil || r.Repo == nil {
		return errors.New("distillation repo: repository is nil")
	}
	for _, id := range ids {
		if err := r.Repo.Delete(ctx, id, r.DefaultTenant); err != nil {
			return fmt.Errorf("distillation repo delete batch %s: %w", id, err)
		}
	}
	return nil
}

// storageTypeToMemoryType maps a stored Type value back to a MemoryType.
// It is the inverse of memoryTypeToStorageType and keeps the read side
// (GetByMemoryType / SearchByVector) consistent with what was persisted.
func storageTypeToMemoryType(storageType string) experience.MemoryType {
	switch storageType {
	case storage_models.ExperienceTypePattern:
		return experience.MemoryPreference
	case storage_models.ExperienceTypeSolution:
		return experience.MemoryInteraction
	case storage_models.ExperienceTypeDistilled:
		return experience.MemoryProfile
	case storage_models.ExperienceTypeSuccess:
		return experience.MemoryKnowledge
	default:
		return experience.MemoryKnowledge
	}
}

// ToDistillationExperience maps a storage_models.Experience into the
// canonical distillation.Experience DTO. Problem/Solution fall back to the
// legacy Input/Output fields when the high-level fields are empty (the
// storage layer stores them in the 'input'/'output' columns for backward
// compat). Confidence is clamped to [0, 1] so downstream filtering operates
// on a well-defined domain. ExtractionMethod is recovered from Metadata
// when present, defaulting to ExtractionDirect.
func ToDistillationExperience(e *storage_models.Experience) distillation.Experience {
	problem := e.Problem
	if problem == "" {
		problem = e.Input
	}
	solution := e.Solution
	if solution == "" {
		solution = e.Output
	}
	method := distillation.ExtractionDirect
	if e.Metadata != nil {
		if m, ok := e.Metadata["extraction_method"].(string); ok && m != "" {
			method = distillation.ExtractionMethod(m)
		}
	}
	return distillation.Experience{
		ID:               e.ID,
		Type:             storageTypeToMemoryType(e.Type),
		Problem:          problem,
		Solution:         solution,
		Confidence:       scoreutil.ClampUnit(e.Score),
		ExtractionMethod: method,
		Vector:           e.Embedding,
	}
}

// ToStorageExperience maps a distillation.Experience into a
// storage_models.Experience DTO ready for postgres persistence. Problem and
// Solution are mirrored into the legacy Input/Output columns so existing
// keyword-search and backward-compat reads keep working. ExtractionMethod
// is stashed in Metadata for round-trip fidelity. tenantID is supplied by
// the adapter (the Experience DTO carries no tenant).
func ToStorageExperience(exp *distillation.Experience, tenantID string) *storage_models.Experience {
	meta := map[string]any{}
	if exp.ExtractionMethod != "" {
		meta["extraction_method"] = string(exp.ExtractionMethod)
	}
	return &storage_models.Experience{
		ID:        exp.ID,
		TenantID:  tenantID,
		Type:      memoryTypeToStorageType(exp.Type),
		Problem:   exp.Problem,
		Solution:  exp.Solution,
		Input:     exp.Problem,
		Output:    exp.Solution,
		Embedding: exp.Vector,
		Score:     exp.Confidence,
		Metadata:  meta,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

// KnowledgeRetrieverAdapter wraps adapter.KnowledgeRetriever and converts
// its local adapter.ContextSnippet results into the canonical
// memctx.ContextSnippet so the MemoryManager's context builder can consume
// them uniformly alongside MemoryRetriever output.
//
// The conversion is a shallow field copy — both ContextSnippet types have
// identical shapes (Source, Content, Score, Metadata). The adapter exists
// only to bridge the import boundary (knowledge/adapter cannot import
// ares_memory/context without creating a cycle through distillation).
type KnowledgeRetrieverAdapter struct {
	// Inner is the underlying AKG knowledge retriever.
	Inner *adapter.KnowledgeRetriever
}

// NewKnowledgeRetrieverAdapter builds an adapter over the given retriever.
func NewKnowledgeRetrieverAdapter(inner *adapter.KnowledgeRetriever) *KnowledgeRetrieverAdapter {
	return &KnowledgeRetrieverAdapter{Inner: inner}
}

// Retrieve delegates to the underlying KnowledgeRetriever and converts each
// adapter.ContextSnippet into a memctx.ContextSnippet. A nil inner
// retriever yields an empty slice — this keeps BuildContext resilient when
// the AKG runtime was not constructed.
func (a *KnowledgeRetrieverAdapter) Retrieve(
	ctx context.Context,
	input string,
	topK int,
) ([]memctx.ContextSnippet, error) {
	if a == nil || a.Inner == nil {
		return []memctx.ContextSnippet{}, nil
	}
	snippets, err := a.Inner.Retrieve(ctx, input, topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge retriever adapter: %w", err)
	}
	out := make([]memctx.ContextSnippet, 0, len(snippets))
	for _, s := range snippets {
		out = append(out, memctx.ContextSnippet{
			Source:   s.Source,
			Content:  s.Content,
			Score:    s.Score,
			Metadata: s.Metadata,
		})
	}
	return out, nil
}
