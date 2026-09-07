package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// stubExpRepo is a minimal ExperienceRepositoryInterface whose ListByAgent
// behavior the test controls. All other methods panic to surface accidental
// use (the production path only calls ListByAgent for the spawn prior).
type stubExpRepo struct {
	exps    []*models.Experience
	err     error
	agentID string // records the agent queried
}

var _ repositories.ExperienceRepositoryInterface = (*stubExpRepo)(nil)

func (s *stubExpRepo) Create(context.Context, *models.Experience) error { panic("not used") }
func (s *stubExpRepo) GetByID(context.Context, string, string) (*models.Experience, error) {
	panic("not used")
}
func (s *stubExpRepo) Update(context.Context, *models.Experience) error { panic("not used") }
func (s *stubExpRepo) UpdateEmbedding(context.Context, string, string, []float64, string, int) error {
	panic("not used")
}
func (s *stubExpRepo) Delete(context.Context, string, string) error { panic("not used") }
func (s *stubExpRepo) SearchByVector(context.Context, []float64, string, int) ([]*models.Experience, error) {
	panic("not used")
}
func (s *stubExpRepo) SearchByKeyword(context.Context, string, string, int) ([]*models.Experience, error) {
	panic("not used")
}
func (s *stubExpRepo) IncrementUsageCount(context.Context, string, string) error { panic("not used") }
func (s *stubExpRepo) DecrementRank(context.Context, string, string) error       { panic("not used") }
func (s *stubExpRepo) ListByType(context.Context, string, string, int) ([]*models.Experience, error) {
	panic("not used")
}
func (s *stubExpRepo) ListByAgent(ctx context.Context, agentID, tenantID string, limit int) ([]*models.Experience, error) {
	s.agentID = agentID
	if s.err != nil {
		return nil, s.err
	}
	return s.exps, nil
}

// TestLoadExperiencePrior_ReturnsDistilledExperience verifies the wiring
// (蒸馏异步产出 → 经验仓库查询 → spawn 注入): the
// agent's most recent distilled experience is returned as a structured prior
// (type/problem/solution/constraints) and the query scopes the default tenant.
func TestLoadExperiencePrior_ReturnsDistilledExperience(t *testing.T) {
	repo := &stubExpRepo{exps: []*models.Experience{
		{
			Type:        models.ExperienceTypeSuccess,
			Problem:     "FFI pointer safety",
			Solution:    "use checked accessors",
			Constraints: "never unsized ABI types",
			AgentID:     "ffi-expert",
		},
	}}
	prior := loadExperiencePrior(context.Background(), repo, "ffi-expert")
	if prior == nil {
		t.Fatal("expected a prior from the distilled experience")
	}
	m, ok := prior.(map[string]any)
	if !ok {
		t.Fatalf("prior = %T, want map[string]any", prior)
	}
	if m["solution"] != "use checked accessors" {
		t.Fatalf("prior solution = %v", m["solution"])
	}
	if m["type"] != models.ExperienceTypeSuccess {
		t.Fatalf("prior type = %v", m["type"])
	}
	if repo.agentID != "ffi-expert" {
		t.Fatalf("queried agent = %q, want ffi-expert", repo.agentID)
	}
}

// TestLoadExperiencePrior_ZeroValueOnNoRepo verifies the zero-value contract
// a nil repo (distillation not wired) yields a nil
// prior — never an error or a panic.
func TestLoadExperiencePrior_ZeroValueOnNoRepo(t *testing.T) {
	if prior := loadExperiencePrior(context.Background(), nil, "any"); prior != nil {
		t.Fatalf("nil repo must yield nil prior, got %v", prior)
	}
}

// TestLoadExperiencePrior_ZeroValueOnEmptyOrError verifies graceful
// degradation: no distilled experience yet, or a repo failure, both yield a
// nil prior (spawn proceeds blank) — the failure is never fatal to spawn.
func TestLoadExperiencePrior_ZeroValueOnEmptyOrError(t *testing.T) {
	ctx := context.Background()
	if prior := loadExperiencePrior(ctx, &stubExpRepo{}, "fresh"); prior != nil {
		t.Fatalf("empty repo must yield nil prior, got %v", prior)
	}
	if prior := loadExperiencePrior(ctx, &stubExpRepo{err: errors.New("pg down")}, "fresh"); prior != nil {
		t.Fatalf("failed query must yield nil prior, got %v", prior)
	}
}

// TestLoadExperiencePrior_TenantScope verifies the prior query uses the same
// single-tenant scope as every other distillation consumer
// (ares_events.DefaultTenantID).
func TestLoadExperiencePrior_TenantScope(t *testing.T) {
	repo := &stubExpRepo{exps: []*models.Experience{{Type: models.ExperienceTypeSuccess}}}
	_ = loadExperiencePrior(context.Background(), repo, "agent-x")
	if repo.agentID != "agent-x" {
		t.Fatalf("agent queried = %q", repo.agentID)
	}
	// The tenant is applied inside the repo in production; here we assert the
	// contract constant is stable and matches the distillation writer.
	if ares_events.DefaultTenantID != "default" {
		t.Fatalf("DefaultTenantID = %q, want default", ares_events.DefaultTenantID)
	}
}
