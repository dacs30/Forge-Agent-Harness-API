package store

import (
	"context"
	"errors"

	"haas/internal/domain"
)

var ErrNotFound = errors.New("environment not found")

// Store is the persistence interface for environments and tenant bootstrap.
//
// Multi-tenancy: Get, List, and Delete are scoped to a userID.
// Passing userID="" bypasses the ownership filter — reserved for internal
// use by the Reaper, which must operate across all tenants.
type Store interface {
	// Environment operations
	Create(ctx context.Context, env *domain.Environment) error
	Get(ctx context.Context, id, userID string) (*domain.Environment, error)
	Update(ctx context.Context, env *domain.Environment) error
	Delete(ctx context.Context, id, userID string) error
	List(ctx context.Context, userID string) ([]*domain.Environment, error)
	ListExpired(ctx context.Context) ([]*domain.Environment, error)

	// BootstrapUser persists a key-hash → user-ID mapping on startup.
	// Called once per API key when using a persistent store.
	// No-op for the in-memory store.
	BootstrapUser(ctx context.Context, keyHash, userID string) error

	// ListByTenant returns all environments owned by tenantID regardless of end-user scoping.
	// Used by the service owner to see every container created under their API key.
	ListByTenant(ctx context.Context, tenantID string) ([]*domain.Environment, error)

	// Snapshot operations
	CreateSnapshot(ctx context.Context, snap *domain.Snapshot) error
	GetSnapshot(ctx context.Context, id, userID string) (*domain.Snapshot, error)
	ListSnapshots(ctx context.Context, userID string) ([]*domain.Snapshot, error)
	DeleteSnapshot(ctx context.Context, id, userID string) error

	// ListSnapshotsByTenant returns all snapshots owned by tenantID regardless of end-user scoping.
	ListSnapshotsByTenant(ctx context.Context, tenantID string) ([]*domain.Snapshot, error)

	// Skill operations. Skills are reusable Agent Skills registered per end-user.
	// UpsertSkill inserts or replaces the skill identified by (userID, skill.Name),
	// storing the (uncompressed tar) archive bytes alongside the metadata.
	UpsertSkill(ctx context.Context, skill *domain.Skill, archive []byte) error
	GetSkill(ctx context.Context, id, userID string) (*domain.Skill, error)
	// GetSkillArchive returns the stored tar archive bytes for a skill.
	GetSkillArchive(ctx context.Context, id, userID string) ([]byte, error)
	ListSkills(ctx context.Context, userID string) ([]*domain.Skill, error)
	DeleteSkill(ctx context.Context, id, userID string) error

	// ListSkillsByTenant returns all skills owned by tenantID regardless of end-user scoping.
	ListSkillsByTenant(ctx context.Context, tenantID string) ([]*domain.Skill, error)
}
