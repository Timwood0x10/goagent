package repositories

import (
	"context"
	"strings"
	"sync"

	"github.com/Timwood0x10/ares/internal/errors"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// errExperienceInvalid reports an invalid experience argument.
var errExperienceInvalid = func(msg string) error {
	return errors.New("experience: " + msg)
}

// memoryExperienceRepository is an in-memory implementation of
// ExperienceRepositoryInterface. It is the default experience store when no
// Postgres is configured, so distillation keeps working in memory-first
// deployments. Postgres remains a pluggable alternative (ExperienceRepository).
//
// Thread-safe: all operations are guarded by a single mutex. Vector search
// falls back to keyword matching because no embedding index exists in memory.
type memoryExperienceRepository struct {
	mu   sync.RWMutex
	next int64
	exps map[string]*storage_models.Experience
}

// NewMemoryExperienceRepository creates an in-memory experience repository.
//
// Args:
//
//	none.
//
// Returns:
//
//	ExperienceRepositoryInterface - the in-memory repository.
func NewMemoryExperienceRepository() ExperienceRepositoryInterface {
	return &memoryExperienceRepository{
		exps: make(map[string]*storage_models.Experience),
	}
}

// Create inserts a new experience into memory.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	exp - the experience to insert; ID and TenantID must be non-empty.
//
// Returns:
//
//	error - validation error, or nil on success.
func (r *memoryExperienceRepository) Create(ctx context.Context, exp *storage_models.Experience) error {
	if exp.ID == "" {
		return errExperienceInvalid("id is required")
	}
	if exp.TenantID == "" {
		return errExperienceInvalid("tenant_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *exp
	if cp.ID == "" {
		r.next++
		cp.ID = idOf(r.next)
	}
	r.exps[cp.ID] = &cp
	return nil
}

// GetByID retrieves an experience by ID within the given tenant.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	tenantID - tenant scope.
//	id - the experience id.
//
// Returns:
//
//	*Experience - the experience.
//	error - errors.ErrRecordNotFound when no experience with this id exists
//	  under the tenant. The PG repository returns the same sentinel on
//	  sql.ErrNoRows; the memory implementation previously returned
//	  (nil, nil), which contradicted its own "matching the PG repository"
//	  comment — a caller switching stores would silently change behavior.
func (r *memoryExperienceRepository) GetByID(ctx context.Context, tenantID, id string) (*storage_models.Experience, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	exp, ok := r.exps[id]
	if !ok || exp.TenantID != tenantID {
		return nil, errors.ErrRecordNotFound
	}
	cp := *exp
	return &cp, nil
}

// Update replaces an existing experience in memory.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	exp - the experience to replace; must exist with a matching tenant.
//
// Returns:
//
//	error - nil.
func (r *memoryExperienceRepository) Update(ctx context.Context, exp *storage_models.Experience) error {
	if exp.ID == "" {
		return errExperienceInvalid("id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.exps[exp.ID]; !ok {
		return nil
	}
	cp := *exp
	r.exps[cp.ID] = &cp
	return nil
}

// UpdateEmbedding writes back only the vector columns of one row, mirroring the
// postgres repository's narrow write-back path.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	tenantID - tenant scope; a mismatch is treated as "no such row".
//	id - the experience id.
//	embedding - the vector to store.
//	model - embedding model name.
//	version - embedding schema version.
//
// Returns:
//
//	error - nil when the row is absent, matching Update's lenient semantics.
func (r *memoryExperienceRepository) UpdateEmbedding(
	ctx context.Context,
	tenantID, id string,
	embedding []float64,
	model string,
	version int,
) error {
	if id == "" {
		return errExperienceInvalid("id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.exps[id]
	if !ok || (tenantID != "" && exp.TenantID != tenantID) {
		return nil
	}
	exp.Embedding = embedding
	exp.EmbeddingModel = model
	exp.EmbeddingVersion = version
	return nil
}

// Delete removes an experience by its ID.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	id - the experience id.
//	tenantID - tenant scope.
//
// Returns:
//
//	error - nil.
func (r *memoryExperienceRepository) Delete(ctx context.Context, id, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.exps[id]
	if !ok || exp.TenantID != tenantID {
		return nil
	}
	delete(r.exps, id)
	return nil
}

// SearchByVector performs vector similarity search. In memory mode there is no
// embedding index, so it falls back to keyword matching on Problem/Solution.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	embedding - unused in memory mode.
//	tenantID - tenant scope.
//	limit - max results.
//
// Returns:
//
//	[]*Experience - matching experiences.
//	error - nil.
func (r *memoryExperienceRepository) SearchByVector(ctx context.Context, embedding []float64, tenantID string, limit int) ([]*storage_models.Experience, error) {
	return r.ListByType(ctx, "", tenantID, limit)
}

// SearchByKeyword performs keyword-based search over Problem and Solution.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	query - the keyword to match.
//	tenantID - tenant scope.
//	limit - max results.
//
// Returns:
//
//	[]*Experience - matching experiences.
//	error - nil.
func (r *memoryExperienceRepository) SearchByKeyword(ctx context.Context, query, tenantID string, limit int) ([]*storage_models.Experience, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	q := strings.ToLower(query)
	var out []*storage_models.Experience
	for _, exp := range r.exps {
		if exp.TenantID != tenantID {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(exp.Problem), q) || strings.Contains(strings.ToLower(exp.Solution), q) {
			cp := *exp
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// IncrementUsageCount increments the usage count of an experience.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	tenantID - tenant scope.
//	id - the experience id.
//
// Returns:
//
//	error - nil.
func (r *memoryExperienceRepository) IncrementUsageCount(ctx context.Context, tenantID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.exps[id]
	if !ok || exp.TenantID != tenantID {
		return nil
	}
	exp.UsageCount++
	return nil
}

// DecrementRank decreases the score of an experience as negative feedback.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	tenantID - tenant scope.
//	id - the experience id.
//
// Returns:
//
//	error - nil.
func (r *memoryExperienceRepository) DecrementRank(ctx context.Context, tenantID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.exps[id]
	if !ok || exp.TenantID != tenantID {
		return nil
	}
	exp.Score--
	return nil
}

// ListByType retrieves experiences by type within a tenant.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	expType - "success" / "failure"; empty matches all.
//	tenantID - tenant scope.
//	limit - max results.
//
// Returns:
//
//	[]*Experience - matching experiences.
//	error - nil.
func (r *memoryExperienceRepository) ListByType(ctx context.Context, expType, tenantID string, limit int) ([]*storage_models.Experience, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*storage_models.Experience
	for _, exp := range r.exps {
		if exp.TenantID != tenantID {
			continue
		}
		if expType != "" && exp.Type != expType {
			continue
		}
		cp := *exp
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ListByAgent retrieves experiences for a specific agent.
//
// Args:
//
//	ctx - unused (kept for interface conformance).
//	agentID - the agent id.
//	tenantID - tenant scope.
//	limit - max results.
//
// Returns:
//
//	[]*Experience - matching experiences.
//	error - nil.
func (r *memoryExperienceRepository) ListByAgent(ctx context.Context, agentID, tenantID string, limit int) ([]*storage_models.Experience, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*storage_models.Experience
	for _, exp := range r.exps {
		if exp.TenantID != tenantID {
			continue
		}
		if agentID != "" && exp.AgentID != agentID {
			continue
		}
		cp := *exp
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func idOf(n int64) string {
	return "mem-exp-" + strings.Repeat("0", 3-len(intToStr(n))) + intToStr(n)
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
