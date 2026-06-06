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
	got, err := s.Get(ctx, env.ID, "")
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
	got, _ = s.Get(ctx, env.ID, "")
	if got.Status != domain.StatusStopped {
		t.Fatalf("expected status stopped, got %s", got.Status)
	}

	// List
	envs, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("expected 1 env, got %d", len(envs))
	}

	// Delete
	if err := s.Delete(ctx, env.ID, ""); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Get after delete
	_, err = s.Get(ctx, env.ID, "")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryStore_TenantIsolation(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	envA := &domain.Environment{
		ID:         "env_aaa",
		TenantID:   "user-a",
		UserID:     "user-a",
		Status:     domain.StatusRunning,
		Spec:       domain.EnvironmentSpec{Image: "alpine:latest", NetworkPolicy: domain.NetworkNone},
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(60 * time.Minute),
	}
	envB := &domain.Environment{
		ID:         "env_bbb",
		TenantID:   "user-b",
		UserID:     "user-b",
		Status:     domain.StatusRunning,
		Spec:       domain.EnvironmentSpec{Image: "alpine:latest", NetworkPolicy: domain.NetworkNone},
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(60 * time.Minute),
	}

	if err := s.Create(ctx, envA); err != nil {
		t.Fatalf("create envA: %v", err)
	}
	if err := s.Create(ctx, envB); err != nil {
		t.Fatalf("create envB: %v", err)
	}

	// user-b cannot Get user-a's env
	if _, err := s.Get(ctx, envA.ID, "user-b"); err != ErrNotFound {
		t.Fatalf("Get(envA, user-b): want ErrNotFound, got %v", err)
	}

	// user-a can Get their own env
	if _, err := s.Get(ctx, envA.ID, "user-a"); err != nil {
		t.Fatalf("Get(envA, user-a): %v", err)
	}

	// List(user-a) returns only user-a's env
	list, err := s.List(ctx, "user-a")
	if err != nil {
		t.Fatalf("List(user-a): %v", err)
	}
	if len(list) != 1 || list[0].ID != envA.ID {
		t.Fatalf("List(user-a): want [%s], got %v", envA.ID, list)
	}

	// user-b cannot Delete user-a's env
	if err := s.Delete(ctx, envA.ID, "user-b"); err != ErrNotFound {
		t.Fatalf("Delete(envA, user-b): want ErrNotFound, got %v", err)
	}

	// user-a can Delete their own env
	if err := s.Delete(ctx, envA.ID, "user-a"); err != nil {
		t.Fatalf("Delete(envA, user-a): %v", err)
	}

	// Update cross-tenant: recreate envA, then try to Update with UserID "user-b"
	if err := s.Create(ctx, envA); err != nil {
		t.Fatalf("re-create envA: %v", err)
	}
	crossUpdate := *envA
	crossUpdate.UserID = "user-b"
	if err := s.Update(ctx, &crossUpdate); err != ErrNotFound {
		t.Fatalf("Update(envA as user-b): want ErrNotFound, got %v", err)
	}
}

func TestMemoryStore_ListByTenant(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()
	now := time.Now()

	// Two end-users under the same tenant.
	aliceEnv := &domain.Environment{
		ID: "env_alice", TenantID: "tenant-x", UserID: "user-alice",
		Status: domain.StatusRunning, Spec: domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(60 * time.Minute),
	}
	bobEnv := &domain.Environment{
		ID: "env_bob", TenantID: "tenant-x", UserID: "user-bob",
		Status: domain.StatusRunning, Spec: domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(60 * time.Minute),
	}
	// Unrelated tenant.
	otherEnv := &domain.Environment{
		ID: "env_other", TenantID: "tenant-y", UserID: "user-other",
		Status: domain.StatusRunning, Spec: domain.EnvironmentSpec{Image: "alpine:latest"},
		CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(60 * time.Minute),
	}

	for _, e := range []*domain.Environment{aliceEnv, bobEnv, otherEnv} {
		if err := s.Create(ctx, e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	list, err := s.ListByTenant(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 envs for tenant-x, got %d", len(list))
	}
	ids := map[string]bool{}
	for _, e := range list {
		ids[e.ID] = true
	}
	if !ids["env_alice"] || !ids["env_bob"] {
		t.Fatalf("expected env_alice and env_bob, got %v", ids)
	}
	if ids["env_other"] {
		t.Fatal("env_other from a different tenant must not appear in tenant-x list")
	}
}

func TestMemoryStore_ListSnapshotsByTenant(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()
	now := time.Now()

	snaps := []*domain.Snapshot{
		{ID: "snap_alice", TenantID: "tenant-x", UserID: "user-alice", EnvironmentID: "env_1", ImageID: "img_1", CreatedAt: now},
		{ID: "snap_bob", TenantID: "tenant-x", UserID: "user-bob", EnvironmentID: "env_2", ImageID: "img_2", CreatedAt: now},
		{ID: "snap_other", TenantID: "tenant-y", UserID: "user-other", EnvironmentID: "env_3", ImageID: "img_3", CreatedAt: now},
	}
	for _, snap := range snaps {
		if err := s.CreateSnapshot(ctx, snap); err != nil {
			t.Fatalf("create %s: %v", snap.ID, err)
		}
	}

	list, err := s.ListSnapshotsByTenant(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("ListSnapshotsByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 for tenant-x, got %d", len(list))
	}
	ids := map[string]bool{}
	for _, snap := range list {
		ids[snap.ID] = true
	}
	if !ids["snap_alice"] || !ids["snap_bob"] {
		t.Fatalf("expected snap_alice and snap_bob, got %v", ids)
	}
	if ids["snap_other"] {
		t.Fatal("snap_other from tenant-y must not appear in tenant-x list")
	}
}

func TestMemoryStore_NotFound(t *testing.T) {
	s := NewMemoryStore(10*time.Minute, 60*time.Minute)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent", "")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	err = s.Update(ctx, &domain.Environment{ID: "nonexistent"})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound on update, got %v", err)
	}

	err = s.Delete(ctx, "nonexistent", "")
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
