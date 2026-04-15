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

	// Snapshot operations
	CreateSnapshot(ctx context.Context, snap *domain.Snapshot) error
	GetSnapshot(ctx context.Context, id, userID string) (*domain.Snapshot, error)
	ListSnapshots(ctx context.Context, userID string) ([]*domain.Snapshot, error)
	DeleteSnapshot(ctx context.Context, id, userID string) error
}
