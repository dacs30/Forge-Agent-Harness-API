package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"haas/internal/config"
	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var (
	ErrImageRequired     = errors.New("image is required when snapshot_id is not set")
	ErrImageNotAllowed   = errors.New("image not allowed")
	ErrInvalidCPU        = errors.New("cpu must be between 0.1 and 4")
	ErrInvalidMemory     = errors.New("memory_mb must be between 128 and 8192")
	ErrInvalidNetwork    = errors.New("network_policy must be none, egress-limited, or full")
	ErrGetEnvironment    = errors.New("failed to get environment")
	ErrListEnvironments  = errors.New("failed to list environments")
	ErrCreateEnvironment = errors.New("failed to create environment")
	ErrCreateContainer   = errors.New("failed to create container")
	ErrStartContainer    = errors.New("failed to start container")
	ErrDeleteEnvironment = errors.New("failed to delete environment")
	ErrGetSnapshot       = errors.New("failed to get snapshot")
)

type ImageNotAllowedError struct {
	Image string
}

func (e ImageNotAllowedError) Error() string {
	return "image not allowed: " + e.Image
}

func (e ImageNotAllowedError) Is(target error) bool {
	return target == ErrImageNotAllowed
}

type EnvironmentService struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
	config *config.Config
}

func NewEnvironmentService(s store.Store, e engine.Engine, logger *slog.Logger, cfg *config.Config) *EnvironmentService {
	if logger == nil {
		logger = slog.Default()
	}
	return &EnvironmentService{store: s, engine: e, logger: logger, config: cfg}
}

type CreateEnvironmentInput struct {
	Image         string
	CPU           float64
	MemoryMB      int64
	DiskMB        int64
	NetworkPolicy string
	EnvVars       map[string]string
	SnapshotID    string
}

func (s *EnvironmentService) CreateEnvironment(ctx context.Context, tenantID, userID string, input CreateEnvironmentInput) (*domain.Environment, error) {
	req := input

	if req.SnapshotID != "" {
		snap, err := s.store.GetSnapshot(ctx, req.SnapshotID, userID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, store.ErrNotFound
			}
			return nil, fmt.Errorf("%w: %v", ErrGetSnapshot, err)
		}
		req.Image = snap.ImageRef()
	} else {
		if req.Image == "" {
			return nil, ErrImageRequired
		}
		if len(s.config.AllowedImages) > 0 && !imageAllowed(req.Image, s.config.AllowedImages) {
			return nil, ImageNotAllowedError{Image: req.Image}
		}
	}

	if req.CPU <= 0 {
		req.CPU = s.config.DefaultCPU
	}
	if req.MemoryMB <= 0 {
		req.MemoryMB = s.config.DefaultMemoryMB
	}
	if req.DiskMB <= 0 {
		req.DiskMB = s.config.DefaultDiskMB
	}
	if req.NetworkPolicy == "" {
		req.NetworkPolicy = s.config.DefaultNetworkPolicy
	}

	if req.CPU < 0.1 || req.CPU > 4 {
		return nil, ErrInvalidCPU
	}
	if req.MemoryMB < 128 || req.MemoryMB > 8192 {
		return nil, ErrInvalidMemory
	}

	np := domain.NetworkPolicy(req.NetworkPolicy)
	if !np.Valid() {
		return nil, ErrInvalidNetwork
	}

	now := time.Now()
	env := &domain.Environment{
		ID:       "env_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
		TenantID: tenantID,
		UserID:   userID,
		Spec: domain.EnvironmentSpec{
			Image:         req.Image,
			CPU:           req.CPU,
			MemoryMB:      req.MemoryMB,
			DiskMB:        req.DiskMB,
			NetworkPolicy: np,
			EnvVars:       req.EnvVars,
		},
		Status:     domain.StatusCreating,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(s.config.MaxLifetime),
	}

	if err := s.store.Create(ctx, env); err != nil {
		s.logger.Error("failed to store environment", "error", err)
		return nil, fmt.Errorf("%w: %v", ErrCreateEnvironment, err)
	}

	containerID, err := s.engine.CreateContainer(ctx, env)
	if err != nil {
		if delErr := s.store.Delete(ctx, env.ID, userID); delErr != nil {
			s.logger.Error("failed to delete environment after container create failure", "error", delErr, "env_id", env.ID)
		}
		s.logger.Error("failed to create container", "error", err, "env_id", env.ID)
		return nil, fmt.Errorf("%w: %v", ErrCreateContainer, err)
	}

	env.ContainerID = containerID
	if err := s.engine.StartContainer(ctx, containerID); err != nil {
		s.engine.StopContainer(ctx, containerID) //nolint:errcheck // best-effort rollback
		if delErr := s.store.Delete(ctx, env.ID, userID); delErr != nil {
			s.logger.Error("failed to delete environment after container start failure", "error", delErr, "env_id", env.ID)
		}
		s.logger.Error("failed to start container", "error", err, "env_id", env.ID)
		return nil, fmt.Errorf("%w: %v", ErrStartContainer, err)
	}

	// Install the user's registered skills. A failing skill should not block the
	// environment from coming up, so failures are logged and skipped.
	s.injectSkills(ctx, userID, containerID)

	env.Status = domain.StatusRunning
	if err := s.store.Update(ctx, env); err != nil {
		s.logger.Error("failed to update environment status to running", "error", err, "env_id", env.ID)
	}

	return env, nil
}

// injectSkills installs every skill registered for userID into the container's
// skills directory. It is best-effort: a failure on one skill is logged and the
// remaining skills (and the environment) proceed.
func (s *EnvironmentService) injectSkills(ctx context.Context, userID, containerID string) {
	skills, err := s.store.ListSkills(ctx, userID)
	if err != nil {
		s.logger.Error("failed to list skills for injection", "error", err, "user_id", userID)
		return
	}
	for _, skill := range skills {
		archive, err := s.store.GetSkillArchive(ctx, skill.ID, userID)
		if err != nil {
			s.logger.Warn("failed to load skill archive, skipping", "error", err, "skill_id", skill.ID)
			continue
		}
		destDir := skill.InstallDir(s.config.SkillsDir)
		if err := s.engine.ExtractArchive(ctx, containerID, destDir, bytes.NewReader(archive)); err != nil {
			s.logger.Warn("failed to inject skill, skipping", "error", err, "skill_id", skill.ID, "name", skill.Name)
			continue
		}
	}
}

func (s *EnvironmentService) DestroyEnvironment(ctx context.Context, id, userID string) error {
	env, err := s.store.Get(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return fmt.Errorf("%w: %v", ErrGetEnvironment, err)
	}

	if env.ContainerID != "" {
		if err := s.engine.StopContainer(ctx, env.ContainerID); err != nil {
			s.logger.Error("failed to stop container", "error", err, "env_id", id)
		}
	}

	if err := s.store.Delete(ctx, id, userID); err != nil {
		s.logger.Error("failed to delete environment record", "error", err, "env_id", id)
		return fmt.Errorf("%w: %v", ErrDeleteEnvironment, err)
	}

	return nil
}

func (s *EnvironmentService) GetEnvironment(ctx context.Context, id, userID string) (*domain.Environment, error) {
	env, err := s.store.Get(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrGetEnvironment, err)
	}
	return env, nil
}

func (s *EnvironmentService) ListEnvironments(ctx context.Context, userID string) ([]*domain.Environment, error) {
	envs, err := s.store.List(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListEnvironments, err)
	}
	return envs, nil
}

func (s *EnvironmentService) ListTenantEnvironments(ctx context.Context, tenantID string) ([]*domain.Environment, error) {
	envs, err := s.store.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListEnvironments, err)
	}
	return envs, nil
}

func imageAllowed(image string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if image == allowed {
			return true
		}
	}
	return false
}
