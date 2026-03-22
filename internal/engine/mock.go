package engine

import (
	"context"
	"io"

	"haas/internal/domain"
)

// MockEngine implements Engine for testing.
type MockEngine struct {
	CreateContainerFn func(ctx context.Context, env *domain.Environment) (string, error)
	StartContainerFn  func(ctx context.Context, containerID string) error
	StopContainerFn   func(ctx context.Context, containerID string) error
	ExecFn            func(ctx context.Context, containerID string, req domain.ExecRequest) (io.ReadCloser, error)
	ExecExitCodeFn    func(ctx context.Context, execID string) (int, error)
	ListFilesFn       func(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error)
	ReadFileFn        func(ctx context.Context, containerID string, path string) (io.ReadCloser, error)
	WriteFileFn       func(ctx context.Context, containerID string, path string, content io.Reader) error
}

func (m *MockEngine) CreateContainer(ctx context.Context, env *domain.Environment) (string, error) {
	if m.CreateContainerFn != nil {
		return m.CreateContainerFn(ctx, env)
	}
	return "mock-container-id", nil
}

func (m *MockEngine) StartContainer(ctx context.Context, containerID string) error {
	if m.StartContainerFn != nil {
		return m.StartContainerFn(ctx, containerID)
	}
	return nil
}

func (m *MockEngine) StopContainer(ctx context.Context, containerID string) error {
	if m.StopContainerFn != nil {
		return m.StopContainerFn(ctx, containerID)
	}
	return nil
}

func (m *MockEngine) Exec(ctx context.Context, containerID string, req domain.ExecRequest) (io.ReadCloser, error) {
	if m.ExecFn != nil {
		return m.ExecFn(ctx, containerID, req)
	}
	return io.NopCloser(io.LimitReader(nil, 0)), nil
}

func (m *MockEngine) ExecExitCode(ctx context.Context, execID string) (int, error) {
	if m.ExecExitCodeFn != nil {
		return m.ExecExitCodeFn(ctx, execID)
	}
	return 0, nil
}

func (m *MockEngine) ListFiles(ctx context.Context, containerID string, path string) ([]domain.FileInfo, error) {
	if m.ListFilesFn != nil {
		return m.ListFilesFn(ctx, containerID, path)
	}
	return nil, nil
}

func (m *MockEngine) ReadFile(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	if m.ReadFileFn != nil {
		return m.ReadFileFn(ctx, containerID, path)
	}
	return io.NopCloser(io.LimitReader(nil, 0)), nil
}

func (m *MockEngine) WriteFile(ctx context.Context, containerID string, path string, content io.Reader) error {
	if m.WriteFileFn != nil {
		return m.WriteFileFn(ctx, containerID, path, content)
	}
	return nil
}
