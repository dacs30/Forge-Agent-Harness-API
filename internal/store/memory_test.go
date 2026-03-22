package store

import (
	"context"
	"testing"
	"time"

	"haas/internal/domain"
)

func TestMemoryStore_CRUD(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()

	env := &domain.Environment{
		ID:     "env_test1",
		Status: domain.StatusRunning,
		Spec: domain.EnvironmentSpec{
			Image: "alpine:latest",
		},
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		ExpiresAt:  time.Now().Add(60 * time.Minute),
	}

	// Create
	if err := s.Create(ctx, env); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := s.Get(ctx, env.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != env.ID {
		t.Fatalf("expected ID %s, got %s", env.ID, got.ID)
	}

	// Update
	env.Status = domain.StatusStopped
	if err := s.Update(ctx, env); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.Get(ctx, env.ID)
	if got.Status != domain.StatusStopped {
		t.Fatalf("expected status stopped, got %s", got.Status)
	}

	// List
	envs, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 env, got %d", len(envs))
	}

	// Delete
	if err := s.Delete(ctx, env.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Get after delete
	_, err = s.Get(ctx, env.ID)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStore_NotFound(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	err = s.Update(ctx, &domain.Environment{ID: "nonexistent"})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on update, got %v", err)
	}

	err = s.Delete(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on delete, got %v", err)
	}
}

func TestMemoryStore_ListExpired(t *testing.T) {
	s := NewMemoryStore(5*time.Minute, 30*time.Minute)
	ctx := context.Background()

	now := time.Now()

	// Active environment
	active := &domain.Environment{
		ID:         "env_active",
		Status:     domain.StatusRunning,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(30 * time.Minute),
	}

	// Idle-expired environment
	idle := &domain.Environment{
		ID:         "env_idle",
		Status:     domain.StatusRunning,
		CreatedAt:  now.Add(-20 * time.Minute),
		LastUsedAt: now.Add(-10 * time.Minute), // last used 10 min ago, timeout is 5 min
		ExpiresAt:  now.Add(10 * time.Minute),
	}

	// Lifetime-expired environment
	expired := &domain.Environment{
		ID:         "env_expired",
		Status:     domain.StatusRunning,
		CreatedAt:  now.Add(-60 * time.Minute),
		LastUsedAt: now,
		ExpiresAt:  now.Add(-1 * time.Minute), // expired 1 min ago
	}

	s.Create(ctx, active)
	s.Create(ctx, idle)
	s.Create(ctx, expired)

	result, err := s.ListExpired(ctx)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 expired envs, got %d", len(result))
	}

	ids := map[string]bool{}
	for _, e := range result {
		ids[e.ID] = true
	}
	if !ids["env_idle"] || !ids["env_expired"] {
		t.Fatalf("expected env_idle and env_expired, got %v", ids)
	}
}
