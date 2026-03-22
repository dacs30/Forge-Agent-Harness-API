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

func (s *MemoryStore) Get(_ context.Context, id string) (*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	env, ok := s.envs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return env, nil
}

func (s *MemoryStore) Update(_ context.Context, env *domain.Environment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.envs[env.ID]; !ok {
		return ErrNotFound
	}
	s.envs[env.ID] = env
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.envs[id]; !ok {
		return ErrNotFound
	}
	delete(s.envs, id)
	return nil
}

func (s *MemoryStore) List(_ context.Context) ([]*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Environment, 0, len(s.envs))
	for _, env := range s.envs {
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
