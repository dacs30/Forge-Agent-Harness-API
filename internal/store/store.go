package store

import (
	"context"
	"errors"

	"haas/internal/domain"
)

var ErrNotFound = errors.New("environment not found")

type Store interface {
	Create(ctx context.Context, env *domain.Environment) error
	Get(ctx context.Context, id string) (*domain.Environment, error)
	Update(ctx context.Context, env *domain.Environment) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]*domain.Environment, error)
	ListExpired(ctx context.Context) ([]*domain.Environment, error)
}
