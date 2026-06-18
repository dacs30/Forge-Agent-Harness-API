package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var (
	ErrSkillNameRequired    = errors.New("skill name is required")
	ErrInvalidSkillName     = errors.New("invalid skill name")
	ErrInvalidSkillArchive  = errors.New("invalid skill archive: expected a gzip-compressed tar")
	ErrSkillMissingManifest = errors.New("skill archive must contain a top-level SKILL.md")
	ErrSkillTooLarge        = errors.New("skill archive too large")
	ErrListSkills           = errors.New("failed to list skills")
	ErrStoreSkill           = errors.New("failed to store skill")
	ErrDeleteSkill          = errors.New("failed to delete skill")
	ErrInstallSkill         = errors.New("failed to install skill")
)

const skillManifest = "SKILL.md"

// skillMaxDecompressRatio bounds the uncompressed size of a skill archive
// relative to its compressed size, guarding against decompression bombs.
const skillMaxDecompressRatio = 20

// skillNamePattern restricts skill names to a safe set so they can be used
// directly as an install directory name without path-escape risk.
var skillNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

type SkillService struct {
	store           store.Store
	engine          engine.Engine
	logger          *slog.Logger
	skillsDir       string
	maxArchiveBytes int64
}

func NewSkillService(s store.Store, e engine.Engine, logger *slog.Logger, skillsDir string, maxArchiveBytes int64) *SkillService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SkillService{
		store:           s,
		engine:          e,
		logger:          logger,
		skillsDir:       skillsDir,
		maxArchiveBytes: maxArchiveBytes,
	}
}

// RegisterSkill validates a gzip-compressed tar of a skill directory and stores
// it (decompressed) under (userID, name), replacing any existing skill with the
// same name for that user.
func (s *SkillService) RegisterSkill(ctx context.Context, tenantID, userID, name string, archive io.Reader) (*domain.Skill, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}

	tarBytes, err := s.decompressAndValidate(archive)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	skill := &domain.Skill{
		ID:        "skill_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12],
		TenantID:  tenantID,
		UserID:    userID,
		Name:      name,
		SizeBytes: int64(len(tarBytes)),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.store.UpsertSkill(ctx, skill, tarBytes); err != nil {
		s.logger.Error("failed to store skill", "error", err, "name", name)
		return nil, fmt.Errorf("%w: %v", ErrStoreSkill, err)
	}

	s.logger.Info("skill registered", "skill_id", skill.ID, "name", name)
	return skill, nil
}

// InstallSkillToEnvironment validates an archive and extracts it directly into a
// running container, without registering it in the tenant library.
func (s *SkillService) InstallSkillToEnvironment(ctx context.Context, envID, userID, name string, archive io.Reader) error {
	if err := validateSkillName(name); err != nil {
		return err
	}

	env, err := s.store.Get(ctx, envID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return fmt.Errorf("%w: %v", ErrGetEnvironment, err)
	}
	if env.Status != domain.StatusRunning {
		return ErrEnvironmentNotRunning
	}

	tarBytes, err := s.decompressAndValidate(archive)
	if err != nil {
		return err
	}

	destDir := path.Join(s.skillsDir, name)
	if err := s.engine.ExtractArchive(ctx, env.ContainerID, destDir, bytes.NewReader(tarBytes)); err != nil {
		s.logger.Error("failed to install skill into container", "error", err, "env_id", envID, "name", name)
		return fmt.Errorf("%w: %v", ErrInstallSkill, err)
	}

	s.logger.Info("skill installed to environment", "env_id", envID, "name", name)
	return nil
}

func (s *SkillService) ListSkills(ctx context.Context, userID string) ([]*domain.Skill, error) {
	skills, err := s.store.ListSkills(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListSkills, err)
	}
	return skills, nil
}

func (s *SkillService) GetSkill(ctx context.Context, id, userID string) (*domain.Skill, error) {
	skill, err := s.store.GetSkill(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get skill: %v", err)
	}
	return skill, nil
}

func (s *SkillService) ListTenantSkills(ctx context.Context, tenantID string) ([]*domain.Skill, error) {
	skills, err := s.store.ListSkillsByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrListSkills, err)
	}
	return skills, nil
}

func (s *SkillService) DeleteSkill(ctx context.Context, id, userID string) error {
	if err := s.store.DeleteSkill(ctx, id, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.ErrNotFound
		}
		return fmt.Errorf("%w: %v", ErrDeleteSkill, err)
	}
	s.logger.Info("skill deleted", "skill_id", id)
	return nil
}

// decompressAndValidate reads a gzip-compressed tar, enforces the size limits,
// verifies a top-level SKILL.md and safe entry paths, and returns the
// uncompressed tar bytes ready for extraction.
func (s *SkillService) decompressAndValidate(r io.Reader) ([]byte, error) {
	gzData, err := readAllMax(r, s.maxArchiveBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSkillTooLarge, err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSkillArchive, err)
	}
	defer gz.Close()

	maxUncompressed := s.maxArchiveBytes * skillMaxDecompressRatio
	tarBytes, err := readAllMax(gz, maxUncompressed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSkillTooLarge, err)
	}

	if err := validateSkillTar(tarBytes); err != nil {
		return nil, err
	}
	return tarBytes, nil
}

// validateSkillTar walks the tar to confirm a top-level SKILL.md exists and that
// no entry escapes the destination directory.
func validateSkillTar(tarBytes []byte) error {
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	hasManifest := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSkillArchive, err)
		}

		clean := path.Clean(hdr.Name)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("%w: unsafe entry path %q", ErrInvalidSkillArchive, hdr.Name)
		}
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			return fmt.Errorf("%w: links are not allowed (%q)", ErrInvalidSkillArchive, hdr.Name)
		}
		if clean == skillManifest {
			hasManifest = true
		}
	}
	if !hasManifest {
		return ErrSkillMissingManifest
	}
	return nil
}

func validateSkillName(name string) error {
	if name == "" {
		return ErrSkillNameRequired
	}
	if !skillNamePattern.MatchString(name) {
		return ErrInvalidSkillName
	}
	return nil
}
