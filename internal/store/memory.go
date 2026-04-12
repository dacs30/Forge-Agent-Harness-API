package store

import (
	"context"
	"sync"
	"time"

	"haas/internal/domain"
)

type MemoryStore struct {
	mu          sync.RWMutex
	envs        map[string]*domain.Environment
	idleTimeout time.Duration
	maxLifetime time.Duration
}

func NewMemoryStore(idleTimeout, maxLifetime time.Duration) *MemoryStore {
	return &MemoryStore{
		envs:        make(map[string]*domain.Environment),
		idleTimeout: idleTimeout,
		maxLifetime: maxLifetime,
	}
}

func (s *MemoryStore) Create(_ context.Context, env *domain.Environment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envs[env.ID] = env
	return nil
}

// Get returns the environment by ID. If userID is non-empty, it must match env.UserID.
func (s *MemoryStore) Get(_ context.Context, id, userID string) (*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	env, ok := s.envs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if userID != "" && env.UserID != userID {
		return nil, ErrNotFound // do not reveal existence to other tenants
	}
	return env, nil
}

func (s *MemoryStore) Update(_ context.Context, env *domain.Environment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.envs[env.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.UserID != env.UserID {
		return ErrNotFound // do not reveal existence to other tenants
	}
	s.envs[env.ID] = env
	return nil
}

// Delete removes the environment by ID. If userID is non-empty, it must match env.UserID.
func (s *MemoryStore) Delete(_ context.Context, id, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	env, ok := s.envs[id]
	if !ok {
		return ErrNotFound
	}
	if userID != "" && env.UserID != userID {
		return ErrNotFound
	}
	delete(s.envs, id)
	return nil
}

// List returns all environments. If userID is non-empty, only that tenant's environments are returned.
func (s *MemoryStore) List(_ context.Context, userID string) ([]*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		if userID != "" && env.UserID != userID {
			continue
		}
		result = append(result, env)
	}
	return result, nil
}

func (s *MemoryStore) ListExpired(_ context.Context) ([]*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	var expired []*domain.Environment
	for _, env := range s.envs {
		if env.Status == domain.StatusDestroyed || env.Status == domain.StatusStopped {
			continue
		}
		idleExpired := now.Sub(env.LastUsedAt) > s.idleTimeout
		lifetimeExpired := now.After(env.ExpiresAt)
		if idleExpired || lifetimeExpired {
			expired = append(expired, env)
		}
	}
	return expired, nil
}

// BootstrapUser is a no-op for the in-memory store.
func (s *MemoryStore) BootstrapUser(_ context.Context, _, _ string) error {
	return nil
}
