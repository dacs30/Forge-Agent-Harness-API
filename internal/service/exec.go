package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

var (
	ErrCommandRequired = errors.New("command is required")
	ErrExecFailed      = errors.New("exec failed")
)

type ExecService struct {
	store  store.Store
	engine engine.Engine
	logger *slog.Logger
}

func NewExecService(s store.Store, e engine.Engine, logger *slog.Logger) *ExecService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ExecService{store: s, engine: e, logger: logger}
}

type ExecTarget struct {
	env *domain.Environment
}

type ExecSession struct {
	Reader    io.ReadCloser
	envID     string
	execID    string
	hasExecID bool
}

func (s *ExecService) PrepareExec(ctx context.Context, envID, userID string) (*ExecTarget, error) {
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

	return &ExecTarget{env: env}, nil
}

func (s *ExecService) StartExec(execCtx, updateCtx context.Context, target *ExecTarget, req domain.ExecRequest) (*ExecSession, error) {
	if len(req.Command) == 0 {
		return nil, ErrCommandRequired
	}

	env := target.env
	s.logger.Info("executing command",
		"env_id", env.ID,
		"command", req.Command,
		"working_dir", req.WorkingDir,
	)

	reader, err := s.engine.Exec(execCtx, env.ContainerID, req)
	if err != nil {
		s.logger.Error("exec failed", "error", err, "env_id", env.ID)
		return nil, fmt.Errorf("%w: %v", ErrExecFailed, err)
	}

	env.LastUsedAt = time.Now()
	if err := s.store.Update(updateCtx, env); err != nil {
		s.logger.Error("failed to update environment last used time", "error", err, "env_id", env.ID)
	}

	session := &ExecSession{
		Reader: reader,
		envID:  env.ID,
	}
	if e, ok := reader.(interface{ ExecID() string }); ok {
		session.execID = e.ExecID()
		session.hasExecID = true
	}
	return session, nil
}

func (s *ExecService) ExitCode(ctx context.Context, session *ExecSession) (int, bool) {
	if !session.hasExecID {
		return 0, false
	}
	exitCode, err := s.engine.ExecExitCode(ctx, session.execID)
	if err != nil {
		s.logger.Error("failed to get exit code", "error", err, "env_id", session.envID)
		return -1, true
	}
	return exitCode, true
}

func (s *ExecService) LogStreamError(session *ExecSession, err error) {
	s.logger.Error("stream error", "error", err, "env_id", session.envID)
}
