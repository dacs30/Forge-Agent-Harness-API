package engine

import (
	"context"
	"io"

	"haas/internal/domain"
)

type Engine interface {
	// CreateContainer provisions and starts a container per the environment spec.
	// Returns the Docker container ID.
	CreateContainer(ctx context.Context, env *domain.Environment) (containerID string, err error)

	// StartContainer starts a created container.
	StartContainer(ctx context.Context, containerID string) error

	// StopContainer stops and removes a container and its volumes.
	StopContainer(ctx context.Context, containerID string) error

	// Exec runs a command inside a running container.
	// Returns a reader for the multiplexed stdout/stderr stream.
	Exec(ctx context.Context, containerID string, req domain.ExecRequest) (io.ReadCloser, error)

	// ExecExitCode returns the exit code of a completed exec.
	ExecExitCode(ctx context.Context, execID string) (int, error)

	// ListFiles returns file listing at the given path inside the container.
	ListFiles(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error)

	// ReadFile returns the contents of a file inside the container.
	ReadFile(ctx context.Context, containerID string, path string) (io.ReadCloser, error)

	// WriteFile writes content to a file inside the container.
	WriteFile(ctx context.Context, containerID string, path string, content io.Reader) error
}
