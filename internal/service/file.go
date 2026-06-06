package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var (
	ErrPathRequired    = errors.New("path query parameter is required")
	ErrInvalidPath     = errors.New("invalid path")
	ErrReadRequestBody = errors.New("failed to read request body")
	ErrListFiles       = errors.New("failed to list files")
	ErrReadFile        = errors.New("failed to read file")
	ErrWriteFile       = errors.New("failed to write file")
)

type FileService struct {
	store              store.Store
	engine             engine.Engine
	logger             *slog.Logger
	maxFileUploadBytes int64
}

func NewFileService(s store.Store, e engine.Engine, logger *slog.Logger, maxFileUploadBytes int64) *FileService {
	if logger == nil {
		logger = slog.Default()
	}
	return &FileService{
		store:              s,
		engine:             e,
		logger:             logger,
		maxFileUploadBytes: maxFileUploadBytes,
	}
}

func (s *FileService) ListFiles(ctx context.Context, envID, userID, rawPath string) ([]domain.FileInfo, error) {
	env, err := s.getRunningEnv(ctx, envID, userID)
	if err != nil {
		return nil, err
	}

	if rawPath == "" {
		rawPath = "/"
	}
	path, err := sanitizePath(rawPath)
	if err != nil {
		return nil, ErrInvalidPath
	}

	files, err := s.engine.ListFiles(ctx, env.ContainerID, path)
	if err != nil {
		s.logger.Error("list files failed", "error", err, "env_id", env.ID, "path", path)
		return nil, fmt.Errorf("%w: %v", ErrListFiles, err)
	}

	s.updateLastUsed(ctx, env)
	return files, nil
}

func (s *FileService) ReadFile(ctx context.Context, envID, userID, rawPath string) (string, io.ReadCloser, error) {
	env, err := s.getRunningEnv(ctx, envID, userID)
	if err != nil {
		return "", nil, err
	}

	if rawPath == "" {
		return "", nil, ErrPathRequired
	}
	path, err := sanitizePath(rawPath)
	if err != nil {
		return "", nil, ErrInvalidPath
	}

	reader, err := s.engine.ReadFile(ctx, env.ContainerID, path)
	if err != nil {
		s.logger.Error("read file failed", "error", err, "env_id", env.ID, "path", path)
		return "", nil, fmt.Errorf("%w: %v", ErrReadFile, err)
	}

	s.updateLastUsed(ctx, env)
	return path, reader, nil
}

func (s *FileService) WriteFile(ctx context.Context, envID, userID, rawPath string, content io.Reader) error {
	env, err := s.getRunningEnv(ctx, envID, userID)
	if err != nil {
		return err
	}

	if rawPath == "" {
		return ErrPathRequired
	}
	path, err := sanitizePath(rawPath)
	if err != nil {
		return ErrInvalidPath
	}

	data, err := readAllMax(content, s.maxFileUploadBytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrReadRequestBody, err)
	}

	if err := s.engine.WriteFile(ctx, env.ContainerID, path, bytes.NewReader(data)); err != nil {
		s.logger.Error("write file failed", "error", err, "env_id", env.ID, "path", path)
		return fmt.Errorf("%w: %v", ErrWriteFile, err)
	}

	s.updateLastUsed(ctx, env)
	return nil
}

func (s *FileService) getRunningEnv(ctx context.Context, id, userID string) (*domain.Environment, error) {
	env, err := s.store.Get(ctx, id, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrGetEnvironment, err)
	}

	if env.Status != domain.StatusRunning {
		return nil, ErrEnvironmentNotRunning
	}

	return env, nil
}

func (s *FileService) updateLastUsed(ctx context.Context, env *domain.Environment) {
	env.LastUsedAt = time.Now()
	if err := s.store.Update(ctx, env); err != nil {
		s.logger.Error("failed to update environment last used time", "error", err, "env_id", env.ID)
	}
}

func readAllMax(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, fmt.Errorf("limit must be non-negative")
	}
	reader := io.LimitReader(r, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("request body too large")
	}
	return data, nil
}

// sanitizePath resolves traversal sequences by forcing the path to be rooted at
// "/" before cleaning, so "../../etc/passwd" becomes "/etc/passwd" and can
// never escape the container's filesystem root.
func sanitizePath(raw string) (string, error) {
	if strings.ContainsAny(raw, "\x00\r\n") {
		return "", fmt.Errorf("path contains invalid characters")
	}
	return filepath.Clean("/" + raw), nil
}
