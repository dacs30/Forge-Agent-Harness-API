package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var (
	ErrEnvironmentNotRunning = errors.New("environment must be running to create a snapshot")
	ErrCreateSnapshot        = errors.New("failed to create snapshot")
	ErrStoreSnapshot         = errors.New("failed to store snapshot")
	ErrListSnapshots         = errors.New("failed to list snapshots")
	ErrDeleteSnapshot        = errors.New("failed to delete snapshot")
)

type SnapshotService struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
}

func NewSnapshotService(s store.Store, e engine.Engine, logger *slog.Logger) *SnapshotService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotService{store: s, engine: e, logger: logger}
}

func (s *SnapshotService) CreateSnapshot(ctx context.Context, tenantID, userID, envID, label string) (*domain.Snapshot, error) {
	env, err := s.store.Get(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrGetEnvironment, err)
	}

	if env.Status != domain.StatusRunning {
		return nil, ErrEnvironmentNotRunning
	}

	snapID := "snap_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]

	imageID, err := s.engine.SnapshotContainer(ctx, env.ContainerID, snapID)
	if err != nil {
		s.logger.Error("failed to snapshot container", "error", err, "env_id", envID)
		return nil, fmt.Errorf("%w: %v", ErrCreateSnapshot, err)
	}

	snap := &domain.Snapshot{
		ID:            snapID,
		TenantID:      tenantID,
		UserID:        userID,
		EnvironmentID: envID,
		ImageID:       imageID,
		Label:         label,
		CreatedAt:     time.Now(),
	}

	if err := s.store.CreateSnapshot(ctx, snap); err != nil {
		s.logger.Error("failed to store snapshot", "error", err, "snap_id", snapID)
		if delErr := s.engine.DeleteSnapshotImage(ctx, imageID); delErr != nil {
			s.logger.Error("failed to clean up snapshot image after store error", "error", delErr, "image_id", imageID)
		}
		return nil, fmt.Errorf("%w: %v", ErrStoreSnapshot, err)
	}

	s.logger.Info("snapshot created", "snap_id", snapID, "env_id", envID)
	return snap, nil
}

func (s *SnapshotService) ListSnapshots(ctx context.Context, userID string) ([]*domain.Snapshot, error) {
	snaps, err := s.store.ListSnapshots(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListSnapshots, err)
	}
	return snaps, nil
}

func (s *SnapshotService) GetSnapshot(ctx context.Context, id, userID string) (*domain.Snapshot, error) {
	snap, err := s.store.GetSnapshot(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrGetSnapshot, err)
	}
	return snap, nil
}

func (s *SnapshotService) ListTenantSnapshots(ctx context.Context, tenantID string) ([]*domain.Snapshot, error) {
	snaps, err := s.store.ListSnapshotsByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListSnapshots, err)
	}
	return snaps, nil
}

func (s *SnapshotService) DeleteSnapshot(ctx context.Context, id, userID string) error {
	snap, err := s.store.GetSnapshot(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return fmt.Errorf("%w: %v", ErrGetSnapshot, err)
	}

	if err := s.engine.DeleteSnapshotImage(ctx, snap.ImageID); err != nil {
		s.logger.Warn("failed to delete snapshot image (may already be removed)", "error", err, "image_id", snap.ImageID)
	}

	if err := s.store.DeleteSnapshot(ctx, id, userID); err != nil {
		s.logger.Error("failed to delete snapshot record", "error", err, "snap_id", id)
		return fmt.Errorf("%w: %v", ErrDeleteSnapshot, err)
	}

	s.logger.Info("snapshot deleted", "snap_id", id)
	return nil
}
