package engine

import (
	"context"
	"io"

	"haas/internal/domain"
)

// InteractiveSession is a live, bidirectional TTY exec session on a container.
// Write sends bytes to stdin; Reader returns the merged stdout/stderr TTY stream.
type InteractiveSession interface {
	io.Writer                                              // stdin
	Reader() io.Reader                                     // merged stdout/stderr (raw TTY)
	Resize(ctx context.Context, cols, rows uint) error    // resize the terminal
	Close() error                                          // close the connection
	ExecID() string                                        // Docker exec ID (for exit code)
}

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

	// ExecInteractive opens a TTY exec session with stdin attached.
	// Used for WebSocket-based interactive terminals.
	ExecInteractive(ctx context.Context, containerID string, req domain.ExecRequest) (InteractiveSession, error)

	// ExecExitCode returns the exit code of a completed exec.
	ExecExitCode(ctx context.Context, execID string) (int, error)

	// ListFiles returns file listing at the given path inside the container.
	ListFiles(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error)

	// ReadFile returns the contents of a file inside the container.
	ReadFile(ctx context.Context, containerID string, path string) (io.ReadCloser, error)

	// WriteFile writes content to a file inside the container.
	WriteFile(ctx context.Context, containerID string, path string, content io.Reader) error

	// SnapshotContainer commits the container's filesystem to a local Docker image
	// tagged as "haas-snapshots:{snapshotID}". Returns the Docker image ID.
	SnapshotContainer(ctx context.Context, containerID, snapshotID string) (imageID string, err error)

	// DeleteSnapshotImage removes the local Docker image for a snapshot.
	DeleteSnapshotImage(ctx context.Context, imageID string) error
}
