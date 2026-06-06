package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
)

func TestExec_EnvironmentNotFoundPrecedesInvalidJSON(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_missing/exec", strings.NewReader(`{`))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusNotFound, "environment not found")
}

func TestExec_NonRunningEnvironmentPrecedesInvalidJSON(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID:         "env_stopped",
		TenantID:   userID,
		UserID:     userID,
		Status:     domain.StatusStopped,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_stopped/exec", strings.NewReader(`{`))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusConflict, "environment is not running")
}

func TestExec_CommandRequired(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	seedAPIFileEnv(t, s, "env_test", userID)

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_test/exec", strings.NewReader(`{"working_dir":"/tmp"}`))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "command is required")
}

func TestExec_StreamsWithTimeoutAndExitCode(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	mock := e.(*engine.MockEngine)
	mock.ExecFn = func(ctx context.Context, _ string, req domain.ExecRequest) (io.ReadCloser, error) {
		if req.TimeoutSeconds != 1 {
			t.Fatalf("timeout_seconds: want 1, got %d", req.TimeoutSeconds)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("exec context should still be active: %v", err)
		}
		return &execIDReadCloser{
			Reader: bytes.NewReader(dockerMux(1, "hello\n")),
			execID: "exec-timeout",
		}, nil
	}
	mock.ExecExitCodeFn = func(ctx context.Context, execID string) (int, error) {
		if execID != "exec-timeout" {
			t.Fatalf("execID: want exec-timeout, got %q", execID)
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("exit-code context should still be active: %v", err)
		}
		return 7, nil
	}
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	seedAPIFileEnv(t, s, "env_test", userID)

	req := httptest.NewRequest(http.MethodPost, "/v1/environments/env_test/exec", strings.NewReader(`{"command":["echo","hello"],"timeout_seconds":1}`))
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `{"stream":"stdout","data":"hello\n"}`) {
		t.Fatalf("stdout event missing from body:\n%s", body)
	}
	if !strings.Contains(body, `{"stream":"exit","data":"7"}`) {
		t.Fatalf("exit event missing from body:\n%s", body)
	}
}

type execIDReadCloser struct {
	*bytes.Reader
	execID string
}

func (r *execIDReadCloser) Close() error {
	return nil
}

func (r *execIDReadCloser) ExecID() string {
	return r.execID
}

func dockerMux(streamType byte, payload string) []byte {
	data := []byte(payload)
	var out bytes.Buffer
	header := make([]byte, 8)
	header[0] = streamType
	binary.BigEndian.PutUint32(header[4:8], uint32(len(data)))
	out.Write(header)
	out.Write(data)
	return out.Bytes()
}
