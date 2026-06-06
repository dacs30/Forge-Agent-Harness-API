package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

func testConfig() *config.Config {
	return &config.Config{
		DefaultCPU:           1,
		DefaultMemoryMB:      512,
		DefaultDiskMB:        1024,
		DefaultNetworkPolicy: string(domain.NetworkNone),
		MaxLifetime:          60 * time.Minute,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnvironmentService_CreateEnvironment_RollsBackStoreOnCreateContainerFailure(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	svc := NewEnvironmentService(mem, &engine.MockEngine{
		CreateContainerFn: func(context.Context, *domain.Environment) (string, error) {
			return "", errors.New("boom")
		},
	}, testLogger(), testConfig())

	_, err := svc.CreateEnvironment(ctx, "tenant", "user", CreateEnvironmentInput{Image: "alpine:latest"})
	if !errors.Is(err, ErrCreateContainer) {
		t.Fatalf("expected ErrCreateContainer, got %v", err)
	}

	envs, err := mem.List(ctx, "user")
	if err != nil {
		t.Fatalf("list environments: %v", err)
	}
	if len(envs) != 0 {
		t.Fatalf("expected rollback to remove environment, got %d records", len(envs))
	}
}

func TestEnvironmentService_CreateEnvironment_SnapshotRestoreSkipsAllowlist(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	if err := mem.CreateSnapshot(ctx, &domain.Snapshot{
		ID:        "snap_test123",
		TenantID:  "tenant",
		UserID:    "user",
		ImageID:   "image-test123",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	cfg := testConfig()
	cfg.AllowedImages = []string{"ubuntu:22.04"}
	var capturedImage string
	svc := NewEnvironmentService(mem, &engine.MockEngine{
		CreateContainerFn: func(_ context.Context, env *domain.Environment) (string, error) {
			capturedImage = env.Spec.Image
			return "container-test123", nil
		},
	}, testLogger(), cfg)

	env, err := svc.CreateEnvironment(ctx, "tenant", "user", CreateEnvironmentInput{SnapshotID: "snap_test123"})
	if err != nil {
		t.Fatalf("create from snapshot: %v", err)
	}
	if capturedImage != "haas-snapshots:snap_test123" {
		t.Fatalf("captured image: want snapshot ref, got %q", capturedImage)
	}
	if env.Spec.Image != capturedImage {
		t.Fatalf("response image: want %q, got %q", capturedImage, env.Spec.Image)
	}
}

func TestEnvironmentService_CreateEnvironment_RunningUpdateFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	svc := NewEnvironmentService(&updateFailStore{Store: mem}, &engine.MockEngine{}, testLogger(), testConfig())

	env, err := svc.CreateEnvironment(ctx, "tenant", "user", CreateEnvironmentInput{Image: "alpine:latest"})
	if err != nil {
		t.Fatalf("create environment should succeed despite update failure: %v", err)
	}
	if env.Status != domain.StatusRunning {
		t.Fatalf("expected returned environment to be running, got %s", env.Status)
	}
}

type updateFailStore struct {
	store.Store
}

func (s *updateFailStore) Update(context.Context, *domain.Environment) error {
	return errors.New("update failed")
}
