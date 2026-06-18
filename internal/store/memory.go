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
	snapshots   map[string]*domain.Snapshot
	skills      map[string]*skillRecord
	idleTimeout time.Duration
	maxLifetime time.Duration
}

// skillRecord bundles skill metadata with its archive bytes for the in-memory store.
type skillRecord struct {
	skill   *domain.Skill
	archive []byte
}

func NewMemoryStore(idleTimeout, maxLifetime time.Duration) *MemoryStore {
	return &MemoryStore{
		envs:        make(map[string]*domain.Environment),
		snapshots:   make(map[string]*domain.Snapshot),
		skills:      make(map[string]*skillRecord),
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

// List returns all environments. If userID is non-empty, only that user's environments are returned.
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

// ListByTenant returns all environments whose TenantID matches, spanning all end-users.
func (s *MemoryStore) ListByTenant(_ context.Context, tenantID string) ([]*domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Environment, 0, len(s.envs))
	for _, env := range s.envs {
		if env.TenantID == tenantID {
			result = append(result, env)
		}
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

func (s *MemoryStore) CreateSnapshot(_ context.Context, snap *domain.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[snap.ID] = snap
	return nil
}

func (s *MemoryStore) GetSnapshot(_ context.Context, id, userID string) (*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snapshots[id]
	if !ok {
		return nil, ErrNotFound
	}
	if userID != "" && snap.UserID != userID {
		return nil, ErrNotFound
	}
	return snap, nil
}

func (s *MemoryStore) ListSnapshots(_ context.Context, userID string) ([]*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Snapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		if userID != "" && snap.UserID != userID {
			continue
		}
		result = append(result, snap)
	}
	return result, nil
}

func (s *MemoryStore) DeleteSnapshot(_ context.Context, id, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.snapshots[id]
	if !ok {
		return ErrNotFound
	}
	if userID != "" && snap.UserID != userID {
		return ErrNotFound
	}
	delete(s.snapshots, id)
	return nil
}

// ListSnapshotsByTenant returns all snapshots whose TenantID matches, spanning all end-users.
func (s *MemoryStore) ListSnapshotsByTenant(_ context.Context, tenantID string) ([]*domain.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Snapshot, 0, len(s.snapshots))
	for _, snap := range s.snapshots {
		if snap.TenantID == tenantID {
			result = append(result, snap)
		}
	}
	return result, nil
}

// UpsertSkill inserts a new skill or, when one with the same (userID, name)
// already exists, replaces its archive in place while preserving the original
// ID and CreatedAt.
func (s *MemoryStore) UpsertSkill(_ context.Context, skill *domain.Skill, archive []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.skills {
		if rec.skill.UserID == skill.UserID && rec.skill.Name == skill.Name {
			rec.skill.SizeBytes = skill.SizeBytes
			rec.skill.UpdatedAt = skill.UpdatedAt
			rec.archive = archive
			// Reflect the persisted identity back to the caller.
			skill.ID = rec.skill.ID
			skill.CreatedAt = rec.skill.CreatedAt
			return nil
		}
	}
	stored := *skill
	s.skills[skill.ID] = &skillRecord{skill: &stored, archive: archive}
	return nil
}

func (s *MemoryStore) GetSkill(_ context.Context, id, userID string) (*domain.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.skills[id]
	if !ok {
		return nil, ErrNotFound
	}
	if userID != "" && rec.skill.UserID != userID {
		return nil, ErrNotFound
	}
	return rec.skill, nil
}

func (s *MemoryStore) GetSkillArchive(_ context.Context, id, userID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.skills[id]
	if !ok {
		return nil, ErrNotFound
	}
	if userID != "" && rec.skill.UserID != userID {
		return nil, ErrNotFound
	}
	return rec.archive, nil
}

func (s *MemoryStore) ListSkills(_ context.Context, userID string) ([]*domain.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Skill, 0, len(s.skills))
	for _, rec := range s.skills {
		if userID != "" && rec.skill.UserID != userID {
			continue
		}
		result = append(result, rec.skill)
	}
	return result, nil
}

func (s *MemoryStore) DeleteSkill(_ context.Context, id, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.skills[id]
	if !ok {
		return ErrNotFound
	}
	if userID != "" && rec.skill.UserID != userID {
		return ErrNotFound
	}
	delete(s.skills, id)
	return nil
}

// ListSkillsByTenant returns all skills whose TenantID matches, spanning all end-users.
func (s *MemoryStore) ListSkillsByTenant(_ context.Context, tenantID string) ([]*domain.Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*domain.Skill, 0, len(s.skills))
	for _, rec := range s.skills {
		if rec.skill.TenantID == tenantID {
			result = append(result, rec.skill)
		}
	}
	return result, nil
}
