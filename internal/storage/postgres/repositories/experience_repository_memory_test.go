// Package repositories unit tests for the in-memory experience repository.
package repositories

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/errors"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// TestMemoryExperienceRepository_GetByID_NotFoundMatchesPGContract locks the
// not-found contract: the memory implementation returns errors.ErrRecordNotFound
// — the same sentinel the PG repository returns on sql.ErrNoRows — instead of
// the previous (nil, nil), so a caller cannot silently misread "not found" as
// "no error, empty result" when switching stores.
func TestMemoryExperienceRepository_GetByID_NotFoundMatchesPGContract(t *testing.T) {
	repo := NewMemoryExperienceRepository()
	ctx := context.Background()

	// A tenant mismatch is also "not found" (tenant-scoped lookup).
	_, err := repo.GetByID(ctx, "tenant-1", "missing-id")
	require.ErrorIs(t, err, errors.ErrRecordNotFound)

	// Hit path: created row, matching tenant.
	created := &storage_models.Experience{
		ID:       "exp-1",
		TenantID: "tenant-1",
		Type:     storage_models.ExperienceTypeQuery,
		Input:    "in",
		Output:   "out",
	}
	require.NoError(t, repo.Create(ctx, created))
	got, err := repo.GetByID(ctx, "tenant-1", created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)

	// Same id under a DIFFERENT tenant stays not-found (tenant isolation).
	_, err = repo.GetByID(ctx, "tenant-2", created.ID)
	require.ErrorIs(t, err, errors.ErrRecordNotFound)
}
