package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"haas/internal/domain"
	"haas/internal/engine"
)

func TestReadFile_NonRunningEnvironmentPrecedesMissingPath(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_stopped/files/content", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusConflict, "environment is not running")
}

func TestWriteFile_InvalidPathPrecedesBodyRead(t *testing.T) {
	s, _, l, cfg, mgr := testDeps()
	router := NewRouter(s, &engine.MockEngine{}, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	seedAPIFileEnv(t, s, "env_test", userID)

	req := httptest.NewRequest(http.MethodPut, "/v1/environments/env_test/files/content?path=bad%0Apath", errReadCloser{})
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assertErrorResponse(t, w, http.StatusBadRequest, "invalid path")
}

func TestReadFile_UsesSanitizedPathForHeaders(t *testing.T) {
	s, e, l, cfg, mgr := testDeps()
	mock := e.(*engine.MockEngine)
	mock.ReadFileFn = func(_ context.Context, _ string, path string) (io.ReadCloser, error) {
		if path != "/tmp/report.txt" {
			t.Fatalf("engine path: want /tmp/report.txt, got %q", path)
		}
		return io.NopCloser(strings.NewReader("hello")), nil
	}
	router := NewRouter(s, e, l, cfg, mgr)
	userID, _ := mgr.UserID(testAPIKey)
	seedAPIFileEnv(t, s, "env_test", userID)

	req := httptest.NewRequest(http.MethodGet, "/v1/environments/env_test/files/content?path=tmp/report.txt", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type: want text/plain; charset=utf-8, got %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename=report.txt` {
		t.Fatalf("Content-Disposition: want attachment filename report.txt, got %q", got)
	}
}

func seedAPIFileEnv(t *testing.T, s interface {
	Create(context.Context, *domain.Environment) error
}, id, userID string) {
	t.Helper()
	now := time.Now()
	if err := s.Create(context.Background(), &domain.Environment{
		ID:          id,
		TenantID:    userID,
		UserID:      userID,
		Status:      domain.StatusRunning,
		ContainerID: "container_" + id,
		CreatedAt:   now,
		LastUsedAt:  now,
		ExpiresAt:   now.Add(60 * time.Minute),
	}); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func (errReadCloser) Close() error {
	return nil
}
