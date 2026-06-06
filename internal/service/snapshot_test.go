package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

func TestSnapshotService_CreateSnapshot_CleansUpImageOnStoreFailure(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	var deletedImageID string
	svc := NewSnapshotService(&createSnapshotFailStore{Store: mem}, &engine.MockEngine{
		SnapshotContainerFn: func(context.Context, string, string) (string, error) {
			return "image-test123", nil
		},
		DeleteSnapshotImageFn: func(_ context.Context, imageID string) error {
			deletedImageID = imageID
			return nil
		},
	}, testLogger())

	_, err := svc.CreateSnapshot(ctx, "tenant", "user", "env_test", "label")
	if !errors.Is(err, ErrStoreSnapshot) {
		t.Fatalf("expected ErrStoreSnapshot, got %v", err)
	}
	if deletedImageID != "image-test123" {
		t.Fatalf("expected cleanup of image-test123, got %q", deletedImageID)
	}
}

func TestSnapshotService_DeleteSnapshot_IgnoresImageDeleteFailure(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	if err := mem.CreateSnapshot(ctx, &domain.Snapshot{
		ID:        "snap_test",
		TenantID:  "tenant",
		UserID:    "user",
		ImageID:   "image-test123",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	svc := NewSnapshotService(mem, &engine.MockEngine{
		DeleteSnapshotImageFn: func(context.Context, string) error {
			return errors.New("image already gone")
		},
	}, testLogger())

	if err := svc.DeleteSnapshot(ctx, "snap_test", "user"); err != nil {
		t.Fatalf("delete snapshot should succeed despite image delete failure: %v", err)
	}
	if _, err := mem.GetSnapshot(ctx, "snap_test", "user"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected snapshot record to be deleted, got %v", err)
	}
}

func TestSnapshotService_CreateSnapshot_RequiresRunningEnvironment(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	if err := mem.Create(ctx, &domain.Environment{
		ID:         "env_stopped",
		TenantID:   "tenant",
		UserID:     "user",
		Status:     domain.StatusStopped,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		ExpiresAt:  time.Now().Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	svc := NewSnapshotService(mem, &engine.MockEngine{}, testLogger())

	_, err := svc.CreateSnapshot(ctx, "tenant", "user", "env_stopped", "")
	if !errors.Is(err, ErrEnvironmentNotRunning) {
		t.Fatalf("expected ErrEnvironmentNotRunning, got %v", err)
	}
}

func seedRunningEnv(t *testing.T, s store.Store, id, tenantID, userID string) {
	t.Helper()
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID:          id,
		TenantID:    tenantID,
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container_" + id,
		CreatedAt:   now,
		LastUsedAt:  now,
		ExpiresAt:   now.Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
}

type createSnapshotFailStore struct {
	store.Store
}

func (s *createSnapshotFailStore) CreateSnapshot(context.Context, *domain.Snapshot) error {
	return errors.New("create snapshot failed")
}
