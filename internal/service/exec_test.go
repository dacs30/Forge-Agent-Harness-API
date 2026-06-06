package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
	"haas/internal/store"
)

func TestExecService_PrepareExec_NotFound(t *testing.T) {
	svc := NewExecService(store.NewMemoryStore(10*time.Minute, 60*time.Minute), &engine.MockEngine{}, testLogger())

	_, err := svc.PrepareExec(context.Background(), "env_missing", "user")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}
}

func TestExecService_StartExec_CommandRequired(t *testing.T) {
	target := &ExecTarget{env: &domain.Environment{ID: "env_test"}}
	svc := NewExecService(store.NewMemoryStore(10*time.Minute, 60*time.Minute), &engine.MockEngine{}, testLogger())

	_, err := svc.StartExec(context.Background(), context.Background(), target, domain.ExecRequest{})
	if !errors.Is(err, ErrCommandRequired) {
		t.Fatalf("expected ErrCommandRequired, got %v", err)
	}
}

func TestExecService_StartExec_LastUsedUpdateFailureIsBestEffort(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemoryStore(10*time.Minute, 60*time.Minute)
	seedRunningEnv(t, mem, "env_test", "tenant", "user")
	svc := NewExecService(&updateFailStore{Store: mem}, &engine.MockEngine{
		ExecFn: func(context.Context, string, domain.ExecRequest) (io.ReadCloser, error) {
			return io.NopCloser(emptyReader{}), nil
		},
	}, testLogger())

	target, err := svc.PrepareExec(ctx, "env_test", "user")
	if err != nil {
		t.Fatalf("prepare exec: %v", err)
	}
	session, err := svc.StartExec(ctx, ctx, target, domain.ExecRequest{Command: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("start exec should succeed despite update failure: %v", err)
	}
	defer session.Reader.Close()
}

func TestExecService_ExitCode(t *testing.T) {
	svc := NewExecService(store.NewMemoryStore(10*time.Minute, 60*time.Minute), &engine.MockEngine{
		ExecExitCodeFn: func(_ context.Context, execID string) (int, error) {
			if execID != "exec-test" {
				t.Fatalf("execID: want exec-test, got %q", execID)
			}
			return 42, nil
		},
	}, testLogger())

	code, ok := svc.ExitCode(context.Background(), &ExecSession{execID: "exec-test", hasExecID: true})
	if !ok {
		t.Fatal("expected exit code to be available")
	}
	if code != 42 {
		t.Fatalf("exit code: want 42, got %d", code)
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}
