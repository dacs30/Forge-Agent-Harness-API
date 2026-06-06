package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"haas/internal/domain"

	_ "modernc.org/sqlite"
)

func newTestSQLStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := NewSQLStore(db, "sqlite", 5*time.Minute, 30*time.Minute)
	if err != nil {
		t.Fatalf("new sql store: %v", err)
	}
	return s
}

func TestSQLStore_CRUD(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()

	const testUserID = "user_test1"
	if err := s.BootstrapUser(ctx, "keyhash_test1", testUserID); err != nil {
		t.Fatalf("bootstrap user: %v", err)
	}

	env := &domain.Environment{
		ID:     "env_test1",
		UserID: testUserID,
		Status: domain.StatusRunning,
		Spec: domain.EnvironmentSpec{
			Image:         "alpine:latest",
			CPU:           1.0,
			MemoryMB:      512,
			DiskMB:        2048,
			NetworkPolicy: domain.NetworkNone,
			EnvVars:       map[string]string{"FOO": "bar"},
		},
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
		LastUsedAt: time.Now().UTC().Truncate(time.Second),
		ExpiresAt:  time.Now().Add(60 * time.Minute).UTC().Truncate(time.Second),
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
		t.Fatalf("ID: want %s, got %s", env.ID, got.ID)
	}
	if got.Spec.Image != env.Spec.Image {
		t.Fatalf("Image: want %s, got %s", env.Spec.Image, got.Spec.Image)
	}
	if got.Spec.EnvVars["FOO"] != "bar" {
		t.Fatalf("EnvVars: want bar, got %s", got.Spec.EnvVars["FOO"])
	}

	// Update
	env.Status = domain.StatusStopped
	env.ContainerID = "container123"
	if err := s.Update(ctx, env); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = s.Get(ctx, env.ID, "")
	if got.Status != domain.StatusStopped {
		t.Fatalf("Status: want stopped, got %s", got.Status)
	}
	if got.ContainerID != "container123" {
		t.Fatalf("ContainerID: want container123, got %s", got.ContainerID)
	}

	// List
	envs, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("List: want 1, got %d", len(envs))
	}

	// Delete
	if err := s.Delete(ctx, env.ID, ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = s.Get(ctx, env.ID, "")
	if err != ErrNotFound {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
}

func TestSQLStore_NotFound(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent", "")
	if err != ErrNotFound {
		t.Fatalf("Get: want ErrNotFound, got %v", err)
	}

	err = s.Update(ctx, &domain.Environment{ID: "nonexistent"})
	if err != ErrNotFound {
		t.Fatalf("Update: want ErrNotFound, got %v", err)
	}
}

func TestSQLStore_TenantIsolation(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()

	// Bootstrap users so the foreign key constraint is satisfied
	if err := s.BootstrapUser(ctx, "hash-a", "user-a"); err != nil {
		t.Fatalf("BootstrapUser user-a: %v", err)
	}
	if err := s.BootstrapUser(ctx, "hash-b", "user-b"); err != nil {
		t.Fatalf("BootstrapUser user-b: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	envA := &domain.Environment{
		ID:         "env_aaa",
		UserID:     "user-a",
		Status:     domain.StatusRunning,
		Spec:       domain.EnvironmentSpec{Image: "alpine:latest", NetworkPolicy: domain.NetworkNone},
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(60 * time.Minute),
	}
	envB := &domain.Environment{
		ID:         "env_bbb",
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

func TestSQLStore_BootstrapUser(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()

	// First call — should succeed
	if err := s.BootstrapUser(ctx, "hash-abc", "user-123"); err != nil {
		t.Fatalf("BootstrapUser first call: %v", err)
	}

	// Second call with same args — should be idempotent
	if err := s.BootstrapUser(ctx, "hash-abc", "user-123"); err != nil {
		t.Fatalf("BootstrapUser idempotent call: %v", err)
	}

	// Same hash, different userID — key rotation scenario, should succeed and update
	if err := s.BootstrapUser(ctx, "hash-abc", "user-456"); err != nil {
		t.Fatalf("BootstrapUser key rotation: %v", err)
	}
}

func TestSQLStore_ListByTenant(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	makeEnv := func(id, tenantID, userID string) *domain.Environment {
		return &domain.Environment{
			ID: id, TenantID: tenantID, UserID: userID,
			Status: domain.StatusRunning,
			Spec:   domain.EnvironmentSpec{Image: "alpine:latest", NetworkPolicy: domain.NetworkNone},
			CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(60 * time.Minute),
		}
	}

	// Two end-users under tenant-x, one under tenant-y.
	for _, e := range []*domain.Environment{
		makeEnv("env_alice", "tenant-x", "user-alice"),
		makeEnv("env_bob", "tenant-x", "user-bob"),
		makeEnv("env_other", "tenant-y", "user-other"),
	} {
		if err := s.Create(ctx, e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	list, err := s.ListByTenant(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 for tenant-x, got %d", len(list))
	}
	ids := map[string]bool{}
	for _, e := range list {
		ids[e.ID] = true
	}
	if !ids["env_alice"] || !ids["env_bob"] {
		t.Fatalf("expected env_alice and env_bob, got %v", ids)
	}
	if ids["env_other"] {
		t.Fatal("env_other from tenant-y must not appear in tenant-x list")
	}
}

func TestSQLStore_ListSnapshotsByTenant(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	makeSnap := func(id, tenantID, userID string) *domain.Snapshot {
		return &domain.Snapshot{
			ID: id, TenantID: tenantID, UserID: userID,
			EnvironmentID: "env_" + id, ImageID: "img_" + id,
			CreatedAt: now,
		}
	}

	for _, snap := range []*domain.Snapshot{
		makeSnap("snap_alice", "tenant-x", "user-alice"),
		makeSnap("snap_bob", "tenant-x", "user-bob"),
		makeSnap("snap_other", "tenant-y", "user-other"),
	} {
		if err := s.CreateSnapshot(ctx, snap); err != nil {
			t.Fatalf("create snapshot %s: %v", snap.ID, err)
		}
	}

	list, err := s.ListSnapshotsByTenant(ctx, "tenant-x")
	if err != nil {
		t.Fatalf("ListSnapshotsByTenant: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 for tenant-x, got %d", len(list))
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

// TestSQLStore_TenantIDBackfill simulates an existing database created before
// tenant_id was added and verifies that migration backfills tenant_id = user_id.
func TestSQLStore_TenantIDBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Build the old schema — no tenant_id column anywhere.
	for _, stmt := range []string{
		"PRAGMA foreign_keys = ON",
		`CREATE TABLE users (id TEXT PRIMARY KEY, created_at BIGINT NOT NULL)`,
		`CREATE TABLE api_keys (
			key_hash TEXT PRIMARY KEY,
			user_id  TEXT NOT NULL REFERENCES users(id),
			created_at BIGINT NOT NULL)`,
		`CREATE TABLE environments (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL, container_id TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL, last_used_at BIGINT NOT NULL, expires_at BIGINT NOT NULL,
			spec_image TEXT NOT NULL, spec_cpu REAL NOT NULL,
			spec_memory_mb BIGINT NOT NULL, spec_disk_mb BIGINT NOT NULL,
			spec_network_policy TEXT NOT NULL, spec_env_vars TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE snapshots (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL DEFAULT '',
			environment_id TEXT NOT NULL DEFAULT '', image_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '', size BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("setup old schema: %v", err)
		}
	}

	// Seed pre-existing rows using the old column layout.
	now := time.Now().Unix()
	if _, err := db.Exec(
		`INSERT INTO environments VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"env_legacy", "user-legacy", "running", "", now, now, now+3600,
		"alpine:latest", 1.0, 512, 2048, "none", "{}",
	); err != nil {
		t.Fatalf("insert legacy env: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO snapshots VALUES (?,?,?,?,?,?,?)`,
		"snap_legacy", "user-legacy", "env_legacy", "img_legacy", "lbl", 0, now,
	); err != nil {
		t.Fatalf("insert legacy snapshot: %v", err)
	}

	// Run migration via NewSQLStore — should add tenant_id and backfill.
	s, err := NewSQLStore(db, "sqlite", 5*time.Minute, 30*time.Minute)
	if err != nil {
		t.Fatalf("NewSQLStore migration: %v", err)
	}

	ctx := context.Background()

	env, err := s.Get(ctx, "env_legacy", "")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if env.TenantID != "user-legacy" {
		t.Fatalf("env TenantID backfill: want user-legacy, got %q", env.TenantID)
	}

	snap, err := s.GetSnapshot(ctx, "snap_legacy", "")
	if err != nil {
		t.Fatalf("GetSnapshot after migration: %v", err)
	}
	if snap.TenantID != "user-legacy" {
		t.Fatalf("snapshot TenantID backfill: want user-legacy, got %q", snap.TenantID)
	}
}

func TestSQLStore_ListExpired(t *testing.T) {
	s := newTestSQLStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	const testUserID = "user_expired1"
	if err := s.BootstrapUser(ctx, "keyhash_expired1", testUserID); err != nil {
		t.Fatalf("bootstrap user: %v", err)
	}

	active := &domain.Environment{
		ID:         "env_active",
		UserID:     testUserID,
		Status:     domain.StatusRunning,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(30 * time.Minute),
	}
	idle := &domain.Environment{
		ID:         "env_idle",
		UserID:     testUserID,
		Status:     domain.StatusRunning,
		CreatedAt:  now.Add(-20 * time.Minute),
		LastUsedAt: now.Add(-10 * time.Minute), // idle 10 min, timeout is 5 min
		ExpiresAt:  now.Add(10 * time.Minute),
	}
	expired := &domain.Environment{
		ID:         "env_expired",
		UserID:     testUserID,
		Status:     domain.StatusRunning,
		CreatedAt:  now.Add(-60 * time.Minute),
		LastUsedAt: now,
		ExpiresAt:  now.Add(-1 * time.Minute), // past max lifetime
	}

	for _, e := range []*domain.Environment{active, idle, expired} {
		if err := s.Create(ctx, e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	result, err := s.ListExpired(ctx)
	if err != nil {
		t.Fatalf("ListExpired: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("want 2 expired, got %d", len(result))
	}

	ids := map[string]bool{}
	for _, e := range result {
		ids[e.ID] = true
	}
	if !ids["env_idle"] || !ids["env_expired"] {
		t.Fatalf("want env_idle and env_expired, got %v", ids)
	}
}
