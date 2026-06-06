package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

func TestFileService_ListFiles_SanitizesPathBeforeEngineCall(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	var gotPath string
	svc := NewFileService(mem, &engine.MockEngine{
		ListFilesFn: func(_ context.Context, _ string, path string) ([]domain.FileInfo, error) {
			gotPath = path
			return []domain.FileInfo{{Name: "passwd", Path: path}}, nil
		},
	}, testLogger(), 1024)

	if _, err := svc.ListFiles(ctx, "env_test", "user", "../../etc"); err != nil {
		t.Fatalf("list files: %v", err)
	}
	if gotPath != "/etc" {
		t.Fatalf("sanitized path: want /etc, got %q", gotPath)
	}
}

func TestFileService_ReadFile_ReturnsSanitizedPathAndOpenReader(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	svc := NewFileService(mem, &engine.MockEngine{
		ReadFileFn: func(_ context.Context, _ string, path string) (io.ReadCloser, error) {
			if path != "/tmp/example.txt" {
				t.Fatalf("engine path: want /tmp/example.txt, got %q", path)
			}
			return io.NopCloser(strings.NewReader("hello")), nil
		},
	}, testLogger(), 1024)

	path, reader, err := svc.ReadFile(ctx, "env_test", "user", "tmp/example.txt")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	defer reader.Close()
	if path != "/tmp/example.txt" {
		t.Fatalf("returned path: want /tmp/example.txt, got %q", path)
	}
}

func TestFileService_WriteFile_ValidatesPathBeforeReadingBody(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	svc := NewFileService(mem, &engine.MockEngine{}, testLogger(), 1024)
	err := svc.WriteFile(ctx, "env_test", "user", "bad\npath", errReader{})
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected ErrInvalidPath before body read, got %v", err)
	}
}

func TestFileService_WriteFile_BodyLimit(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")

	svc := NewFileService(mem, &engine.MockEngine{}, testLogger(), 3)
	err := svc.WriteFile(ctx, "env_test", "user", "/tmp/file.txt", strings.NewReader("toolong"))
	if !errors.Is(err, ErrReadRequestBody) {
		t.Fatalf("expected ErrReadRequestBody, got %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("body should not be read")
}
